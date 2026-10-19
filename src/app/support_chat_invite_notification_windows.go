//go:build windows

package app

import (
	"context"
	"runtime"
	"sync"
	"time"

	"golang.org/x/sys/windows"

	"discovery/app/core/winui"
	"discovery/app/supportchat"
)

// ── Cartão nativo do convite de chat do suporte ─────────────────────────────
//
// Mesmo vocabulário visual do prompt de autorização de acesso remoto
// (remote_consent_native_windows.go): cartão topmost no canto inferior direito,
// tema claro/escuro + accent do Windows, selo, título/subtítulo, mensagem em
// caixa própria e botões Fluent. Duas diferenças deliberadas:
//
//   - SEM contagem regressiva / barra de progresso / ação padrão: não existe
//     decisão automática nem botão pré-focado (o usuário precisa escolher);
//   - o cartão só PEDE para abrir o chat; a janela do chat é criada depois do
//     aceite, no processo de UI.

const (
	supportChatNotifyClassName = "DiscoveryRmmSupportChatNotification"
	supportChatNotifyWidth     = 520
	supportChatNotifyHeight    = 248
	supportChatNotifyRadius    = 16
	supportChatNotifyPad       = 24
	// Mesmo recuo do prompt de consentimento: nasce acima da barra de tarefas.
	supportChatNotifyScreenMargin = 24
	supportChatNotifySystemCorner = 8
	// supportChatNotifyWMAppCancel fecha o cartão de outra goroutine.
	supportChatNotifyWMAppCancel = 0x8000 + 43
	// supportChatNotifyMessageMaxExtraPx é o quanto a caixa de mensagem pode
	// crescer (96 DPI) para caber o nome do atendente + chamado.
	supportChatNotifyMessageMaxExtraPx = 140
)

// supportChatNotifyButton identifica os botões do cartão.
type supportChatNotifyButton int

const (
	supportChatNotifyNone supportChatNotifyButton = iota
	supportChatNotifyOpen
	supportChatNotifyDismiss
)

// supportChatNotifyFonts agrupa as fontes (liberadas no WM_DESTROY).
type supportChatNotifyFonts struct {
	title    uintptr
	subtitle uintptr
	body     uintptr
	button   uintptr
	hint     uintptr
}

func (f *supportChatNotifyFonts) release() {
	winui.DeleteGDIObject(f.title)
	winui.DeleteGDIObject(f.subtitle)
	winui.DeleteGDIObject(f.body)
	winui.DeleteGDIObject(f.button)
	winui.DeleteGDIObject(f.hint)
}

// supportChatNotifyState é o estado do cartão. Os campos de desenho só são
// tocados na thread da janela; a decisão é protegida por mutex.
type supportChatNotifyState struct {
	mu     sync.Mutex
	invite supportchat.Invite
	// done marca a decisão já tomada (clicou, Esc, X ou cancelamento).
	done bool
	// responding é true enquanto a decisão está sendo levada ao servidor.
	responding bool
	// decided guarda a escolha até o servidor responder: o cartão NÃO é
	// destruído antes disso (ver resolve).
	decided *supportChatInviteNotificationResult
	// resultCh entrega a decisão ao processo de UI depois que o cartão saiu da
	// tela (com o erro do envio ao servidor em Result.Error).
	resultCh chan supportChatInviteNotificationResult
	// onDecided leva a escolha ao SERVIDOR. Roda em goroutine enquanto o cartão
	// ainda está na tela, mostrando "avisando o suporte…".
	//
	// Por que reportar DENTRO do cartão: antes a janela era destruída e a
	// decisão ia para um canal lido em goroutine — entre o clique e o POST
	// restava uma janela de até 20 s (timeout) em que o convite continuava
	// "aguardando confirmação" no console e o cartão já não existia mais. Se o
	// processo/agent morresse nesse intervalo, o atendente ficava com o convite
	// pendente para sempre (a recusa "não chegava" ao navegador).
	onDecided func(result supportChatInviteNotificationResult) error

	theme   winui.Theme
	fonts   supportChatNotifyFonts
	hover   supportChatNotifyButton
	pressed supportChatNotifyButton
	focus   supportChatNotifyButton
	// focus começa em "none": sem ação padrão, Enter não decide nada.
	systemCorners bool

	scale           float64
	width           int
	height          int
	radius          int
	extraHeight     int
	dwmCornerRadius int
}

