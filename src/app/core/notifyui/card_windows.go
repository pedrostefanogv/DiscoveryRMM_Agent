//go:build windows

package notifyui

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/sys/windows"

	"discovery/app/core/winui"
)

// ── Cartão nativo de notificação ──
//
// Mesma arquitetura do prompt de consentimento (app/remote_consent_native_*.go):
// janela popup topmost no canto inferior direito, pintada com o tema do Windows
// do usuário interativo, com botões e contagem regressiva opcional. A
// diferença é a generalização: 1..3 botões com resultado estruturado, contador
// opcional e desfechos clicado/timeout/fechado/cancelado.

const (
	cardClassName   = "DiscoveryRmmNotifyCard"
	cardWidth       = 560
	cardRadius      = 16
	cardPad         = 24
	cardTimerID     = 1
	cardTickMS      = 200
	cardButtonW     = 128
	cardButtonH     = 38
	cardButtonGap   = 10
	cardMaxButtons  = 3
	// cardProcessRowH/cardProcessIcon dimensionam as linhas da lista de
	// processos do Welcome (ícone 28px + nome/executável).
	cardProcessRowH  = 40
	cardProcessIcon  = 28
	// cardMessageMaxExtraPx limita o crescimento da caixa de mensagem (96 DPI).
	cardMessageMaxExtraPx = 160
	// cardWMAppCancel fecha o cartão de outra goroutine (cancelamento).
	cardWMAppCancel = 0x8000 + 43
)

// cardLayout devolve os retângulos do cartão em pixels físicos.
type cardLayout struct {
	badge, kindIcon, title, subtitle, divider, messageBox, messageText winui.Rect
	countLabel, countValue, progressTrack                    winui.Rect
	processBox                                               winui.Rect
	processRows                                              []winui.Rect
	buttons                                                  []winui.Rect
	hint                                                     winui.Rect
}

// cardLayoutFor monta o layout para a escala/extra informados. Puro
// (usado também pelos testes de pré-visualização).
func cardLayoutFor(scale float64, extraHeight int, hasCountdown bool, nButtons int, processRows int) cardLayout {
	if scale <= 0 {
		scale = 1
	}
	if nButtons < 1 {
		nButtons = 1
	}
	if nButtons > cardMaxButtons {
		nButtons = cardMaxButtons
	}
	sw := func(v int) int { return winui.ScalePx(v, scale) }

	l := cardLayout{}
	l.badge = winui.Rect{X: sw(cardPad), Y: sw(20), W: sw(40), H: sw(40)}
	// Ícone do MOTIVO (informação/aviso/erro/pergunta) alinhado à direita do
	// cabeçalho, na mesma linha do selo.
	kindSize := sw(26)
	l.kindIcon = winui.Rect{
		X: sw(cardWidth) - sw(cardPad) - kindSize,
		Y: sw(20) + (sw(40)-kindSize)/2,
		W: kindSize, H: kindSize,
	}
	headerLeft := l.badge.Right() + sw(14)
	headerWidth := l.kindIcon.X - sw(10) - headerLeft
	l.title = winui.Rect{X: headerLeft, Y: sw(20), W: headerWidth, H: sw(28)}
	l.subtitle = winui.Rect{X: headerLeft, Y: sw(52), W: headerWidth, H: sw(18)}
	l.divider = winui.Rect{X: sw(cardPad), Y: sw(86), W: sw(cardWidth) - 2*sw(cardPad), H: 1}
	l.messageBox = winui.Rect{X: sw(cardPad), Y: sw(98), W: sw(cardWidth) - 2*sw(cardPad), H: sw(84) + extraHeight}
	l.messageText = l.messageBox.Inset(sw(14), sw(10))

	contentY := l.messageBox.Bottom() + sw(10)
	if processRows > 0 {
		l.processBox = winui.Rect{
			X: sw(cardPad), Y: contentY,
			W: sw(cardWidth) - 2*sw(cardPad),
			H: sw(8) + processRows*sw(cardProcessRowH) + sw(6),
		}
		for i := 0; i < processRows; i++ {
			y := l.processBox.Y + sw(4) + i*sw(cardProcessRowH)
			l.processRows = append(l.processRows, winui.Rect{
				X: l.processBox.X + sw(12), Y: y,
				W: l.processBox.W - sw(24), H: sw(cardProcessRowH),
			})
		}
		contentY = l.processBox.Bottom() + sw(10)
	}
	if hasCountdown {
		l.countLabel = winui.Rect{X: sw(cardPad), Y: contentY, W: sw(280), H: sw(16)}
		l.countValue = winui.Rect{X: sw(cardPad), Y: l.countLabel.Bottom() + sw(4), W: sw(120), H: sw(30)}
		l.progressTrack = winui.Rect{
			X: sw(cardPad), Y: l.countValue.Bottom() + sw(8),
			W: sw(cardWidth) - 2*sw(cardPad), H: sw(7),
		}
		contentY = l.progressTrack.Bottom() + sw(18)
	}

	// Botões alinhados à direita, na ordem do slice (esquerda→direita): o
	// último é a ação principal.
	btnY := contentY
	for i := 0; i < nButtons; i++ {
		x := sw(cardWidth) - sw(cardPad) - sw(cardButtonW) - (nButtons-1-i)*(sw(cardButtonW)+sw(cardButtonGap))
		l.buttons = append(l.buttons, winui.Rect{X: x, Y: btnY, W: sw(cardButtonW), H: sw(cardButtonH)})
	}
	contentY = btnY + sw(cardButtonH) + sw(12)
	l.hint = winui.Rect{X: sw(cardPad), Y: contentY, W: sw(cardWidth) - 2*sw(cardPad), H: sw(16)}
	return l
}

