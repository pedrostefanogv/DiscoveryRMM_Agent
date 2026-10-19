//go:build windows

package screen

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"

	"discovery/app/core/winui"
)

// ── Banner local da sessão remota (informativo, CLICK-THROUGH) ──
//
// Cartão flutuante topmost no desktop do usuário acessado: operador + tempo de
// sessão. É 100% click-through (WS_EX_LAYERED|WS_EX_TRANSPARENT + HTTRANSPARENT
// no WM_NCHITTEST): o banner NUNCA captura o mouse.
//
// O motivo é direto: o banner é EXCLUÍDO da captura (WDA_EXCLUDEFROMCAPTURE),
// então o operador remoto vê o app por baixo dele e clica "através" do banner.
// Quando a janela capturava o clique (comportamento anterior), clicar em abas
// do navegador — que ficam justamente no topo da tela — simplesmente não
// funcionava, embora o operador não visse o banner.
//
// A ação "Desconectar" saiu do banner e passou para o ícone da bandeja
// (screen.SessionTray): o usuário local encerra a sessão sem que nenhuma janela
// fique por cima dos apps.
//
// O banner continua excluído da captura: o viewer e a gravação não podem vê-lo.
// Se a exclusão não for suportada pelo Windows, a janela NÃO é exibida (melhor
// sem banner do que vazando no stream).

const (
	// wdaExcludeFromCapture é o SetWindowDisplayAffinity(WDA_EXCLUDEFROMCAPTURE).
	wdaExcludeFromCapture = 0x00000011

	bannerClassName = "DiscoveryRmmSessionBanner"
	// Medidas de projeto em 96 DPI (100%). O cartão real é escalado pelo DPI
	// efetivo do monitor compartilhado (ver NewSessionBanner/paintMetrics):
	// sem isso, em telas a 125%/150% o banner (e as fontes) ficava
	// proporcionalmente menor que o restante da interface.
	bannerWidth     = 620
	bannerHeight    = 68
	bannerRadius    = 14
	bannerFontPx    = 16
	bannerSubFontPx = 13
	bannerTimerID   = 1
	bannerRefreshMS = 1000
	// bannerAlpha: cartão levemente translúcido sem perder contraste.
	bannerAlpha = 242
)

var procSetWindowDisplayAffinity = user32DLL.NewProc("SetWindowDisplayAffinity")

// BannerOptions descreve o banner a criar. Não há botão: a desconexão é feita
// pelo ícone da bandeja (SessionTray), para não bloquear cliques do app em uso.
type BannerOptions struct {
	Operator        string
	Text            string
	AllowDisconnect bool
	MonitorIndex    int
}

// bannerFonts agrupa as fontes do banner (liberadas no WM_DESTROY).
type bannerFonts struct {
	primary   uintptr
	secondary uintptr
}

func (f bannerFonts) release() {
	winui.DeleteGDIObject(f.primary)
	winui.DeleteGDIObject(f.secondary)
}

// SessionBanner é o cartão flutuante exibido durante a sessão de tela.
type SessionBanner struct {
	mu       sync.Mutex
	hwnd     uintptr
	start    time.Time
	baseText string
	operator string
	allow    bool
	closed   bool
	ready    chan error
	doneCh   chan struct{}

	// Estado de desenho (somente thread da janela).
	theme winui.Theme
	fonts bannerFonts

	// Geometria efetiva em pixels físicos, já escalada pelo DPI do monitor.
	// width/height/radius são preenchidos em NewSessionBanner; scale 0 (ex.:
	// testes que pintam sem criar janela) cai no layout de 96 DPI.
	width  int
	height int
	radius int
	scale  float64
}

// bannerState liga o HWND ao estado Go (o wndproc só recebe uintptrs).
type bannerState struct {
	banner *SessionBanner
}

var bannerStates sync.Map

var (
	bannerClassOnce sync.Once
	bannerClassErr  error
)

func ensureBannerClass() error {
	bannerClassOnce.Do(func() {
		// Sem CS_DROPSHADOW: a sombra do sistema era desenhada deslocada
		// (direita/abaixo) e deixava um borrão escuro atrás do cartão no
		// desktop do usuário acessado. O cartão já tem borda própria.
		bannerClassErr = winui.RegisterWindowClass(
			bannerClassName,
			windows.NewCallback(bannerWndProc),
			0,
		)
	})
	return bannerClassErr
}