// metrics devolve a geometria efetiva (fallback: layout de projeto 96 DPI).
func (s *supportChatNotifyState) metrics() (width, height, radius int, scale float64) {
	scale = s.scale
	if scale <= 0 {
		scale = 1
	}
	width, height, radius = s.width, s.height, s.radius
	if width <= 0 {
		width = supportChatNotifyWidth
	}
	if height <= 0 {
		height = supportChatNotifyHeight + s.extraHeight
	}
	if radius <= 0 {
		radius = supportChatNotifyRadius
	}
	return width, height, radius, scale
}

// supportChatNotifyLayout devolve os retângulos do cartão no layout base
// (96 DPI), usado pelos testes e como fallback de pintura.
func supportChatNotifyLayout() (logo, title, subtitle, divider, messageBox, messageText,
	open, dismiss, hint winui.Rect) {
	return supportChatNotifyLayoutFor(1, 0)
}

// supportChatNotifyLayoutFor escala o layout pelo DPI e cresce a caixa de
// mensagem em extraHeight px físicos (empurrando os botões para baixo).
func supportChatNotifyLayoutFor(scale float64, extraHeight int) (logo, title, subtitle, divider, messageBox, messageText,
	open, dismiss, hint winui.Rect) {
	logo = winui.Rect{X: supportChatNotifyPad, Y: 20, W: 40, H: 40}
	headerLeft := logo.Right() + 14
	headerWidth := supportChatNotifyWidth - headerLeft - supportChatNotifyPad
	title = winui.Rect{X: headerLeft, Y: 20, W: headerWidth, H: 28}
	subtitle = winui.Rect{X: headerLeft, Y: 52, W: headerWidth, H: 18}
	divider = winui.Rect{X: supportChatNotifyPad, Y: 86, W: supportChatNotifyWidth - 2*supportChatNotifyPad, H: 1}
	messageBox = winui.Rect{X: supportChatNotifyPad, Y: 98, W: supportChatNotifyWidth - 2*supportChatNotifyPad, H: 84}
	dismiss = winui.Rect{X: supportChatNotifyWidth - supportChatNotifyPad - 128, Y: 196, W: 128, H: 38}
	open = winui.Rect{X: dismiss.X - 10 - 128, Y: 196, W: 128, H: 38}
	hint = winui.Rect{X: supportChatNotifyPad, Y: 206, W: open.X - supportChatNotifyPad - 12, H: 16}

	if scale <= 0 {
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
		messageBox, open, dismiss, hint = scaleRect(messageBox), scaleRect(open), scaleRect(dismiss), scaleRect(hint)
	}

	if extraHeight > 0 {
		messageBox.H += extraHeight
		open.Y += extraHeight
		dismiss.Y += extraHeight
		hint.Y += extraHeight
	}
	messageText = messageBox.Inset(winui.ScalePx(14, scale), winui.ScalePx(10, scale))
	return
}

// layout devolve o layout na escala/altura efetivas.
func (s *supportChatNotifyState) layout() (logo, title, subtitle, divider, messageBox, messageText,
	open, dismiss, hint winui.Rect) {
	scale := s.scale
	if scale <= 0 {
		scale = 1
	}
	return supportChatNotifyLayoutFor(scale, s.extraHeight)
}

// insideRoundedCard informa se o ponto está dentro do cartão visível (cantos
// arredondados pelo DWM continuam retangulares para o mouse).
func (s *supportChatNotifyState) insideRoundedCard(x, y int) bool {
	width, height, _, _ := s.metrics()
	corner := s.dwmCornerRadius
	if corner <= 0 {
		corner = supportChatNotifySystemCorner
	}
	return consentInsideRoundedCardAt(x, y, width, height, corner)
}

// buttonAt devolve o botão sob o ponto informado.
func (s *supportChatNotifyState) buttonAt(x, y int) supportChatNotifyButton {
	_, _, _, _, _, _, open, dismiss, _ := s.layout()
	if open.Contains(x, y) {
		return supportChatNotifyOpen
	}
	if dismiss.Contains(x, y) {
		return supportChatNotifyDismiss
	}
	return supportChatNotifyNone
}