// cardHeightFor devolve a altura total do cartão para o layout.
func cardHeightFor(l cardLayout, scale float64) int {
	return l.hint.Bottom() + winui.ScalePx(cardPad, scale)
}

// cardFonts agrupa as fontes do cartão (liberadas no WM_DESTROY).
type cardFonts struct {
	title, subtitle, body, countLabel, countValue, button, hint uintptr
	// processFallback é a fonte da letra do ícone fallback da lista.
	processFallback uintptr
}

func (f *cardFonts) release() {
	for _, h := range []uintptr{f.title, f.subtitle, f.body, f.countLabel, f.countValue, f.button, f.hint, f.processFallback} {
		if h != 0 {
			winui.DeleteGDIObject(h)
		}
	}
}

// cardState é o estado do cartão (desenho só na thread da janela).
type cardState struct {
	mu       sync.Mutex
	req      CardRequest
	decided  bool
	result   CardResult
	resultCh chan CardResult

	deadline time.Time
	total    time.Duration

	theme   winui.Theme
	fonts   cardFonts
	// processIcons guarda os HICONs da lista de processos (liberados no WM_DESTROY).
	processIcons []uintptr
	layout  cardLayout
	scale   float64
	hover   int // -1 = nenhum
	pressed int
	focus   int

	systemCorners  bool
	dwmCornerRadius int
}

// cardHeight computa a altura efetiva (mensagem longa cresce o cartão).
// Os retângulos do layout já estão em pixels físicos; só o padding inferior
// depende da escala.
func (s *cardState) cardHeight() int {
	return cardHeightFor(s.layout, s.scale)
}