// bannerThemeLoader lê o tema do usuário; var para testes de pré-visualização.
var bannerThemeLoader = winui.DetectTheme

func bannerWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	raw, ok := bannerStates.Load(hwnd)
	if !ok {
		return winui.DefWindowProc(hwnd, msg, wparam, lparam)
	}
	state, _ := raw.(*bannerState)
	banner := state.banner

	switch msg {
	case winui.MsgEraseBkgnd:
		// Toda a pintura é feita em WM_PAINT (double buffer).
		return 1
	case winui.MsgPaint:
		hdc, paint := winui.BeginPaint(hwnd)
		banner.paint(hdc)
		winui.EndPaint(hwnd, &paint)
		return 0
	case winui.MsgPrintClient:
		banner.paint(wparam)
		return 0
	case winui.MsgNCHitTest:
		// Cinto e suspensório do WS_EX_TRANSPARENT: qualquer hit-test que ainda
		// chegue aqui atravessa para a janela de baixo.
		return ^uintptr(0) // HTTRANSPARENT
	case winui.MsgTimer:
		winui.Invalidate(hwnd)
		return 0
	case winui.MsgSettingChange:
		// O usuário trocou tema/accent com a sessão aberta: relê e repinta.
		banner.theme = bannerThemeLoader()
		winui.Invalidate(hwnd)
		return 0
	case winui.MsgClose:
		winui.DestroyWindow(hwnd)
		return 0
	case winui.MsgDestroy:
		banner.fonts.release()
		banner.fonts = bannerFonts{}
		bannerStates.Delete(hwnd)
		winui.PostQuitMessage(0)
		return 0
	}
	return winui.DefWindowProc(hwnd, msg, wparam, lparam)
}

// bannerScaleForDPI converte o DPI efetivo do monitor em fator de escala
// (96 DPI = 100%). Clampa em [1, 3]: não encolhe o cartão abaixo do projeto e
// evita tamanhos absurdos em configurações de acessibilidade extremas.
func bannerScaleForDPI(dpi int) float64 {
	return winui.ScaleForDPI(dpi)
}

// scalePx aplica o fator de escala a uma medida de projeto (96 DPI).
func scalePx(value int, scale float64) int {
	return winui.ScalePx(value, scale)
}

// paintMetrics devolve a geometria efetiva usada pelo desenho. Quando o
// SessionBanner é montado manualmente (testes de pintura), cai no layout de
// 96 DPI.
func (b *SessionBanner) paintMetrics() (width, height, radius int, scale float64) {
	scale = b.scale
	if scale <= 0 {
		scale = 1
	}
	width, height, radius = b.width, b.height, b.radius
	if width <= 0 {
		width = bannerWidth
	}
	if height <= 0 {
		height = bannerHeight
	}
	if radius <= 0 {
		radius = bannerRadius
	}
	return width, height, radius, scale
}