// resolve registra a escolha e mantém o cartão na tela (com o botão escolhido
// em "carregando") até o servidor responder.
//
// ORDEM DELIBERADA: reportar ANTES de fechar. A recusa precisa chegar ao
// servidor mesmo que o desktop/runtime esteja lento, e o usuário precisa ver
// que o clique foi registrado. Quando o resultado volta (ou o prazo estoura),
// finish é quem destrói a janela e aplica a decisão no processo de UI.
func (s *supportChatNotifyState) resolve(hwnd uintptr, accepted bool) {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	s.done = true
	s.responding = true
	s.decided = &supportChatInviteNotificationResult{SessionID: s.invite.SessionID, Accepted: accepted}
	result := *s.decided
	onDecided := s.onDecided
	s.mu.Unlock()

	winui.Invalidate(hwnd)

	done := make(chan error, 1)
	go func() {
		var err error
		if onDecided != nil {
			err = onDecided(result)
		}
		done <- err
	}()

	go func() {
		var err error
		select {
		case err = <-done:
		case <-time.After(decisionGrace):
			// O envio não respondeu no prazo. O cartão não pode ficar preso:
			// fecha informando o erro (o processo de UI loga e segue).
			err = errSupportChatInviteReportTimeout
		}
		s.finish(hwnd, err)
	}()
}

// isResponding informa se a decisão está em voo para o servidor (o cartão não
// aceita mais cliques/teclas e mostra o estado de espera).
func (s *supportChatNotifyState) isResponding() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.responding
}

// decisionGrace é o prazo em que o cartão espera o servidor antes de sair da
// tela sozinho. Não pode ser maior que o timeout do POST
// (supportChatInviteReportWait): um callback travado nunca pode deixar o cartão
// preso na tela para sempre.
const decisionGrace = supportChatInviteReportWait + 5*time.Second

// finish fecha o cartão (uma única vez) quando a decisão já chegou ao servidor.
func (s *supportChatNotifyState) finish(hwnd uintptr, err error) {
	s.mu.Lock()
	result := s.decided
	s.mu.Unlock()

	// Fecha a janela ANTES de publicar a decisão: o tratamento do aceite abre a
	// janela do chat e o cartão topmost não pode ficar por cima dela nem por
	// alguns milissegundos.
	if hwnd != 0 {
		winui.DestroyWindow(hwnd)
	}
	if result == nil {
		close(s.resultCh)
		return
	}
	s.resultCh <- supportChatInviteNotificationResult{SessionID: result.SessionID, Accepted: result.Accepted, Error: err}
	close(s.resultCh)
}

// abort fecha o cartão sem decisão (cancelado por outro caminho).
//
// Se o usuário JÁ respondeu (responding), o fechamento NÃO cancela a decisão:
// ela continua indo ao servidor e, quando termina, finish fecha o cartão. Antes
// o cancelamento genérico podia destruir o cartão e fechar o canal no meio do
// envio, e a recusa se perdia.
func (s *supportChatNotifyState) abort(hwnd uintptr) {
	s.mu.Lock()
	if s.done {
		responding := s.responding
		s.mu.Unlock()
		if !responding && hwnd != 0 {
			winui.DestroyWindow(hwnd)
		}
		return
	}
	s.done = true
	s.mu.Unlock()

	close(s.resultCh)
	if hwnd != 0 {
		winui.DestroyWindow(hwnd)
	}
}

// activate dispara a ação de um botão.
func (s *supportChatNotifyState) activate(hwnd uintptr, button supportChatNotifyButton) {
	switch button {
	case supportChatNotifyOpen:
		s.resolve(hwnd, true)
	case supportChatNotifyDismiss:
		s.resolve(hwnd, false)
	}
}