// remaining devolve o tempo restante da contagem (0 quando acabou).
func (s *cardState) remaining() time.Duration {
	remaining := time.Until(s.deadline)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// progressRatio devolve 1.0 no início e 0.0 no fim da contagem.
func (s *cardState) progressRatio() float64 {
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

// finish registra o resultado e encerra a janela.
func (s *cardState) finish(hwnd uintptr, result CardResult) {
	s.mu.Lock()
	if s.decided {
		s.mu.Unlock()
		return
	}
	s.decided = true
	s.result = result
	s.mu.Unlock()

	select {
	case s.resultCh <- result:
	default:
	}
	if hwnd != 0 {
		winui.KillTimer(hwnd, cardTimerID)
		winui.DestroyWindow(hwnd)
	}
}

// buttonAt devolve o índice do botão sob o ponto (ou -1).
func (s *cardState) buttonAt(x, y int) int {
	for i, r := range s.layout.buttons {
		if r.Contains(x, y) {
			return i
		}
	}
	return -1
}

// activate dispara o botão focado/clicado.
func (s *cardState) activate(hwnd uintptr, idx int) {
	if idx < 0 || idx >= len(s.req.Buttons) {
		return
	}
	s.finish(hwnd, CardResult{ButtonID: s.req.Buttons[idx].ID, Outcome: OutcomeClicked})
}

// timeoutResult devolve o resultado do fim da contagem.
func (s *cardState) timeoutResult() CardResult {
	return CardResult{ButtonID: s.req.TimeoutButtonID, Outcome: OutcomeTimeout}
}

// closeResult devolve o resultado de Esc/X.
func (s *cardState) closeResult() CardResult {
	return CardResult{ButtonID: s.req.CloseButtonID, Outcome: OutcomeClosed}
}

// handleKey trata teclado: Tab/setas alternam o foco, Enter confirma e Esc fecha.
func (s *cardState) handleKey(hwnd, key uintptr) uintptr {
	n := len(s.req.Buttons)
	switch key {
	case winui.VkTab, winui.VkLeft, winui.VkRight:
		if s.focus < 0 {
			s.focus = s.defaultIndex()
		} else {
			s.focus = (s.focus + 1) % n
		}
		winui.Invalidate(hwnd)
		return 0
	case winui.VkReturn, winui.VkSpace:
		if s.focus < 0 {
			s.focus = s.defaultIndex()
		}
		s.activate(hwnd, s.focus)
		return 0
	case winui.VkEscape:
		s.finish(hwnd, s.closeResult())
		return 0
	}
	return 0
}

// defaultIndex devolve o índice do botão padrão (DefaultButtonID).
func (s *cardState) defaultIndex() int {
	for i, b := range s.req.Buttons {
		if strings.EqualFold(b.ID, s.req.DefaultButtonID) {
			return i
		}
	}
	return len(s.req.Buttons) - 1
}

// timeoutIndex devolve o índice do botão de fim de contagem (para foco inicial
// quando há contador e o default é o botão de timeout).
func (s *cardState) timeoutIndex() int {
	for i, b := range s.req.Buttons {
		if strings.EqualFold(b.ID, s.req.TimeoutButtonID) {
			return i
		}
	}
	return -1
}

func cardLParamX(lparam uintptr) int { return int(int16(lparam & 0xFFFF)) }
func cardLParamY(lparam uintptr) int { return int(int16((lparam >> 16) & 0xFFFF)) }

var (
	cardClassOnce sync.Once
	cardClassErr  error
	cardStates    sync.Map // hwnd -> *cardState
)

func ensureCardClass() error {
	cardClassOnce.Do(func() {
		cardClassErr = winui.RegisterWindowClass(
			cardClassName,
			windows.NewCallback(cardWndProc),
			winui.ClassStyleDropShadow,
		)
	})
	return cardClassErr
}

// cardWndProc é o procedimento da janela do cartão.
func cardWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	raw, ok := cardStates.Load(hwnd)
	if !ok {
		return winui.DefWindowProc(hwnd, msg, wparam, lparam)
	}
	state, _ := raw.(*cardState)

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
		if state.req.CountdownSeconds > 0 && state.remaining() <= 0 {
			state.finish(hwnd, state.timeoutResult())
			return 0
		}
		winui.Invalidate(hwnd)
		return 0
	case winui.MsgMouseMove:
		hover := state.buttonAt(cardLParamX(lparam), cardLParamY(lparam))
		if hover != state.hover {
			state.hover = hover
			winui.Invalidate(hwnd)
		}
		winui.TrackMouseLeave(hwnd)
		return 0
	case winui.MsgMouseLeave:
		if state.hover != -1 {
			state.hover = -1
			winui.Invalidate(hwnd)
		}
		return 0
	case winui.MsgLButtonDown:
		idx := state.buttonAt(cardLParamX(lparam), cardLParamY(lparam))
		if idx >= 0 {
			state.pressed = idx
			state.focus = idx
			winui.SetCapture(hwnd)
			winui.Invalidate(hwnd)
		}
		return 0
	case winui.MsgLButtonUp:
		pressed := state.pressed
		state.pressed = -1
		winui.ReleaseCapture()
		winui.Invalidate(hwnd)
		if pressed >= 0 && state.buttonAt(cardLParamX(lparam), cardLParamY(lparam)) == pressed {
			state.activate(hwnd, pressed)
		}
		return 0
	case winui.MsgSetCursor:
		if state.buttonAt(cardLParamX(lparam), cardLParamY(lparam)) >= 0 {
			winui.SetCursorHand()
		} else {
			winui.SetCursorArrow()
		}
		return 1
	case winui.MsgKeyDown:
		return state.handleKey(hwnd, wparam)
	case cardWMAppCancel:
		state.finish(hwnd, CardResult{Outcome: OutcomeCanceled})
		return 0
	case winui.MsgClose:
		state.finish(hwnd, state.closeResult())
		return 0
	case winui.MsgDestroy:
		state.releaseProcessIcons()
		state.fonts.release()
		cardStates.Delete(hwnd)
		winui.PostQuitMessage(0)
		return 0
	}
	return winui.DefWindowProc(hwnd, msg, wparam, lparam)
}