// NewSessionBanner cria e exibe o banner na thread dedicada. Retorna erro se a
// janela não puder ser excluída da captura (nesse caso nada é exibido).
//
// O tamanho final (janela, raio, fontes e coordenadas de desenho) é escalado
// pelo DPI efetivo do monitor compartilhado — com PerMonitorV2 o Windows NÃO
// escala a janela sozinho. Mudanças de escala com a sessão já aberta não
// redimensionam o cartão (não há WM_DPICHANGED sendo tratado aqui).
func NewSessionBanner(opts BannerOptions) (*SessionBanner, error) {
	if err := ensureBannerClass(); err != nil {
		return nil, err
	}

	banner := &SessionBanner{
		start:    time.Now(),
		baseText: strings.TrimSpace(opts.Text),
		operator: strings.TrimSpace(opts.Operator),
		allow:    opts.AllowDisconnect,
		ready:    make(chan error, 1),
		doneCh:   make(chan struct{}),
	}

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(banner.doneCh)

		x, y := bannerPosition(opts.MonitorIndex)
		hwnd, err := winui.CreateWindow(
			bannerClassName,
			"Discovery RMM — sessão remota",
			winui.ExStyleTopmost|winui.ExStyleToolWindow|winui.ExStyleNoActivate|
				winui.ExStyleLayered|winui.ExStyleTransparent,
			winui.WinStylePopup|winui.WinStyleClipSiblings,
			x, y, bannerWidth, bannerHeight,
		)
		if err != nil {
			banner.ready <- err
			return
		}
		banner.mu.Lock()
		banner.hwnd = hwnd
		banner.mu.Unlock()
		bannerStates.Store(hwnd, &bannerState{banner: banner})

		// Fora da captura: sem isso o banner apareceria no viewer/gravação.
		if ok, _, affinityErr := procSetWindowDisplayAffinity.Call(hwnd, wdaExcludeFromCapture); ok == 0 {
			winui.DestroyWindow(hwnd)
			banner.ready <- fmt.Errorf("SetWindowDisplayAffinity falhou: %v (errno=%d)", affinityErr, errnoOf(affinityErr))
			return
		}

		// DPI efetivo do monitor compartilhado. Com manifest PerMonitorV2 o
		// Windows não escala a janela sozinho: sem isto o cartão ficava menor
		// que o resto da UI em telas a 125%/150%.
		dpi := winui.DpiForWindow(hwnd)
		scale := bannerScaleForDPI(dpi)
		width := scalePx(bannerWidth, scale)
		height := scalePx(bannerHeight, scale)
		radius := scalePx(bannerRadius, scale)
		banner.width, banner.height, banner.radius, banner.scale = width, height, radius, scale

		// A posição inicial usou a largura de projeto; centraliza de novo com a
		// largura efetiva (escala > 1 alarga o cartão).
		x, y = bannerPositionForSize(opts.MonitorIndex, width, scalePx(8, scale))
		winui.MoveResizeWindow(hwnd, x, y, width, height)

		// Transparência uniforme (a janela é layered por causa do click-through).
		winui.SetLayeredAlpha(hwnd, bannerAlpha)
		winui.SetRoundedRegion(hwnd, width, height, radius)
		banner.theme = bannerThemeLoader()
		banner.fonts = bannerFonts{
			primary:   winui.CreateFont(scalePx(bannerFontPx, scale), 600),
			secondary: winui.CreateFont(scalePx(bannerSubFontPx, scale), 400),
		}

		winui.ShowWindow(hwnd, winui.SwShowNormal)
		winui.UpdateWindow(hwnd)
		winui.SetTimer(hwnd, bannerTimerID, bannerRefreshMS)
		banner.ready <- nil

		var msg winui.Msg
		for winui.NextMessage(&msg) > 0 {
			winui.TranslateMessage(&msg)
			winui.DispatchMessage(&msg)
		}
	}()

	if err := <-banner.ready; err != nil {
		return nil, err
	}
	return banner, nil
}

// bannerPosition centraliza o banner no topo do monitor que está sendo
// compartilhado (monitorIndex do capturer) usando a largura de projeto.
func bannerPosition(monitorIndex int) (x, y int) {
	return bannerPositionForWidth(monitorIndex, bannerWidth)
}

// bannerPositionForWidth é a posição real do banner com a largura efetiva
// (escalada pelo DPI), mantendo a margem superior de projeto (8px @96 DPI).
func bannerPositionForWidth(monitorIndex, width int) (x, y int) {
	return bannerPositionForSize(monitorIndex, width, 8)
}

// bannerPositionForSize centraliza o banner no topo do monitor com largura e
// margem superior em pixels físicos. Sem geometria, usa a área útil do
// monitor primário.
func bannerPositionForSize(monitorIndex, width, topMargin int) (x, y int) {
	monitors, err := GetMonitors()
	if err != nil || len(monitors) == 0 {
		area := winui.WorkArea()
		return area.X + (area.W-width)/2, area.Y + topMargin
	}
	index := monitorIndex
	if index < 0 || index >= len(monitors) {
		index = 0
	}
	target := monitors[index]
	x = target.X + (target.Width-width)/2
	// Monitor mais estreito que o banner: não sai pela esquerda.
	if x < target.X {
		x = target.X
	}
	return x, target.Y + topMargin
}

// primaryText é a linha principal do banner (texto do servidor ou default).
// Quando o template do servidor não traz o operador, o nome é anexado aqui (e
// não na linha de baixo) para o texto de encerramento caber inteiro.
func (b *SessionBanner) primaryText() string {
	text := b.baseText
	if text == "" {
		text = "Sessão remota em andamento"
	}
	if b.operator != "" && !strings.Contains(strings.ToLower(text), strings.ToLower(b.operator)) {
		text += " — " + b.operator
	}
	return text
}

