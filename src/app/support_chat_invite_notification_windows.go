//go:build windows

package app

import (
	"context"
	"fmt"
	"runtime"
	"sync"

	"golang.org/x/sys/windows"

	"discovery/app/core/notifyui"
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
	// decided guarda a escolha do usuário para publicá-la ao processo de UI
	// (ver resolve/publishDecision).
	decided *supportChatInviteNotificationResult
	// published garante que a decisão chega ao processo de UI uma única vez
	// (fechamento normal x prazo de segurança — ver publishDecision).
	published bool
	// resultCh entrega a decisão ao processo de UI (com o erro do envio ao
	// servidor em Result.Error).
	resultCh chan supportChatInviteNotificationResult
	// windowThread é a thread que criou a janela e roda o loop de mensagens.
	// Decide quem pode encerrar o loop (PostQuitMessage só é válido nela).
	windowThread uint32
	// onDecided leva a escolha ao SERVIDOR. Roda em goroutine, em paralelo ao
	// fechamento do cartão (a tela não espera rede).
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
	// Reserva 36px (glifo do motivo 26px + folga) à direita do cabeçalho.
	headerWidth := supportChatNotifyWidth - headerLeft - supportChatNotifyPad - 36
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

// resolve registra a escolha e tira o cartão da tela NA HORA — para as duas
// respostas (aceitar e recusar).
//
// DECISÃO DE UX (relato do usuário, duas vezes): "clicou em Abrir chat, o chat
// abre e esse modal/notificação deve fechar igual quando clica Agora não". O
// cartão é uma PERGUNTA: respondida, sai de cena. Não existe mais o estado
// "Abrindo o chat de suporte…" no cartão — quem abre a janela do chat é o
// processo de UI, e o cartão já não tem o que dizer.
//
// O reporte ao servidor segue em paralelo (goroutine): a tela nunca espera rede.
func (s *supportChatNotifyState) resolve(hwnd uintptr, accepted bool) {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	s.done = true
	s.decided = &supportChatInviteNotificationResult{SessionID: s.invite.SessionID, Accepted: accepted}
	result := *s.decided
	onDecided := s.onDecided
	s.mu.Unlock()

	// 1) Envio ao servidor, sem bloquear a UI nem o fechamento.
	go func() {
		// Publicador EXCLUSIVO da decisão: com o erro REAL do POST, sempre
		// publicado (mesmo com pânico no caminho de rede). Antes, o WM_DESTROY
		// podia publicar antes e mascarar uma falha transitória: uma RECUSA com
		// POST falho virava "publicada sem erro" e o retry de cortesia nunca
		// rodava (console ficava em "Aguardando confirmação" até expirar).
		var err error
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panico ao reportar a decisao do convite: %v", r)
			}
			s.publishDecision(err)
		}()
		if onDecided != nil {
			err = onDecided(result)
		}
	}()

	// 2) Fecha o cartão imediatamente (mesmo comportamento de "Agora não").
	//    Este resolve roda dentro do WndProc, ou seja, na thread da janela.
	s.destroyCard(hwnd)
}

// destroyCard tira o cartão da tela de forma DETERMINÍSTICA.
//
// Em Windows, DestroyWindow pode ser chamado de qualquer thread e, quando
// destruiu a janela, já envia WM_QUIT à thread dona (o loop de mensagens
// termina). PostQuitMessage só é chamado pela THREAD DA JANELA: chamá-lo de
// outra goroutine parava o loop com a janela ainda viva — a janela ficava órfã
// na tela (o cartão travava depois da resposta).
func (s *supportChatNotifyState) destroyCard(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	if winui.DestroyWindow(hwnd) != 0 {
		return
	}
	// DestroyWindow recusou (a janela já não existe): encerra o loop aqui apenas
	// se ESTA goroutine é a thread dona da janela.
	if s.ownsWindowThread() {
		winui.PostQuitMessage(0)
	}
}

// destroyCardOnWindowThread pede à thread da janela que feche o cartão (único
// caminho seguro quando a chamada vem de outra goroutine).
func (s *supportChatNotifyState) destroyCardOnWindowThread(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	if s.ownsWindowThread() {
		s.destroyCard(hwnd)
		return
	}
	winui.PostMessage(hwnd, winui.MsgClose, 0, 0)
}

// ownsWindowThread informa se a goroutine atual é a thread dona da janela.
func (s *supportChatNotifyState) ownsWindowThread() bool {
	s.mu.Lock()
	owner := s.windowThread
	s.mu.Unlock()
	if owner == 0 {
		return false
	}
	return winui.CurrentThreadID() == owner
}

