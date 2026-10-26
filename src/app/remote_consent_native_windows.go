//go:build windows

package app

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"

	"discovery/app/core/notifyui"
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
// O antigo fallback PSADT (Show-ADTInstallationPrompt) foi removido: o prompt
// PSADT não tinha timer e não devolvia Permitir/Negar — por isso este prompt
// nativo é o único backend do consentimento.

const (
	consentClassName = "DiscoveryRmmConsentPrompt"
	// 560x336: o cartão anterior (640x400) ocupava espaço demais no canto da
	// tela. O layout abaixo é a versão compacta (~12% menor na largura).
	consentWidth   = 560
	consentHeight  = 336
	consentRadius  = 16
	consentPad     = 24
	consentTimerID = 1
	// consentTickMS: ~5 repinturas por segundo mantêm a barra suave sem custo.
	consentTickMS = 200
	// consentScreenMargin é o recuo do cartão em relação às bordas direita e
	// inferior da área útil: o prompt nasce no canto inferior direito (como um
	// toast do Windows/PSADT), sem cobrir o centro da tela do usuário.
	consentScreenMargin = 24
	// consentSystemCornerRadius é o raio (px físicos) com que o DWM arredonda a
	// janela no Windows 11. Serve ao hit-testing: sem região de recorte, a
	// janela continua retangular para o mouse e os cantos transparentes
	// precisam deixar o clique atravessar (ver consentInsideRoundedCard).
	consentSystemCornerRadius = 8
	// consentWMAppCancel é a mensagem usada para fechar o prompt de outra
	// goroutine (cancelamento do servidor/worker).
	consentWMAppCancel = 0x8000 + 42
	// consentMessageMaxExtraPx é o quanto a caixa de mensagem pode crescer
	// (96 DPI) para caber textos longos (o servidor aceita até 500 caracteres).
	// O prompt cresce para cima, ancorado no canto inferior direito.
	consentMessageMaxExtraPx = 160
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

// consentLayout devolve os retângulos do cartão no layout de projeto (96 DPI).
func consentLayout() (logo, title, subtitle, divider, messageBox, messageText,
	countLabel, countValue, defaultAction, progressTrack, allow, deny, hint winui.Rect) {
	return consentLayoutFor(1, 0)
}

// consentLayoutFor escala o layout de projeto pelo DPI e cresce a caixa de
// mensagem em extraHeight px físicos (empurrando contador/barra/botões para
// baixo). Sem escala/extra devolve o layout base usado pelos testes.
func consentLayoutFor(scale float64, extraHeight int) (logo, title, subtitle, divider, messageBox, messageText,
	countLabel, countValue, defaultAction, progressTrack, allow, deny, hint winui.Rect) {
	logo, title, subtitle, divider, messageBox, _, countLabel, countValue,
		defaultAction, progressTrack, allow, deny, hint = consentLayoutBase()
	if scale == 0 {
		scale = 1
	}
	if scale != 1 {
		scaleRect := func(r winui.Rect) winui.Rect {
			return winui.Rect{
				X: winui.ScalePx(r.X, scale),
				Y: winui.ScalePx(r.Y, scale),
				W: winui.ScalePx(r.W, scale),
				H: winui.ScalePx(r.H, scale),
			}
		}
		logo, title, subtitle, divider = scaleRect(logo), scaleRect(title), scaleRect(subtitle), scaleRect(divider)
		messageBox = scaleRect(messageBox)
		countLabel, countValue, defaultAction = scaleRect(countLabel), scaleRect(countValue), scaleRect(defaultAction)
		progressTrack, allow, deny, hint = scaleRect(progressTrack), scaleRect(allow), scaleRect(deny), scaleRect(hint)
	}
	insetX := winui.ScalePx(14, scale)
	insetY := winui.ScalePx(10, scale)
	if extraHeight > 0 {
		messageBox.H += extraHeight
		countLabel.Y += extraHeight
		countValue.Y += extraHeight
		defaultAction.Y += extraHeight
		progressTrack.Y += extraHeight
		allow.Y += extraHeight
		deny.Y += extraHeight
		hint.Y += extraHeight
	}
	messageText = messageBox.Inset(insetX, insetY)
	return
}

// consentLayoutBase é o layout de projeto em 96 DPI (sem escala nem extra).
func consentLayoutBase() (logo, title, subtitle, divider, messageBox, messageText,
	countLabel, countValue, defaultAction, progressTrack, allow, deny, hint winui.Rect) {
	logo = winui.Rect{X: consentPad, Y: 20, W: 40, H: 40}
	headerLeft := logo.Right() + 14
	headerWidth := consentWidth - headerLeft - consentPad
	title = winui.Rect{X: headerLeft, Y: 20, W: headerWidth, H: 28}
	subtitle = winui.Rect{X: headerLeft, Y: 52, W: headerWidth, H: 18}
	divider = winui.Rect{X: consentPad, Y: 86, W: consentWidth - 2*consentPad, H: 1}
	messageBox = winui.Rect{X: consentPad, Y: 98, W: consentWidth - 2*consentPad, H: 84}
	messageText = messageBox.Inset(14, 10)
	countLabel = winui.Rect{X: consentPad, Y: 194, W: 200, H: 16}
	countValue = winui.Rect{X: consentPad, Y: 210, W: 100, H: 34}
	defaultAction = winui.Rect{X: consentPad + 110, Y: 216, W: consentWidth - 2*consentPad - 110, H: 34}
	progressTrack = winui.Rect{X: consentPad, Y: 256, W: consentWidth - 2*consentPad, H: 7}
	deny = winui.Rect{X: consentWidth - consentPad - 128, Y: 274, W: 128, H: 38}
	allow = winui.Rect{X: deny.X - 10 - 128, Y: 274, W: 128, H: 38}
	hint = winui.Rect{X: consentPad, Y: 284, W: allow.X - consentPad - 12, H: 16}
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
	// systemCorners: o DWM arredonda a janela com antialiasing (Windows 11).
	// Quando true, o cartão é pintado reto e o compositor recorta os cantos —
	// evita o serrilhado da região binária do SetWindowRgn.
	systemCorners bool

	// Geometria efetiva em pixels físicos (escalada pelo DPI do monitor) e
	// altura extra da caixa de mensagem para textos longos. dwmCornerRadius é
	// o raio do canto do DWM já escalado (hit-testing).
	scale           float64
	width           int
	height          int
	radius          int
	extraHeight     int
	dwmCornerRadius int
}

// metrics devolve a geometria efetiva da janela (fallback: projeto 96 DPI).
func (s *consentNativeState) metrics() (width, height, radius int, scale float64) {
	scale = s.scale
	if scale <= 0 {
		scale = 1
	}
	width, height, radius = s.width, s.height, s.radius
	if width <= 0 {
		width = consentWidth
	}
	if height <= 0 {
		height = consentHeight + s.extraHeight
	}
	if radius <= 0 {
		radius = consentRadius
	}
	return width, height, radius, scale
}

// layout devolve os retângulos do cartão na escala/altura efetivas.
func (s *consentNativeState) layout() (logo, title, subtitle, divider, messageBox, messageText,
	countLabel, countValue, defaultAction, progressTrack, allow, deny, hint winui.Rect) {
	scale := s.scale
	if scale <= 0 {
		scale = 1
	}
	return consentLayoutFor(scale, s.extraHeight)
}

// insideRoundedCard informa se o ponto (coordenadas de cliente) está dentro do
// cartão visível quando o DWM arredonda a janela (o raio acompanha o DPI).
func (s *consentNativeState) insideRoundedCard(x, y int) bool {
	width, height, _, _ := s.metrics()
	corner := s.dwmCornerRadius
	if corner <= 0 {
		corner = consentSystemCornerRadius
	}
	return consentInsideRoundedCardAt(x, y, width, height, corner)
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
	_, _, _, _, _, _, _, _, _, _, allow, deny, _ := s.layout()
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
	case winui.MsgNCHitTest:
		// Com os cantos do DWM o recorte é só visual: a janela segue
		// retangular para o mouse. Nos cantos transparentes devolve
		// HTTRANSPARENT para o clique atingir o app de baixo (antes a região
		// do SetWindowRgn fazia esse recorte de input).
		if state.systemCorners {
			x, y := winui.ScreenToClient(hwnd, consentLParamX(lparam), consentLParamY(lparam))
			if !state.insideRoundedCard(x, y) {
				return winui.HtTransparent
			}
		}
		return winui.DefWindowProc(hwnd, msg, wparam, lparam)
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

// consentInsideRoundedCard informa se o ponto (coordenadas de cliente) está
// dentro do cartão visível no layout de projeto (96 DPI). Os cantos são os do
// arredondamento do DWM no Windows 11: um ponto fora da curva não deve capturar
// o clique (a janela sem região continua retangular para o mouse).
func consentInsideRoundedCard(x, y int) bool {
	return consentInsideRoundedCardAt(x, y, consentWidth, consentHeight, consentSystemCornerRadius)
}

// consentInsideRoundedCardAt é a versão parametrizada (largura/altura/raio
// efetivos, já escalados pelo DPI).
func consentInsideRoundedCardAt(x, y, width, height, cornerRadius int) bool {
	if x < 0 || y < 0 || x >= width || y >= height {
		return false
	}
	r := cornerRadius
	if r <= 0 {
		return true
	}
	left, top := x < r, y < r
	right, bottom := x >= width-r, y >= height-r
	if !left && !right && !top && !bottom {
		return true
	}
	// Centro do círculo do canto correspondente.
	cx, cy := x, y
	if left {
		cx = r
	} else if right {
		cx = width - r
	}
	if top {
		cy = r
	} else if bottom {
		cy = height - r
	}
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= r*r
}

// drawConsentBadge desenha o selo do cabeçalho: fundo levemente tingido pelo
// accent do Windows com o glifo de monitor do MESMO conjunto de ícones do
// cartão de notificação (Material Symbols, fonts.google.com/icons) —
// substitui o "monitor" desenhado à mão com retângulos.
func drawConsentBadge(hdc uintptr, rect winui.Rect, theme winui.Theme, scale float64) {
	sw := func(v int) int { return winui.ScalePx(v, scale) }
	winui.FillRoundRectBorderAA(hdc, rect, sw(12),
		theme.Card.Blend(theme.Accent, 16),
		theme.Card.Blend(theme.Accent, 40))
	notifyui.DrawGlyph(hdc, rect, notifyui.GlyphDesktop, theme.Accent)
}

// paint desenha o cartão inteiro.
func (s *consentNativeState) paint(screenDC uintptr) {
	width, height, radius, scale := s.metrics()
	sw := func(v int) int { return winui.ScalePx(v, scale) }

	memDC, bitmap, oldBitmap := winui.BufferedPaint(screenDC, width, height)
	hdc := memDC
	if hdc == 0 {
		hdc = screenDC
	} else {
		defer winui.FlushBufferedPaint(screenDC, memDC, bitmap, oldBitmap, width, height)
	}

	theme := s.theme
	logo, title, subtitle, divider, messageBox, messageText, countLabel, countValue,
		defaultAction, progressTrack, allow, deny, hint := s.layout()
	card := winui.Rect{W: width, H: height}

	// Ação padrão (fim do tempo) define a cor de destaque do bloco de contagem.
	actionColor := theme.Danger
	if s.timeoutApproves() {
		actionColor = theme.Success
	}

	// Cartão: fundo, faixa de destaque do Windows e borda. Com os cantos do
	// DWM o desenho é reto: o próprio compositor recorta/antialiasa o canto
	// (um canto desenhado com raio fixo deixaria uma cunha sem pintura entre o
	// desenho e o recorte do sistema).
	cardRadius := radius
	if s.systemCorners {
		cardRadius = 0
	}
	winui.FillRoundRectAA(hdc, card, cardRadius, theme.Card)
	// Faixa accent como pílula com margem: recortada nos cantos ela deixava
	// degraus serrilhados (mesmo ajuste feito no banner da sessão).
	accentMargin := sw(10)
	winui.FillRoundRectAA(hdc,
		winui.Rect{X: sw(3), Y: accentMargin, W: sw(4), H: height - 2*accentMargin},
		sw(2), theme.Accent)
	winui.StrokeRoundRectAA(hdc, card, cardRadius, sw(1), theme.Border)

	// Selo do acesso remoto (desenhado no tema; independe de asset externo).
	drawConsentBadge(hdc, logo, theme, scale)

	winui.DrawTextSingle(hdc, s.titleText(), title, s.fonts.title, theme.Text, winui.AlignLeft)
	winui.DrawTextSingle(hdc, "Acesso remoto", subtitle, s.fonts.subtitle, theme.TextMuted, winui.AlignLeft)
	winui.FillRect(hdc, divider, theme.Border)

	// Caixa da mensagem (mesma hierarquia do diálogo Fluent do PSADT).
	winui.FillRoundRectBorderAA(hdc, messageBox, sw(8), theme.Surface, theme.Border)
	winui.DrawTextWrappedEllipsis(hdc, s.messageText(), messageText, s.fonts.body, theme.Text, winui.AlignLeft)

	// Contagem regressiva + ação padrão.
	winui.DrawTextSingle(hdc, "Tempo para responder", countLabel, s.fonts.countLabel, theme.TextMuted, winui.AlignLeft)
	winui.DrawTextSingle(hdc, consentCountdownClock(s.remaining()), countValue, s.fonts.countValue, actionColor, winui.AlignLeft)
	winui.DrawTextWrapped(hdc, consentDefaultActionText(s.req.TimeoutAction), defaultAction,
		s.fonts.defaultAction, actionColor, winui.AlignLeft)

	// Barra de progresso do tempo restante.
	winui.FillRoundRectAA(hdc, progressTrack, sw(4), theme.Border)
	progress := progressTrack
	progress.W = int(float64(progressTrack.W) * s.progressRatio())
	if minWidth := sw(8); progress.W < minWidth {
		progress.W = minWidth
	}
	if progress.W > progressTrack.W {
		progress.W = progressTrack.W
	}
	winui.FillRoundRectAA(hdc, progress, sw(4), actionColor)

	// Dica de teclado (a ação padrão também é o Enter).
	winui.DrawTextSingle(hdc, "Enter confirma a ação padrão · Esc recusa", hint, s.fonts.hint, theme.TextMuted, winui.AlignLeft)

	// Botões (raio e anel de foco acompanham o DPI).
	buttonRadius := sw(9)
	allowStyle := winui.PillButton(
		theme.Success,
		theme.Success.Lighten(12),
		theme.Success.Darken(12),
		theme.SuccessText,
		buttonRadius,
	)
	denyStyle := winui.OutlineButton(theme, buttonRadius)
	allowStyle.FocusGap, allowStyle.FocusWidth = sw(4), sw(2)
	denyStyle.FocusGap, denyStyle.FocusWidth = sw(4), sw(2)
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
	// Canto inferior direito da área útil (acima da barra de tarefas), como os
	// toasts do Windows — e não no centro da tela, que cobre o trabalho do
	// usuário no momento do pedido de acesso remoto. A posição inicial usa o
	// tamanho de projeto; depois do DPI real o cartão é redimensionado.
	x, y := winui.BottomRightOnActiveWorkArea(consentWidth, consentHeight, consentScreenMargin)
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

	// DPI efetivo do monitor: com PerMonitorV2 o Windows não escala a janela
	// sozinho. O prompt acompanha a escala, como o banner da sessão.
	scale := winui.ScaleForDPI(winui.DpiForWindow(hwnd))
	width := winui.ScalePx(consentWidth, scale)
	baseHeight := winui.ScalePx(consentHeight, scale)
	radius := winui.ScalePx(consentRadius, scale)

	// Altura dinâmica da caixa de mensagem: o servidor permite mensagens longas
	// (até 500 caracteres) e a caixa fixa cortava o texto silenciosamente.
	bodyFont := winui.CreateFont(winui.ScalePx(15, scale), 400)
	_, _, _, _, _, messageTextRect, _, _, _, _, _, _, _ := consentLayoutFor(scale, 0)
	extraHeight := 0
	if needed := winui.MeasureWrappedText(bodyFont, state.messageText(), messageTextRect.W); needed > messageTextRect.H {
		extraHeight = needed - messageTextRect.H
		if maxExtra := winui.ScalePx(consentMessageMaxExtraPx, scale); extraHeight > maxExtra {
			extraHeight = maxExtra
		}
	}
	height := baseHeight + extraHeight

	state.scale = scale
	state.width = width
	state.height = height
	state.radius = radius
	state.extraHeight = extraHeight
	state.dwmCornerRadius = winui.ScalePx(consentSystemCornerRadius, scale)

	// Ancora no canto inferior direito com o tamanho final (texto longo cresce
	// para cima, sem sair da área útil).
	x, y = winui.BottomRightOnActiveWorkArea(width, height, winui.ScalePx(consentScreenMargin, scale))
	winui.MoveResizeWindow(hwnd, x, y, width, height)

	// Cantos: no Windows 11 o DWM arredonda com antialiasing (contorno suave);
	// sem suporte, cai na região binária (serrilhada) como antes.
	state.systemCorners = winui.SetSystemRoundedCorners(hwnd)
	if !state.systemCorners {
		winui.SetRoundedRegion(hwnd, width, height, radius)
	}
	state.theme = consentThemeLoader()
	state.fonts = consentFonts{
		title:         winui.CreateFont(winui.ScalePx(22, scale), 600),
		subtitle:      winui.CreateFont(winui.ScalePx(14, scale), 400),
		body:          bodyFont,
		countLabel:    winui.CreateFont(winui.ScalePx(12, scale), 400),
		countValue:    winui.CreateFont(winui.ScalePx(30, scale), 600),
		defaultAction: winui.CreateFont(winui.ScalePx(13, scale), 600),
		button:        winui.CreateFont(winui.ScalePx(14, scale), 600),
		hint:          winui.CreateFont(winui.ScalePx(11, scale), 400),
	}

	// Só publica o estado depois da geometria/fontes prontas: a janela ainda
	// está oculta e o wndproc não pinta nada antes do ShowWindow. Isso também
	// elimina a corrida de testes/consumidores que localizam o HWND pelo mapa.
	consentStates.Store(hwnd, state)

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