// secondaryText mostra o tempo decorrido e, quando a política permite, como
// encerrar a sessão — com a mesma instrução que o usuário encontra na bandeja.
func (b *SessionBanner) secondaryText() string {
	elapsed := time.Since(b.start)
	clock := fmt.Sprintf("%02d:%02d", int(elapsed.Minutes()), int(elapsed.Seconds())%60)
	if !b.allow {
		return "Conectado há " + clock
	}
	return clock + "  ·  Para encerrar, clique no ícone do agente na bandeja do sistema"
}

// paint desenha o cartão inteiro no DC (memória quando disponível). Todas as
// medidas de projeto (96 DPI) passam por sw(), então o layout acompanha a
// escala do monitor (janela/fontes já vêm escaladas de NewSessionBanner).
func (b *SessionBanner) paint(screenDC uintptr) {
	width, height, radius, scale := b.paintMetrics()
	sw := func(value int) int { return scalePx(value, scale) }

	memDC, bitmap, oldBitmap := winui.BufferedPaint(screenDC, width, height)
	hdc := memDC
	if hdc == 0 {
		hdc = screenDC
	} else {
		defer winui.FlushBufferedPaint(screenDC, memDC, bitmap, oldBitmap, width, height)
	}

	theme := b.theme
	full := winui.Rect{W: width, H: height}
	// O bitmap do double buffer nasce preto: pintar o cliente inteiro com a
	// cor do cartão evita pixels escuros nas bordas/cantos (a região
	// arredondada da janela recorta o contorno).
	winui.FillRect(hdc, full, theme.Card)

	// Borda 1px desenhada 1px para dentro: com o traço exatamente no limite,
	// metade da caneta ficava fora da janela e a linha inferior/direita saía
	// falhada. O raio acompanha o recorte da janela (radius-1).
	inset := sw(1)
	card := winui.Rect{X: inset, Y: inset, W: width - 2*inset, H: height - 2*inset}
	winui.StrokeRoundRectAA(hdc, card, radius-inset, sw(1), theme.Border)

	// Faixa de destaque à esquerda: pílula arredondada com margem vertical.
	// Antes ela ia de ponta a ponta e era recortada pelo canto arredondado,
	// deixando degraus de accent (pixels "serrilhados") nos cantos.
	accentMargin := sw(10)
	winui.FillRoundRectAA(hdc,
		winui.Rect{X: sw(3), Y: accentMargin, W: sw(4), H: height - 2*accentMargin},
		sw(2), theme.Accent)

	// Indicador de sessão ativa.
	winui.FillEllipseAA(hdc, winui.Rect{X: sw(24), Y: sw(28), W: sw(12), H: sw(12)}, theme.Accent)

	textLeft := sw(48)
	textWidth := width - textLeft - sw(24)
	winui.DrawTextSingle(hdc, b.primaryText(),
		winui.Rect{X: textLeft, Y: sw(12), W: textWidth, H: sw(22)}, b.fonts.primary, theme.Text, winui.AlignLeft)
	winui.DrawTextSingle(hdc, b.secondaryText(),
		winui.Rect{X: textLeft, Y: sw(37), W: textWidth, H: sw(18)}, b.fonts.secondary, theme.TextMuted, winui.AlignLeft)
}

// RequestClose pede o encerramento da janela sem bloquear (idempotente): a
// destruição roda na thread dona da janela, então aqui só é postado WM_CLOSE.
//
// É o método usado pelo manager de sessões: Close() pode esperar até 2s pela
// thread da janela e o closeSessionLocked roda com o mutex do manager preso.
func (b *SessionBanner) RequestClose() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	hwnd := b.hwnd
	b.mu.Unlock()

	if hwnd != 0 {
		winui.PostMessage(hwnd, winui.MsgClose, 0, 0)
	}
}

// Close encerra a janela e espera a thread dona destruí-la (até 2s). Use
// RequestClose quando o chamador não puder bloquear (ex.: segurando um mutex).
func (b *SessionBanner) Close() {
	if b == nil {
		return
	}
	b.RequestClose()
	if b.doneCh == nil {
		// Banner montado manualmente (sem thread de janela): nada a aguardar.
		return
	}
	select {
	case <-b.doneCh:
	case <-time.After(2 * time.Second):
	}
}