// paint desenha o cartão inteiro (double buffering como no consent).
func (s *cardState) paint(screenDC uintptr) {
	width := winui.ScalePx(cardWidth, s.scale)
	height := s.cardHeight()

	memDC, bitmap, oldBitmap := winui.BufferedPaint(screenDC, width, height)
	hdc := memDC
	if hdc == 0 {
		hdc = screenDC
	} else {
		defer winui.FlushBufferedPaint(screenDC, memDC, bitmap, oldBitmap, width, height)
	}

	theme := s.theme
	l := s.layout
	card := winui.Rect{W: width, H: height}

	cardRadius := winui.ScalePx(cardRadius, s.scale)
	if s.systemCorners {
		cardRadius = 0
	}
	winui.FillRoundRectAA(hdc, card, cardRadius, theme.Card)
	accentMargin := winui.ScalePx(10, s.scale)
	winui.FillRoundRectAA(hdc,
		winui.Rect{X: winui.ScalePx(3, s.scale), Y: accentMargin, W: winui.ScalePx(4, s.scale), H: height - 2*accentMargin},
		winui.ScalePx(2, s.scale), theme.Accent)
	winui.StrokeRoundRectAA(hdc, card, cardRadius, 1, theme.Border)

	// Selo: logo do agente (o "D") onde antes ficava o sino genérico.
	if !drawCardBadge(hdc, l.badge, theme, s.scale) {
		drawNotifyBadge(hdc, l.badge, theme, s.scale)
	}
	// Ícone do motivo da notificação (informação/aviso/erro/pergunta).
	drawCardKindIcon(hdc, l.kindIcon, s.req.Icon, theme, s.scale)

	winui.DrawTextSingle(hdc, s.req.Title, l.title, s.fonts.title, theme.Text, winui.AlignLeft)
	winui.DrawTextSingle(hdc, s.req.Subtitle, l.subtitle, s.fonts.subtitle, theme.TextMuted, winui.AlignLeft)
	winui.FillRect(hdc, l.divider, theme.Border)

	winui.FillRoundRectBorderAA(hdc, l.messageBox, winui.ScalePx(8, s.scale), theme.Surface, theme.Border)
	winui.DrawTextWrappedEllipsis(hdc, s.req.Message, l.messageText, s.fonts.body, theme.Text, winui.AlignLeft)

	if len(l.processRows) > 0 {
		winui.FillRoundRectBorderAA(hdc, l.processBox, winui.ScalePx(8, s.scale), theme.Surface, theme.Border)
		for i, row := range l.processRows {
			if i >= len(s.req.ProcessList) {
				break
			}
			item := s.req.ProcessList[i]
			iconRect := winui.Rect{
				X: row.X, Y: row.Y + (row.H-winui.ScalePx(cardProcessIcon, s.scale))/2,
				W: winui.ScalePx(cardProcessIcon, s.scale), H: winui.ScalePx(cardProcessIcon, s.scale),
			}
			var hIcon uintptr
			if i < len(s.processIcons) {
				hIcon = s.processIcons[i]
			}
			if hIcon != 0 {
				winui.DrawIconAt(hdc, hIcon, iconRect)
			} else {
				drawProcessIconFallback(hdc, iconRect, theme, s.scale, item.DisplayName, s.fonts.processFallback)
			}
			nameRect := winui.Rect{X: iconRect.Right() + winui.ScalePx(10, s.scale), Y: row.Y + winui.ScalePx(3, s.scale), W: row.Right() - iconRect.Right() - winui.ScalePx(10, s.scale), H: winui.ScalePx(18, s.scale)}
			pathRect := winui.Rect{X: nameRect.X, Y: nameRect.Bottom() + winui.ScalePx(1, s.scale), W: nameRect.W, H: winui.ScalePx(14, s.scale)}
			winui.DrawTextSingle(hdc, item.DisplayName, nameRect, s.fonts.body, theme.Text, winui.AlignLeft)
			winui.DrawTextSingle(hdc, processPathLabel(item.ImagePath), pathRect, s.fonts.hint, theme.TextMuted, winui.AlignLeft)
		}
	}

	if s.req.CountdownSeconds > 0 {
		winui.DrawTextSingle(hdc, s.req.CountdownLabel, l.countLabel, s.fonts.countLabel, theme.TextMuted, winui.AlignLeft)
		winui.DrawTextSingle(hdc, CountdownClock(s.remaining()), l.countValue, s.fonts.countValue, theme.Accent, winui.AlignLeft)
		winui.FillRoundRectAA(hdc, l.progressTrack, winui.ScalePx(4, s.scale), theme.Border)
		progress := l.progressTrack
		progress.W = int(float64(l.progressTrack.W) * s.progressRatio())
		if progress.W < winui.ScalePx(8, s.scale) {
			progress.W = winui.ScalePx(8, s.scale)
		}
		if progress.W > l.progressTrack.W {
			progress.W = l.progressTrack.W
		}
		winui.FillRoundRectAA(hdc, progress, winui.ScalePx(4, s.scale), theme.Accent)
	}

	winui.DrawTextSingle(hdc, s.req.Hint, l.hint, s.fonts.hint, theme.TextMuted, winui.AlignLeft)

	buttonRadius := winui.ScalePx(9, s.scale)
	for i, rect := range l.buttons {
		button := s.req.Buttons[i]
		var style winui.ButtonStyle
		if button.IsPrimary() {
			style = winui.PillButton(theme.Accent, theme.Accent.Lighten(12), theme.Accent.Darken(12), theme.Card.ContrastText(), buttonRadius)
		} else {
			style = winui.OutlineButton(theme, buttonRadius)
		}
		// Sem anel de foco: o contorno em volta dos botões foi removido por
		// design (o foco continua navegável e o Enter ainda confirma).
		winui.PaintButton(hdc, rect, button.Label, s.fonts.button, style,
			s.hover == i, s.pressed == i, false, theme.Accent)
	}
}