// handleKey trata teclado. Não há ação padrão: Enter/Espaço só confirmam um
// botão que o usuário tenha focado com Tab/setas; Esc recusa (como no
// consentimento de acesso remoto).
func (s *supportChatNotifyState) handleKey(hwnd, key uintptr) uintptr {
	switch key {
	case winui.VkTab, winui.VkLeft, winui.VkRight:
		switch s.focus {
		case supportChatNotifyNone:
			s.focus = supportChatNotifyOpen
		case supportChatNotifyOpen:
			s.focus = supportChatNotifyDismiss
		default:
			s.focus = supportChatNotifyOpen
		}
		winui.Invalidate(hwnd)
		return 0
	case winui.VkReturn, winui.VkSpace:
		if s.focus == supportChatNotifyNone {
			return 0
		}
		s.activate(hwnd, s.focus)
		return 0
	case winui.VkEscape:
		s.activate(hwnd, supportChatNotifyDismiss)
		return 0
	}
	return 0
}

// supportChatNotifyWndProc é o procedimento da janela do cartão.
func supportChatNotifyWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	raw, ok := supportChatNotifyStates.Load(hwnd)
	if !ok {
		return winui.DefWindowProc(hwnd, msg, wparam, lparam)
	}
	state, _ := raw.(*supportChatNotifyState)

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
	case winui.MsgMouseMove:
		hover := state.buttonAt(consentLParamX(lparam), consentLParamY(lparam))
		if hover != state.hover {
			state.hover = hover
			winui.Invalidate(hwnd)
		}
		winui.TrackMouseLeave(hwnd)
		return 0
	case winui.MsgMouseLeave:
		if state.hover != supportChatNotifyNone {
			state.hover = supportChatNotifyNone
			winui.Invalidate(hwnd)
		}
		return 0
	case winui.MsgLButtonDown:
		if state.isResponding() {
			return 0
		}
		button := state.buttonAt(consentLParamX(lparam), consentLParamY(lparam))
		if button != supportChatNotifyNone {
			state.pressed = button
			state.focus = button
			winui.SetCapture(hwnd)
			winui.Invalidate(hwnd)
		}
		return 0
	case winui.MsgLButtonUp:
		pressed := state.pressed
		state.pressed = supportChatNotifyNone
		winui.ReleaseCapture()
		winui.Invalidate(hwnd)
		if pressed != supportChatNotifyNone &&
			state.buttonAt(consentLParamX(lparam), consentLParamY(lparam)) == pressed {
			state.activate(hwnd, pressed)
		}
		return 0
	case winui.MsgSetCursor:
		if state.buttonAt(consentLParamX(lparam), consentLParamY(lparam)) != supportChatNotifyNone {
			winui.SetCursorHand()
		} else {
			winui.SetCursorArrow()
		}
		return 1
	case winui.MsgNCHitTest:
		if state.systemCorners {
			x, y := winui.ScreenToClient(hwnd, consentLParamX(lparam), consentLParamY(lparam))
			if !state.insideRoundedCard(x, y) {
				return winui.HtTransparent
			}
		}
		return winui.DefWindowProc(hwnd, msg, wparam, lparam)
	case winui.MsgKeyDown:
		if state.isResponding() {
			return 0
		}
		return state.handleKey(hwnd, wparam)
	case supportChatNotifyWMAppCancel:
		state.abort(hwnd)
		return 0
	case winui.MsgClose:
		// X equivale a "Agora não" (mesma semântica de fail-closed do prompt
		// de acesso remoto: fechar sem escolher não deixa o chat pendente).
		state.activate(hwnd, supportChatNotifyDismiss)
		return 0
	case winui.MsgDestroy:
		state.fonts.release()
		supportChatNotifyStates.Delete(hwnd)
		winui.PostQuitMessage(0)
		return 0
	}
	return winui.DefWindowProc(hwnd, msg, wparam, lparam)
}

// drawSupportChatBadge desenha o selo: balão de conversa no accent do Windows.
func drawSupportChatBadge(hdc uintptr, rect winui.Rect, theme winui.Theme, scale float64) {
	sw := func(v int) int { return winui.ScalePx(v, scale) }
	winui.FillRoundRectBorderAA(hdc, rect, sw(12),
		theme.Card.Blend(theme.Accent, 16),
		theme.Card.Blend(theme.Accent, 40))

	bubble := winui.Rect{X: rect.X + sw(9), Y: rect.Y + sw(9), W: rect.W - sw(18), H: sw(17)}
	winui.FillRoundRectAA(hdc, bubble, sw(6), theme.Accent)
	// Rabinho do balão (sem polígono no winui): pequeno retângulo no canto.
	winui.FillRoundRectAA(hdc, winui.Rect{
		X: bubble.X + sw(7), Y: bubble.Bottom() - sw(3), W: sw(7), H: sw(6),
	}, sw(2), theme.Accent)
	// Três pontinhos com a cor do cartão.
	dotY := bubble.Y + bubble.H/2 - sw(1)
	dotX := bubble.X + bubble.W/2 - sw(9)
	for i := 0; i < 3; i++ {
		winui.FillRoundRectAA(hdc, winui.Rect{
			X: dotX + i*sw(7), Y: dotY, W: sw(3), H: sw(3),
		}, sw(1), theme.Card.Blend(theme.Accent, 16))
	}
}

