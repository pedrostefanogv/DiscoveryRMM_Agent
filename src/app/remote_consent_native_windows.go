//go:build windows

package app

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"

	"discovery/app/core/remotesession"
	"discovery/app/core/winui"
)

// ── Prompt nativo de consentimento ──
//
// Cartão topmost no desktop do usuário acessado, desenhado pelo agente com o
// TEMA do Windows (claro/escuro + accent) — antes era uma janela Win32 com
// controles padrão e sem hierarquia visual. Além do visual, este é o único
// backend do consentimento que mostra:
//
//   - CONTAGEM REGRESSIVA grande (mm:ss) + barra de progresso;
//   - a AÇÃO PADRÃO explícita ("Se você não responder, o acesso será NEGADO
//     automaticamente"), que segue o timeoutAction da política do servidor.
//
// O Show-ADTInstallationPrompt do PSADT (fallback em remote_consent_prompt.go)
// não tem timer: só os diálogos de Welcome/Restart do toolkit desenham contagem,
// e nenhum deles devolve Permitir/Negar. Por isso o prompt nativo é o principal.

const (
	consentClassName = "DiscoveryRmmConsentPrompt"
	consentWidth     = 640
	consentHeight    = 400
	consentRadius    = 16
	consentPad       = 28
	consentTimerID   = 1
	// consentTickMS: ~5 repinturas por segundo mantêm a barra suave sem custo.
	consentTickMS = 200
	// consentWMAppCancel é a mensagem usada para fechar o prompt de outra
	// goroutine (cancelamento do servidor/worker).
	consentWMAppCancel = 0x8000 + 42
)

// consentButton identifica os botões do prompt.
type consentButton int

const (
	consentButtonNone consentButton = iota
	consentButtonAllow
	consentButtonDeny
)

// consentFonts agrupa as fontes do prompt (liberadas no WM_DESTROY).
type consentFonts struct {
	title         uintptr
	subtitle      uintptr
	body          uintptr
	countLabel    uintptr
	countValue    uintptr
	defaultAction uintptr
	button        uintptr
	hint          uintptr
}

func (f *consentFonts) release() {
	winui.DeleteGDIObject(f.title)
	winui.DeleteGDIObject(f.subtitle)
	winui.DeleteGDIObject(f.body)
	winui.DeleteGDIObject(f.countLabel)
	winui.DeleteGDIObject(f.countValue)
	winui.DeleteGDIObject(f.defaultAction)
	winui.DeleteGDIObject(f.button)
	winui.DeleteGDIObject(f.hint)
}

// consentLayout devolve os retângulos do cartão (coordenadas fixas do desenho).
func consentLayout() (logo, title, subtitle, divider, messageBox, messageText,
	countLabel, countValue, defaultAction, progressTrack, allow, deny, hint winui.Rect) {
	logo = winui.Rect{X: consentPad, Y: 26, W: 44, H: 44}
	headerLeft := logo.Right() + 16
	headerWidth := consentWidth - headerLeft - consentPad
	title = winui.Rect{X: headerLeft, Y: 26, W: headerWidth, H: 32}
	subtitle = winui.Rect{X: headerLeft, Y: 60, W: headerWidth, H: 20}
	divider = winui.Rect{X: consentPad, Y: 104, W: consentWidth - 2*consentPad, H: 1}
	messageBox = winui.Rect{X: consentPad, Y: 120, W: consentWidth - 2*consentPad, H: 88}
	messageText = messageBox.Inset(16, 12)
	countLabel = winui.Rect{X: consentPad, Y: 220, W: 240, H: 18}
	countValue = winui.Rect{X: consentPad, Y: 238, W: 120, H: 40}
	defaultAction = winui.Rect{X: consentPad + 128, Y: 244, W: consentWidth - 2*consentPad - 128, H: 40}
	progressTrack = winui.Rect{X: consentPad, Y: 292, W: consentWidth - 2*consentPad, H: 8}
	deny = winui.Rect{X: consentWidth - consentPad - 140, Y: 316, W: 140, H: 42}
	allow = winui.Rect{X: deny.X - 12 - 140, Y: 316, W: 140, H: 42}
	hint = winui.Rect{X: consentPad, Y: 330, W: allow.X - consentPad - 16, H: 18}
	return
}