// drawNotifyBadge desenha o selo do cabeçalho: um sino/alerta genérico no
// tema do Windows (independe de asset externo).
func drawNotifyBadge(hdc uintptr, rect winui.Rect, theme winui.Theme, scale float64) {
	winui.FillRoundRectBorderAA(hdc, rect, winui.ScalePx(12, scale),
		theme.Card.Blend(theme.Accent, 16),
		theme.Card.Blend(theme.Accent, 40))
	cx := rect.X + rect.W/2
	// Corpo do sino: arco aproximado por retângulo arredondado + "badalo".
	body := winui.Rect{X: cx - winui.ScalePx(8, scale), Y: rect.Y + winui.ScalePx(10, scale), W: winui.ScalePx(16, scale), H: winui.ScalePx(14, scale)}
	winui.StrokeRoundRectAA(hdc, body, winui.ScalePx(6, scale), winui.ScalePx(2, scale), theme.Accent)
	winui.FillRect(hdc, winui.Rect{X: cx - winui.ScalePx(2, scale), Y: body.Bottom() + winui.ScalePx(1, scale), W: winui.ScalePx(4, scale), H: winui.ScalePx(3, scale)}, theme.Accent)
	winui.FillRect(hdc, winui.Rect{X: cx - winui.ScalePx(1, scale), Y: rect.Bottom() - winui.ScalePx(6, scale), W: winui.ScalePx(2, scale), H: winui.ScalePx(3, scale)}, theme.Accent)
}