// paint desenha o cartão inteiro (sem contador/progresso/ação padrão).
func (s *supportChatNotifyState) paint(screenDC uintptr) {
	width, height, radius, scale := s.metrics()
	sw := func(v int) int { return winui.ScalePx(v, scale) }

	// Decisão já tomada e indo para o servidor: os botões não respondem mais e o
	// texto explica a espera (ver resolve). Sem este retorno de imagem o cartão
	// parecia "morto" nos segundos do POST.
	s.mu.Lock()
	responding := s.responding
	decided := s.decided
	s.mu.Unlock()

	memDC, bitmap, oldBitmap := winui.BufferedPaint(screenDC, width, height)
	hdc := memDC
	if hdc == 0 {
		hdc = screenDC
	} else {
		defer winui.FlushBufferedPaint(screenDC, memDC, bitmap, oldBitmap, width, height)
	}

	theme := s.theme
	logo, title, subtitle, divider, messageBox, messageText, open, dismiss, hint := s.layout()
	card := winui.Rect{W: width, H: height}

	cardRadius := radius
	if s.systemCorners {
		cardRadius = 0
	}
	winui.FillRoundRectAA(hdc, card, cardRadius, theme.Card)
	accentMargin := sw(10)
	winui.FillRoundRectAA(hdc,
		winui.Rect{X: sw(3), Y: accentMargin, W: sw(4), H: height - 2*accentMargin},
		sw(2), theme.Accent)
	winui.StrokeRoundRectAA(hdc, card, cardRadius, sw(1), theme.Border)

	drawSupportChatBadge(hdc, logo, theme, scale)

	winui.DrawTextSingle(hdc, supportChatInviteNotificationTitle(), title, s.fonts.title, theme.Text, winui.AlignLeft)
	winui.DrawTextSingle(hdc, supportChatInviteNotificationSubtitle(), subtitle, s.fonts.subtitle, theme.TextMuted, winui.AlignLeft)
	winui.FillRect(hdc, divider, theme.Border)

	winui.FillRoundRectBorderAA(hdc, messageBox, sw(8), theme.Surface, theme.Border)
	message := supportChatInviteNotificationMessage(s.invite)
	if responding {
		message = supportChatInviteNotificationRespondingMessage(decided != nil && decided.Accepted)
	}
	winui.DrawTextWrappedEllipsis(hdc, message, messageText, s.fonts.body, theme.Text, winui.AlignLeft)

	winui.DrawTextSingle(hdc, supportChatInviteNotificationHint(), hint, s.fonts.hint, theme.TextMuted, winui.AlignLeft)

	buttonRadius := sw(9)
	openStyle := winui.PillButton(
		theme.Accent,
		theme.Accent.Lighten(12),
		theme.Accent.Darken(12),
		theme.AccentText,
		buttonRadius,
	)
	dismissStyle := winui.OutlineButton(theme, buttonRadius)
	openStyle.FocusGap, openStyle.FocusWidth = sw(4), sw(2)
	dismissStyle.FocusGap, dismissStyle.FocusWidth = sw(4), sw(2)
	hover, pressed, focus := s.hover, s.pressed, s.focus
	if responding {
		// Cartão em espera: nenhum botão em destaque (o clique já foi aceito).
		hover, pressed, focus = supportChatNotifyNone, supportChatNotifyNone, supportChatNotifyNone
	}
	winui.PaintButton(hdc, open, supportChatInviteNotificationAllow(), s.fonts.button, openStyle,
		hover == supportChatNotifyOpen, pressed == supportChatNotifyOpen,
		focus == supportChatNotifyOpen, theme.Accent)
	winui.PaintButton(hdc, dismiss, supportChatInviteNotificationDeny(), s.fonts.button, dismissStyle,
		hover == supportChatNotifyDismiss, pressed == supportChatNotifyDismiss,
		focus == supportChatNotifyDismiss, theme.Accent)
}