// consentNativeState é o estado do prompt (campos de desenho só na thread DA
// janela; decisão protegida por mutex).
type consentNativeState struct {
	mu       sync.Mutex
	req      remotesession.ConsentRequest
	decided  bool
	decision remotesession.ConsentDecision
	resultCh chan remotesession.ConsentDecision

	deadline time.Time
	total    time.Duration

	// desenho
	theme   winui.Theme
	fonts   consentFonts
	hover   consentButton
	pressed consentButton
	focus   consentButton
}

// consentTimeoutDecision aplica a ação de timeout da política do servidor.
func consentTimeoutDecision(req remotesession.ConsentRequest) remotesession.ConsentDecision {
	accepted := strings.EqualFold(strings.TrimSpace(req.TimeoutAction), "approve")
	outcome := "timeout-denied"
	if accepted {
		outcome = "timeout-granted"
	}
	return remotesession.ConsentDecision{
		Accepted:      accepted,
		Auto:          true,
		Outcome:       outcome,
		Reason:        "timeout",
		PromptBackend: "native",
		DecidedAt:     time.Now().UTC(),
	}
}

// consentStoppedDecision representa sessão cancelada pelo servidor/worker.
func consentStoppedDecision() remotesession.ConsentDecision {
	return remotesession.ConsentDecision{
		Accepted:      false,
		Auto:          true,
		Outcome:       "stopped-by-server",
		Reason:        "stopped_by_server",
		PromptBackend: "native",
		DecidedAt:     time.Now().UTC(),
	}
}

// consentDeniedDecision representa recusa do usuário (botão/X/Esc).
func consentDeniedDecision(reason string) remotesession.ConsentDecision {
	return remotesession.ConsentDecision{
		Accepted:      false,
		Outcome:       "denied",
		Reason:        reason,
		PromptBackend: "native",
		DecidedAt:     time.Now().UTC(),
	}
}

// timeoutApproves indica se a ação padrão (fim do tempo) é permitir.
func (s *consentNativeState) timeoutApproves() bool {
	return strings.EqualFold(strings.TrimSpace(s.req.TimeoutAction), "approve")
}

// remaining devolve o tempo restante até o fim da contagem.
func (s *consentNativeState) remaining() time.Duration {
	remaining := time.Until(s.deadline)
	if remaining < 0 {
		remaining = 0
	}
	return remaining
}

// progressRatio devolve 1.0 no início e 0.0 no fim da contagem.
func (s *consentNativeState) progressRatio() float64 {
	if s.total <= 0 {
		return 0
	}
	ratio := float64(s.remaining()) / float64(s.total)
	if ratio < 0 {
		return 0
	}
	if ratio > 1 {
		return 1
	}
	return ratio
}

// titleText é o título do cartão.
func (s *consentNativeState) titleText() string {
	title := strings.TrimSpace(s.req.Title)
	if title == "" {
		title = "Autorização de acesso remoto"
	}
	return title
}

// messageText é a mensagem enviada pelo servidor (com o nome do operador).
func (s *consentNativeState) messageText() string {
	message := strings.TrimSpace(s.req.Message)
	if message == "" {
		message = "O suporte solicita acesso remoto ao seu computador. Você autoriza?"
	}
	return message
}

// finish registra a decisão, encerra a janela e devolve o resultado.
func (s *consentNativeState) finish(hwnd uintptr, decision remotesession.ConsentDecision) {
	s.mu.Lock()
	if s.decided {
		s.mu.Unlock()
		return
	}
	s.decided = true
	s.decision = decision
	s.mu.Unlock()

	select {
	case s.resultCh <- decision:
	default:
	}
	if hwnd != 0 {
		winui.KillTimer(hwnd, consentTimerID)
		winui.DestroyWindow(hwnd)
	}
}

// activate dispara a ação de um botão.
func (s *consentNativeState) activate(hwnd uintptr, button consentButton) {
	switch button {
	case consentButtonAllow:
		s.finish(hwnd, remotesession.ConsentDecision{
			Accepted:      true,
			Outcome:       "granted",
			Reason:        "user_allowed",
			PromptBackend: "native",
			DecidedAt:     time.Now().UTC(),
		})
	case consentButtonDeny:
		s.finish(hwnd, consentDeniedDecision("user_denied"))
	}
}

// buttonAt devolve o botão sob o ponto informado.
func (s *consentNativeState) buttonAt(x, y int) consentButton {
	_, _, _, _, _, _, _, _, _, _, allow, deny, _ := consentLayout()
	if allow.Contains(x, y) {
		return consentButtonAllow
	}
	if deny.Contains(x, y) {
		return consentButtonDeny
	}
	return consentButtonNone
}