// drawCardBadge desenha o selo do cabeçalho: fundo tingido pelo accent com o
// logo do agente (o "D") por cima. Devolve false quando o PNG não pôde ser
// desenhado (o chamador cai no sino vetorial antigo).
func drawCardBadge(hdc uintptr, rect winui.Rect, theme winui.Theme, scale float64) bool {
	sw := func(v int) int { return winui.ScalePx(v, scale) }
	winui.FillRoundRectBorderAA(hdc, rect, sw(12),
		theme.Card.Blend(theme.Accent, 16),
		theme.Card.Blend(theme.Accent, 40))
	// O logo já traz folga transparente em volta do "D".
	return DrawAgentLogo(hdc, rect)
}

// drawCardKindIcon desenha o glifo do motivo da notificação à direita do
// cabeçalho, no tema do Windows.
func drawCardKindIcon(hdc uintptr, rect winui.Rect, kind string, theme winui.Theme, scale float64) {
	if rect.Empty() {
		return
	}
	color := theme.Accent
	switch normalizeIconKind(kind) {
	case IconWarning:
		color = theme.Accent
	case IconError:
		color = theme.Danger
	}
	DrawKindIcon(hdc, rect, kind, color)
}

// drawProcessIconFallback desenha um quadrado arredondado com a inicial do
// programa quando o ícone do executável não pôde ser extraído.
func drawProcessIconFallback(hdc uintptr, rect winui.Rect, theme winui.Theme, scale float64, name string, fallbackFont uintptr) {
	winui.FillRoundRectBorderAA(hdc, rect, winui.ScalePx(6, scale), theme.Card.Blend(theme.Accent, 12), theme.Border)
	letter := ""
	for _, r := range strings.TrimSpace(name) {
		letter = string(unicode.ToUpper(r))
		break
	}
	if letter == "" {
		return
	}
	winui.DrawTextSingle(hdc, letter, rect, fallbackFont, theme.Accent, winui.AlignCenter)
}

// processPathLabel encurta o caminho do executável (mostra o nome do arquivo).
func processPathLabel(imagePath string) string {
	if imagePath == "" {
		return ""
	}
	if idx := strings.LastIndexAny(imagePath, "\\"); idx >= 0 && idx+1 < len(imagePath) {
		return imagePath[idx+1:]
	}
	return imagePath
}

// releaseProcessIcons libera os HICONs da lista de processos.
func (s *cardState) releaseProcessIcons() {
	for _, hIcon := range s.processIcons {
		winui.DestroyIconHandle(hIcon)
	}
	s.processIcons = nil
}

// ShowCard abre o cartão nativo e devolve o resultado. O contexto cancelado
// fecha o cartão com Outcome "canceled".
func ShowCard(ctx context.Context, req CardRequest) (CardResult, bool) {
	req = NormalizeCardRequest(req)
	if err := ensureCardClass(); err != nil {
		return CardResult{Outcome: OutcomeError, Reason: err.Error()}, false
	}
	if ctx.Err() != nil {
		return CardResult{Outcome: OutcomeCanceled}, true
	}
	resultCh := make(chan CardResult, 1)
	go func() {
		resultCh <- runCardWindow(ctx, req)
	}()
	return <-resultCh, true
}