// supportChatNotifyThemeLoader lê o tema do usuário (var para testes).
var supportChatNotifyThemeLoader = winui.DetectTheme

var (
	supportChatNotifyClassOnce sync.Once
	supportChatNotifyClassErr  error
	supportChatNotifyStates    sync.Map // hwnd -> *supportChatNotifyState
)

func ensureSupportChatNotifyClass() error {
	supportChatNotifyClassOnce.Do(func() {
		supportChatNotifyClassErr = winui.RegisterWindowClass(
			supportChatNotifyClassName,
			windows.NewCallback(supportChatNotifyWndProc),
			winui.ClassStyleDropShadow,
		)
	})
	return supportChatNotifyClassErr
}

// startSupportChatInviteNotification cria o cartão e devolve o canal de decisão
// + um cancelador (fecha o cartão sem decisão). ok=false quando a janela nativa
// não pode ser usada; o chamador cai no fluxo antigo.
//
// onDecided é o callback que leva a escolha ao SERVIDOR. Ele roda ANTES de o
// cartão sair da tela (ver resolve) e recebe a decisão com o erro do envio.
func startSupportChatInviteNotification(ctx context.Context, invite supportchat.Invite, onDecided func(supportChatInviteNotificationResult) error) (<-chan supportChatInviteNotificationResult, func(), bool) {
	if err := ensureSupportChatNotifyClass(); err != nil {
		return nil, nil, false
	}
	if ctx.Err() != nil {
		return nil, nil, false
	}

	resultCh := make(chan supportChatInviteNotificationResult, 1)
	readyCh := make(chan bool, 1)
	cancelCh := make(chan struct{})
	var cancelOnce sync.Once
	cancel := func() {
		cancelOnce.Do(func() { close(cancelCh) })
	}

	go runSupportChatInviteNotificationWindow(ctx, invite, onDecided, resultCh, readyCh, cancelCh)

	// Confirma a criação da janela antes de dizer que o cartão está disponível:
	// se o CreateWindow falhar, o chamador ainda cai no fluxo antigo (janela do
	// chat) em vez de ficar sem nenhuma superfície de aceite.
	select {
	case created := <-readyCh:
		if !created {
			cancel()
			return nil, nil, false
		}
		return resultCh, cancel, true
	case <-ctx.Done():
		cancel()
		return nil, nil, false
	}
}