// consentWndProc é o procedimento da janela do prompt.
func consentWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	raw, ok := consentStates.Load(hwnd)
	if !ok {
		return winui.DefWindowProc(hwnd, msg, wparam, lparam)
	}
	state, _ := raw.(*consentNativeState)

	switch msg {
	case winui.MsgEraseBkgnd:
		return 1
	case winui.MsgPaint:
		hdc, paint := winui.BeginPaint(hwnd)
		state.paint(hdc)
		winui.EndPaint(hwnd, &paint)
		return 0
	case winui.MsgPrintClient:
		state.paint(wparam)
		return 0
	case winui.MsgTimer:
		if state.remaining() <= 0 {
			state.finish(hwnd, consentTimeoutDecision(state.req))
			return 0
		}
		winui.Invalidate(hwnd)
		return 0
	case winui.MsgMouseMove:
		hover := state.buttonAt(consentLParamX(lparam), consentLParamY(lparam))
		if hover != state.hover {
			state.hover = hover
			winui.Invalidate(hwnd)
		}
		winui.TrackMouseLeave(hwnd)
		return 0
	case winui.MsgMouseLeave:
		if state.hover != consentButtonNone {
			state.hover = consentButtonNone
			winui.Invalidate(hwnd)
		}
		return 0
	case winui.MsgLButtonDown:
		button := state.buttonAt(consentLParamX(lparam), consentLParamY(lparam))
		if button != consentButtonNone {
			state.pressed = button
			state.focus = button
			winui.SetCapture(hwnd)
			winui.Invalidate(hwnd)
		}
		return 0
	case winui.MsgLButtonUp:
		pressed := state.pressed
		state.pressed = consentButtonNone
		winui.ReleaseCapture()
		winui.Invalidate(hwnd)
		if pressed != consentButtonNone &&
			state.buttonAt(consentLParamX(lparam), consentLParamY(lparam)) == pressed {
			state.activate(hwnd, pressed)
		}
		return 0
	case winui.MsgSetCursor:
		if state.buttonAt(consentLParamX(lparam), consentLParamY(lparam)) != consentButtonNone {
			winui.SetCursorHand()
		} else {
			winui.SetCursorArrow()
		}
		return 1
	case winui.MsgKeyDown:
		return state.handleKey(hwnd, wparam)
	case consentWMAppCancel:
		state.finish(hwnd, consentStoppedDecision())
		return 0
	case winui.MsgClose:
		// Fechar pelo X equivale a recusar (fail closed).
		state.finish(hwnd, consentDeniedDecision("user_closed"))
		return 0
	case winui.MsgDestroy:
		state.fonts.release()
		consentStates.Delete(hwnd)
		winui.PostQuitMessage(0)
		return 0
	}
	return winui.DefWindowProc(hwnd, msg, wparam, lparam)
}

// handleKey trata teclado: Tab/setas alternam o foco, Enter confirma a ação
// padrão e Esc recusa.
func (s *consentNativeState) handleKey(hwnd, key uintptr) uintptr {
	switch key {
	case winui.VkTab, winui.VkLeft, winui.VkRight:
		if s.focus == consentButtonAllow {
			s.focus = consentButtonDeny
		} else {
			s.focus = consentButtonAllow
		}
		winui.Invalidate(hwnd)
		return 0
	case winui.VkReturn, winui.VkSpace:
		if s.focus == consentButtonNone {
			s.focus = consentButtonDeny
			if s.timeoutApproves() {
				s.focus = consentButtonAllow
			}
		}
		s.activate(hwnd, s.focus)
		return 0
	case winui.VkEscape:
		s.finish(hwnd, consentDeniedDecision("user_closed"))
		return 0
	}
	return 0
}

// consentLParamX/Y extraem as coordenadas (16 bits com sinal) do LPARAM.
func consentLParamX(lparam uintptr) int { return int(int16(lparam & 0xFFFF)) }
func consentLParamY(lparam uintptr) int { return int(int16((lparam >> 16) & 0xFFFF)) }