// publishDecision entrega a decisão ao processo de UI (uma única vez).
func (s *supportChatNotifyState) publishDecision(err error) {
	s.mu.Lock()
	if s.published || s.decided == nil {
		s.mu.Unlock()
		return
	}
	s.published = true
	result := *s.decided
	s.mu.Unlock()

	select {
	case s.resultCh <- supportChatInviteNotificationResult{
		SessionID: result.SessionID,
		Accepted:  result.Accepted,
		Error:     err,
	}:
	default:
	}
	close(s.resultCh)
}

// abort fecha o cartão sem decisão (cancelado por outro caminho: convite
// substituto, expirado no servidor, chat encerrado).
//
// Com decisão já tomada o fechamento é no-op — a escolha do usuário já foi
// publicada (e o envio ao servidor segue em goroutine).
func (s *supportChatNotifyState) abort(hwnd uintptr) {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	s.done = true
	s.mu.Unlock()

	close(s.resultCh)
	if hwnd != 0 {
		// Thread da janela (WM_*) ou mensagem para ela (cancelamento de outra
		// goroutine): em ambos os casos o cartão sai da tela.
		s.destroyCardOnWindowThread(hwnd)
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
		// SEM publicação aqui. Com decisão, o publicador exclusivo é o goroutine
		// do resolve (com o erro real do POST — ver resolve). Publicar aqui era
		// a corrida que mascarava falha de rede. Sem decisão, publishDecision
		// com decided==nil era no-op (abort/cancel já fecham o canal).
		winui.PostQuitMessage(0)
		return 0
	}
	return winui.DefWindowProc(hwnd, msg, wparam, lparam)
}

// drawSupportChatBadge desenha o selo: fundo tingido pelo accent com o glifo
// de conversa do MESMO conjunto de ícones do cartão de notificação (Material
// Symbols, fonts.google.com/icons) — substitui o balão desenhado à mão.
func drawSupportChatBadge(hdc uintptr, rect winui.Rect, theme winui.Theme, scale float64) {
	sw := func(v int) int { return winui.ScalePx(v, scale) }
	winui.FillRoundRectBorderAA(hdc, rect, sw(12),
		theme.Card.Blend(theme.Accent, 16),
		theme.Card.Blend(theme.Accent, 40))
	// Mesmo padrão do cartão de notificação: o logo do agente (o "D") no selo.
	notifyui.DrawAgentLogo(hdc, rect)
}

// paint desenha o cartão inteiro (sem contador/progresso/ação padrão).
func (s *supportChatNotifyState) paint(screenDC uintptr) {
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

	// Glifo do MOTIVO (chat) à direita do cabeçalho — mesmo padrão do cartão
	// de notificação (logo do agente à esquerda, motivo à direita).
	kindSize := sw(26)
	kindIcon := winui.Rect{
		X: sw(supportChatNotifyWidth) - sw(supportChatNotifyPad) - kindSize,
		Y: sw(20) + (sw(40)-kindSize)/2,
		W: kindSize, H: kindSize,
	}
	notifyui.DrawGlyph(hdc, kindIcon, notifyui.GlyphChat, theme.Accent)

	winui.DrawTextSingle(hdc, supportChatInviteNotificationTitle(), title, s.fonts.title, theme.Text, winui.AlignLeft)
	winui.DrawTextSingle(hdc, supportChatInviteNotificationSubtitle(), subtitle, s.fonts.subtitle, theme.TextMuted, winui.AlignLeft)
	winui.FillRect(hdc, divider, theme.Border)

	winui.FillRoundRectBorderAA(hdc, messageBox, sw(8), theme.Surface, theme.Border)
	winui.DrawTextWrappedEllipsis(hdc, supportChatInviteNotificationMessage(s.invite), messageText, s.fonts.body, theme.Text, winui.AlignLeft)

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
	winui.PaintButton(hdc, open, supportChatInviteNotificationAllow(), s.fonts.button, openStyle,
		s.hover == supportChatNotifyOpen, s.pressed == supportChatNotifyOpen,
		s.focus == supportChatNotifyOpen, theme.Accent)
	winui.PaintButton(hdc, dismiss, supportChatInviteNotificationDeny(), s.fonts.button, dismissStyle,
		s.hover == supportChatNotifyDismiss, s.pressed == supportChatNotifyDismiss,
		s.focus == supportChatNotifyDismiss, theme.Accent)
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

	state.mu.Lock()
	state.windowThread = winui.CurrentThreadID()
	state.mu.Unlock()

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
		// O loop terminou sem WM_DESTROY (WM_QUIT inesperado): a janela não pode
		// ficar órfã na tela. Aqui já estamos na thread da janela.
		state.abort(hwnd)
	}
	// Se havia decisão tomada, publishDecision já foi chamado (resolve ou o
	// WM_DESTROY): o canal foi fechado e o processo de UI foi avisado.
}