// runSupportChatInviteNotificationWindow cria a janela, roda o loop de
// mensagens e publica a decisão (ou fecha o canal quando cancelada).
func runSupportChatInviteNotificationWindow(ctx context.Context, invite supportchat.Invite, onDecided func(supportChatInviteNotificationResult) error, resultCh chan supportChatInviteNotificationResult, readyCh chan<- bool, cancelCh <-chan struct{}) {
	state := &supportChatNotifyState{
		invite:    invite,
		resultCh:  resultCh,
		onDecided: onDecided,
		hover:     supportChatNotifyNone,
		pressed:   supportChatNotifyNone,
		focus:     supportChatNotifyNone, // sem ação padrão
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	x, y := winui.BottomRightOnActiveWorkArea(supportChatNotifyWidth, supportChatNotifyHeight, supportChatNotifyScreenMargin)
	hwnd, err := winui.CreateWindow(
		supportChatNotifyClassName,
		supportChatInviteNotificationTitle(),
		winui.ExStyleTopmost|winui.ExStyleToolWindow,
		winui.WinStylePopup|winui.WinStyleClipSiblings,
		x, y, supportChatNotifyWidth, supportChatNotifyHeight,
	)
	if err != nil {
		readyCh <- false
		close(resultCh)
		return
	}

	scale := winui.ScaleForDPI(winui.DpiForWindow(hwnd))

	// Em telas pequenas com DPI alto (ex.: 1024x768 a 200%) o cartão escalado
	// pode não caber na área útil: reduz a escala até caber, senão ele nasce
	// parcialmente fora da tela (o bottom-right só clampa a posição).
	area := winui.ActiveWorkArea()
	for i := 0; i < 3; i++ {
		width := winui.ScalePx(supportChatNotifyWidth, scale)
		baseHeight := winui.ScalePx(supportChatNotifyHeight, scale)
		margin := winui.ScalePx(supportChatNotifyScreenMargin, scale)
		maxWidth := area.W - 2*margin
		maxHeight := area.H - 2*margin
		if maxWidth <= 0 || maxHeight <= 0 {
			break
		}
		factor := 1.0
		if width > maxWidth {
			factor = float64(maxWidth) / float64(width)
		}
		if baseHeight > maxHeight {
			if f := float64(maxHeight) / float64(baseHeight); f < factor {
				factor = f
			}
		}
		if factor >= 1 {
			break
		}
		scale *= factor
	}

	width := winui.ScalePx(supportChatNotifyWidth, scale)
	baseHeight := winui.ScalePx(supportChatNotifyHeight, scale)
	radius := winui.ScalePx(supportChatNotifyRadius, scale)
	margin := winui.ScalePx(supportChatNotifyScreenMargin, scale)
	maxHeight := area.H - 2*margin

	bodyFont := winui.CreateFont(winui.ScalePx(15, scale), 400)
	_, _, _, _, _, messageTextRect, _, _, _ := supportChatNotifyLayoutFor(scale, 0)
	extraHeight := 0
	if needed := winui.MeasureWrappedText(bodyFont, supportChatInviteNotificationMessage(invite), messageTextRect.W); needed > messageTextRect.H {
		extraHeight = needed - messageTextRect.H
		if maxExtra := winui.ScalePx(supportChatNotifyMessageMaxExtraPx, scale); extraHeight > maxExtra {
			extraHeight = maxExtra
		}
		if room := maxHeight - baseHeight; room <= 0 {
			extraHeight = 0
		} else if extraHeight > room {
			extraHeight = room
		}
	}
	height := baseHeight + extraHeight

	state.scale = scale
	state.width = width
	state.height = height
	state.radius = radius
	state.extraHeight = extraHeight
	state.dwmCornerRadius = winui.ScalePx(supportChatNotifySystemCorner, scale)

	x, y = winui.BottomRightOnActiveWorkArea(width, height, margin)
	winui.MoveResizeWindow(hwnd, x, y, width, height)

	state.systemCorners = winui.SetSystemRoundedCorners(hwnd)
	if !state.systemCorners {
		winui.SetRoundedRegion(hwnd, width, height, radius)
	}
	state.theme = supportChatNotifyThemeLoader()
	state.fonts = supportChatNotifyFonts{
		title:    winui.CreateFont(winui.ScalePx(21, scale), 600),
		subtitle: winui.CreateFont(winui.ScalePx(14, scale), 400),
		body:     bodyFont,
		button:   winui.CreateFont(winui.ScalePx(14, scale), 600),
		hint:     winui.CreateFont(winui.ScalePx(11, scale), 400),
	}

	supportChatNotifyStates.Store(hwnd, state)

	winui.ShowWindow(hwnd, winui.SwShowNormal)
	winui.EnsureTopMost(hwnd)
	// NÃO rouba o foco: é uma notificação, não um modal. O cartão fica visível
	// acima das outras janelas; o usuário clica para responder. Isso evita que
	// um Esc destinado ao app em uso recuse o convite.
	winui.UpdateWindow(hwnd)
	readyCh <- true

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			winui.PostMessage(hwnd, supportChatNotifyWMAppCancel, 0, 0)
		case <-cancelCh:
			winui.PostMessage(hwnd, supportChatNotifyWMAppCancel, 0, 0)
		case <-done:
		}
	}()

	var msg winui.Msg
	for winui.NextMessage(&msg) > 0 {
		winui.TranslateMessage(&msg)
		winui.DispatchMessage(&msg)
	}

	state.mu.Lock()
	decided := state.done
	state.mu.Unlock()
	if !decided {
		// Inclui o hwnd: se o loop terminou sem WM_DESTROY (WM_QUIT inesperado),
		// a janela não pode ficar órfã na tela. Com decisão em voo, abort só
		// mantém a janela viva até finish (ver abort).
		state.abort(hwnd)
	}
}