// drawConsentBadge desenha o selo do cabeçalho: um "monitor" no cartão, com
// fundo levemente tingido pelo accent do Windows (funciona em claro e escuro).
func drawConsentBadge(hdc uintptr, rect winui.Rect, theme winui.Theme) {
	winui.FillRoundRectBorder(hdc, rect, 12,
		theme.Card.Blend(theme.Accent, 16),
		theme.Card.Blend(theme.Accent, 40))
	screen := winui.Rect{X: rect.X + 10, Y: rect.Y + 12, W: rect.W - 20, H: 17}
	winui.StrokeRoundRect(hdc, screen, 4, 2, theme.Accent)
	winui.FillRect(hdc, winui.Rect{X: rect.X + rect.W/2 - 4, Y: screen.Bottom() + 3, W: 8, H: 2}, theme.Accent)
	winui.FillRoundRect(hdc, winui.Rect{X: rect.X + rect.W/2 - 8, Y: screen.Bottom() + 8, W: 16, H: 3}, 1, theme.Accent)
}

// paint desenha o cartão inteiro.
func (s *consentNativeState) paint(screenDC uintptr) {
	memDC, bitmap, oldBitmap := winui.BufferedPaint(screenDC, consentWidth, consentHeight)
	hdc := memDC
	if hdc == 0 {
		hdc = screenDC
	} else {
		defer winui.FlushBufferedPaint(screenDC, memDC, bitmap, oldBitmap, consentWidth, consentHeight)
	}

	theme := s.theme
	logo, title, subtitle, divider, messageBox, messageText, countLabel, countValue,
		defaultAction, progressTrack, allow, deny, hint := consentLayout()
	card := winui.Rect{W: consentWidth, H: consentHeight}

	// Ação padrão (fim do tempo) define a cor de destaque do bloco de contagem.
	actionColor := theme.Danger
	if s.timeoutApproves() {
		actionColor = theme.Success
	}

	// Cartão: fundo, faixa de destaque do Windows e borda.
	winui.FillRoundRect(hdc, card, consentRadius, theme.Card)
	winui.FillRect(hdc, winui.Rect{X: 0, Y: 2, W: 5, H: consentHeight - 4}, theme.Accent)
	winui.StrokeRoundRect(hdc, card, consentRadius, 1, theme.Border)

	// Selo do acesso remoto (desenhado no tema; independe de asset externo).
	drawConsentBadge(hdc, logo, theme)

	winui.DrawTextSingle(hdc, s.titleText(), title, s.fonts.title, theme.Text, winui.AlignLeft)
	winui.DrawTextSingle(hdc, "Acesso remoto", subtitle, s.fonts.subtitle, theme.TextMuted, winui.AlignLeft)
	winui.FillRect(hdc, divider, theme.Border)

	// Caixa da mensagem (mesma hierarquia do diálogo Fluent do PSADT).
	winui.FillRoundRectBorder(hdc, messageBox, 8, theme.Surface, theme.Border)
	winui.DrawTextWrapped(hdc, s.messageText(), messageText, s.fonts.body, theme.Text, winui.AlignLeft)

	// Contagem regressiva + ação padrão.
	winui.DrawTextSingle(hdc, "Tempo para responder", countLabel, s.fonts.countLabel, theme.TextMuted, winui.AlignLeft)
	winui.DrawTextSingle(hdc, consentCountdownClock(s.remaining()), countValue, s.fonts.countValue, actionColor, winui.AlignLeft)
	winui.DrawTextWrapped(hdc, consentDefaultActionText(s.req.TimeoutAction), defaultAction,
		s.fonts.defaultAction, actionColor, winui.AlignLeft)

	// Barra de progresso do tempo restante.
	winui.FillRoundRect(hdc, progressTrack, 4, theme.Border)
	progress := progressTrack
	progress.W = int(float64(progressTrack.W) * s.progressRatio())
	if progress.W < 8 {
		progress.W = 8
	}
	if progress.W > progressTrack.W {
		progress.W = progressTrack.W
	}
	winui.FillRoundRect(hdc, progress, 4, actionColor)

	// Dica de teclado (a ação padrão também é o Enter).
	winui.DrawTextSingle(hdc, "Enter confirma a ação padrão · Esc recusa", hint, s.fonts.hint, theme.TextMuted, winui.AlignLeft)

	// Botões.
	allowStyle := winui.PillButton(
		theme.Success,
		theme.Success.Lighten(12),
		theme.Success.Darken(12),
		theme.SuccessText,
		9,
	)
	denyStyle := winui.OutlineButton(theme, 9)
	winui.PaintButton(hdc, allow, "Permitir", s.fonts.button, allowStyle,
		s.hover == consentButtonAllow, s.pressed == consentButtonAllow,
		s.focus == consentButtonAllow, theme.Accent)
	winui.PaintButton(hdc, deny, "Negar", s.fonts.button, denyStyle,
		s.hover == consentButtonDeny, s.pressed == consentButtonDeny,
		s.focus == consentButtonDeny, theme.Accent)
}

