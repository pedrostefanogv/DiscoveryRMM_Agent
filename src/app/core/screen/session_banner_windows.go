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
	bannerWidth     = 620
	bannerHeight    = 68
	bannerRadius    = 14
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
		bannerClassErr = winui.RegisterWindowClass(
			bannerClassName,
			windows.NewCallback(bannerWndProc),
			winui.ClassStyleDropShadow,
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

// NewSessionBanner cria e exibe o banner na thread dedicada. Retorna erro se a
// janela não puder ser excluída da captura (nesse caso nada é exibido).
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

		// Transparência uniforme (a janela é layered por causa do click-through).
		winui.SetLayeredAlpha(hwnd, bannerAlpha)
		winui.SetRoundedRegion(hwnd, bannerWidth, bannerHeight, bannerRadius)
		banner.theme = bannerThemeLoader()
		banner.fonts = bannerFonts{
			primary:   winui.CreateFont(16, 600),
			secondary: winui.CreateFont(13, 400),
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
// compartilhado (monitorIndex do capturer). Sem geometria, usa o primário.
func bannerPosition(monitorIndex int) (x, y int) {
	monitors, err := GetMonitors()
	if err != nil || len(monitors) == 0 {
		area := winui.WorkArea()
		return area.X + (area.W-bannerWidth)/2, area.Y + 8
	}
	index := monitorIndex
	if index < 0 || index >= len(monitors) {
		index = 0
	}
	target := monitors[index]
	x = target.X + (target.Width-bannerWidth)/2
	// Monitor mais estreito que o banner: não sai pela esquerda.
	if x < target.X {
		x = target.X
	}
	return x, target.Y + 8
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

// paint desenha o cartão inteiro no DC (memória quando disponível).
func (b *SessionBanner) paint(screenDC uintptr) {
	memDC, bitmap, oldBitmap := winui.BufferedPaint(screenDC, bannerWidth, bannerHeight)
	hdc := memDC
	if hdc == 0 {
		hdc = screenDC
	} else {
		defer winui.FlushBufferedPaint(screenDC, memDC, bitmap, oldBitmap, bannerWidth, bannerHeight)
	}

	theme := b.theme
	card := winui.Rect{W: bannerWidth, H: bannerHeight}
	// Cartão + borda + faixa de destaque à esquerda.
	winui.FillRoundRect(hdc, card, bannerRadius, theme.Card)
	winui.FillRect(hdc, winui.Rect{X: 0, Y: 2, W: 5, H: bannerHeight - 4}, theme.Accent)
	winui.StrokeRoundRect(hdc, card, bannerRadius, 1, theme.Border)

	// Indicador de sessão ativa.
	winui.FillEllipse(hdc, winui.Rect{X: 24, Y: 28, W: 12, H: 12}, theme.Accent)

	textWidth := bannerWidth - 48 - 24
	winui.DrawTextSingle(hdc, b.primaryText(),
		winui.Rect{X: 48, Y: 12, W: textWidth, H: 22}, b.fonts.primary, theme.Text, winui.AlignLeft)
	winui.DrawTextSingle(hdc, b.secondaryText(),
		winui.Rect{X: 48, Y: 37, W: textWidth, H: 18}, b.fonts.secondary, theme.TextMuted, winui.AlignLeft)
}

// Close encerra a janela (idempotente). A destruição roda na thread dona da
// janela: daqui só é postado um WM_CLOSE.
func (b *SessionBanner) Close() {
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
	select {
	case <-b.doneCh:
	case <-time.After(2 * time.Second):
	}
}