// runCardWindow cria a janela, roda o loop de mensagens e devolve o resultado.
func runCardWindow(ctx context.Context, req CardRequest) CardResult {
	hasCountdown := req.CountdownSeconds > 0
	total := time.Duration(req.CountdownSeconds) * time.Second

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Posição/geometry inicial com o tamanho de projeto; após o DPI o cartão é
	// redimensionado e re-ancorado.
	scale0 := 1.0
	layout0 := cardLayoutFor(scale0, 0, hasCountdown, len(req.Buttons), len(req.ProcessList))
	height0 := cardHeightFor(layout0, scale0)

	x, y := winui.BottomRightOnActiveWorkArea(cardWidth, height0, cardPad)
	hwnd, err := winui.CreateWindow(
		cardClassName,
		req.Title,
		winui.ExStyleTopmost|winui.ExStyleToolWindow,
		winui.WinStylePopup|winui.WinStyleClipSiblings,
		x, y, cardWidth, height0,
	)
	if err != nil {
		return CardResult{Outcome: OutcomeError, Reason: err.Error()}
	}

	scale := winui.ScaleForDPI(winui.DpiForWindow(hwnd))

	// Crescimento dinâmico da caixa de mensagem (textos longos).
	bodyFont := winui.CreateFont(winui.ScalePx(15, scale), 400)
	messageTextRect := cardLayoutFor(scale, 0, hasCountdown, len(req.Buttons), len(req.ProcessList)).messageText.Inset(winui.ScalePx(14, scale), winui.ScalePx(10, scale))
	extraHeight := 0
	if needed := winui.MeasureWrappedText(bodyFont, req.Message, messageTextRect.W); needed > messageTextRect.H {
		extraHeight = needed - messageTextRect.H
		if maxExtra := winui.ScalePx(cardMessageMaxExtraPx, scale); extraHeight > maxExtra {
			extraHeight = maxExtra
		}
	}
	layout := cardLayoutFor(scale, extraHeight, hasCountdown, len(req.Buttons), len(req.ProcessList))
	height := cardHeightFor(layout, scale)

	// Ícones dos processos (extração shell na thread da janela, liberada no
	// WM_DESTROY). Falha em um ícone cai no fallback vetorial.
	processIcons := make([]uintptr, len(req.ProcessList))
	for i, item := range req.ProcessList {
		if hIcon, ok := winui.ExtractFileIcon(item.ImagePath); ok {
			processIcons[i] = hIcon
		}
	}

	state := &cardState{
		req:      req,
		total:    total,
		deadline: time.Now().Add(total),
		resultCh: make(chan CardResult, 1),
		hover:    -1,
		pressed:  -1,
		layout:   layout,
		scale:    scale,

		processIcons: processIcons,
	}
	state.focus = state.defaultIndex()
	state.dwmCornerRadius = winui.ScalePx(8, scale)

	x, y = winui.BottomRightOnActiveWorkArea(cardWidth, height, winui.ScalePx(cardPad, scale))
	winui.MoveResizeWindow(hwnd, x, y, cardWidth, height)

	state.systemCorners = winui.SetSystemRoundedCorners(hwnd)
	if !state.systemCorners {
		winui.SetRoundedRegion(hwnd, cardWidth, height, winui.ScalePx(cardRadius, scale))
	}
	state.theme = winui.DetectTheme()
	state.fonts = cardFonts{
		title:       winui.CreateFont(winui.ScalePx(22, scale), 600),
		subtitle:    winui.CreateFont(winui.ScalePx(14, scale), 400),
		body:        bodyFont,
		countLabel:  winui.CreateFont(winui.ScalePx(12, scale), 400),
		countValue:  winui.CreateFont(winui.ScalePx(26, scale), 600),
		button:      winui.CreateFont(winui.ScalePx(14, scale), 600),
		hint:        winui.CreateFont(winui.ScalePx(11, scale), 400),

		processFallback: winui.CreateFont(winui.ScalePx(14, scale), 600),
	}

	// Só publica o estado depois da geometria/fontes prontas.
	cardStates.Store(hwnd, state)

	winui.ShowWindow(hwnd, winui.SwShowNormal)
	winui.EnsureTopMost(hwnd)
	winui.SetForegroundWindow(hwnd)
	winui.SetFocusTo(hwnd)
	winui.UpdateWindow(hwnd)
	if hasCountdown {
		winui.SetTimer(hwnd, cardTimerID, cardTickMS)
	}

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			winui.PostMessage(hwnd, cardWMAppCancel, 0, 0)
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
	result := state.result
	state.mu.Unlock()
	if decided {
		return result
	}
	return CardResult{Outcome: OutcomeCanceled}
}