// consentThemeLoader lê o tema do usuário; var para testes de pré-visualização.
var consentThemeLoader = winui.DetectTheme

var (
	consentClassOnce sync.Once
	consentClassErr  error
	consentStates    sync.Map // hwnd -> *consentNativeState
)

func ensureConsentClass() error {
	consentClassOnce.Do(func() {
		consentClassErr = winui.RegisterWindowClass(
			consentClassName,
			windows.NewCallback(consentWndProc),
			winui.ClassStyleDropShadow,
		)
	})
	return consentClassErr
}

// nativeConsentPrompt abre a janela e devolve a decisão. O contexto cancelado
// fecha o prompt imediatamente e produz stopped-by-server.
func nativeConsentPrompt(ctx context.Context, req remotesession.ConsentRequest) (remotesession.ConsentDecision, bool) {
	if err := ensureConsentClass(); err != nil {
		return remotesession.ConsentDecision{}, false
	}
	if ctx.Err() != nil {
		return consentStoppedDecision(), true
	}

	type nativeResult struct {
		decision remotesession.ConsentDecision
		ok       bool
	}
	resultCh := make(chan nativeResult, 1)
	go func() {
		decision, ok := runNativeConsentWindow(ctx, req)
		resultCh <- nativeResult{decision: decision, ok: ok}
	}()
	result := <-resultCh
	return result.decision, result.ok
}

// runNativeConsentWindow cria a janela, roda o loop de mensagens e devolve a
// decisão do usuário (ou a ação padrão, no fim da contagem).
func runNativeConsentWindow(ctx context.Context, req remotesession.ConsentRequest) (remotesession.ConsentDecision, bool) {
	timeout := req.Timeout
	// Nunca esperar mais do que o deadline do servidor: o prompt precisa
	// expirar ANTES do contexto ser cancelado para aplicar a ação padrão
	// (cancelamento de contexto vira "stopped-by-server", não timeout).
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}
	if timeout < time.Second {
		timeout = time.Second
	}
	state := &consentNativeState{
		req:      req,
		total:    timeout,
		deadline: time.Now().Add(timeout),
		resultCh: make(chan remotesession.ConsentDecision, 1),
		hover:    consentButtonNone,
		pressed:  consentButtonNone,
		focus:    consentButtonDeny,
	}
	if state.timeoutApproves() {
		state.focus = consentButtonAllow
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "Autorização de acesso remoto"
	}
	x, y := winui.CenteredOnWorkArea(consentWidth, consentHeight)
	hwnd, err := winui.CreateWindow(
		consentClassName,
		title,
		winui.ExStyleTopmost|winui.ExStyleToolWindow,
		winui.WinStylePopup|winui.WinStyleClipSiblings,
		x, y, consentWidth, consentHeight,
	)
	if err != nil {
		return remotesession.ConsentDecision{}, false
	}
	consentStates.Store(hwnd, state)

	winui.SetRoundedRegion(hwnd, consentWidth, consentHeight, consentRadius)
	state.theme = consentThemeLoader()
	state.fonts = consentFonts{
		title:         winui.CreateFont(25, 600),
		subtitle:      winui.CreateFont(15, 400),
		body:          winui.CreateFont(16, 400),
		countLabel:    winui.CreateFont(13, 400),
		countValue:    winui.CreateFont(36, 600),
		defaultAction: winui.CreateFont(14, 600),
		button:        winui.CreateFont(15, 600),
		hint:          winui.CreateFont(12, 400),
	}

	winui.ShowWindow(hwnd, winui.SwShowNormal)
	winui.EnsureTopMost(hwnd)
	winui.SetForegroundWindow(hwnd)
	winui.SetFocusTo(hwnd)
	winui.UpdateWindow(hwnd)
	winui.SetTimer(hwnd, consentTimerID, consentTickMS)

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			winui.PostMessage(hwnd, consentWMAppCancel, 0, 0)
		case <-done:
		}
	}()

	var msg winui.Msg
	for winui.NextMessage(&msg) > 0 {
		winui.TranslateMessage(&msg)
		winui.DispatchMessage(&msg)
	}

	state.mu.Lock()
	decided := state.decided
	decision := state.decision
	state.mu.Unlock()
	if decided {
		return decision, true
	}
	return consentStoppedDecision(), true
}
