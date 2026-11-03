//go:build windows

package screen

import (
	"log"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"discovery/app/core/winui"
)

// ── Aviso da bandeja desenhado pelo PRÓPRIO agente ──
//
// PROBLEMA (Win11, 2026-11-03): o balloon clássico (Shell_NotifyIconW NIM_MODIFY
// com NIF_INFO) é desenhado pelo explorer.exe. Como o painel de notificações e a
// área de trabalho ficam na MESMA janela top-level do shell, ele não tem um
// retângulo de janela atribuível ao nosso HWND: nenhuma técnica de exclusão
// (WDA_* ou emenda por região na captura) consegue isolar o aviso. Resultado: o
// operador via o cartão escuro do banner no stream, exatamente na área da
// bandeja.
//
// SOLUÇÃO: o aviso passou a ser uma janela Fluent desenhada por este pacote,
// irmã do banner — topmost, sem ativação, 100% click-through (o clique
// atravessa para o botão que está embaixo na bandeja). Sendo uma janela NOSSA,
// registrada em registerBannerCapture, a emenda por região do capturer GDI a
// cobre do mesmo jeito que cobre o banner: o cartão aparece na tela FÍSICA e
// não chega ao stream.
//
// Comportamentos preservados do balloon:
//   - aparece sozinho, sem clique e sem abrir janela para o usuário;
//   - some sozinho depois de trayBalloonVisibleMS;
//   - é apenas informativo: nada acontece ao clicar nele.
//
// Diferença deliberada: como a mensagem traz "Para encerrar, clique no ícone…",
// o cartão é ancorado ACIMA do ícone do agente — e nunca sobre ele — para a
// instrução continuar clicável (o cartão é click-through, mas cobrir o ícone
// ainda atrapalharia a leitura).

const (
	trayBalloonClassName = "DiscoveryRmmSessionBalloon"
	trayBalloonTextID    = 1
	// trayBalloonVisibleMS é o tempo em tela do aviso. O balloon do shell fica
	// ~5s (configurável no Windows); 6s dá leitura confortável da frase longa.
	trayBalloonVisibleMS = 6000
	// trayBalloonRefreshMS atualiza o relógio decorrido enquanto está visível.
	trayBalloonRefreshMS = 1000
	trayBalloonAlpha     = 244
	trayBalloonGap       = 12
	// Medidas de projeto (96 DPI): o cartão cresce com o DPI efetivo.
	trayBalloonWidth   = 372
	trayBalloonHeight  = 86
	trayBalloonRadius  = 12
	trayBalloonFontPx  = 14
	trayBalloonSubPx   = 12
	trayBalloonPadding = 12
)

var (
	balloonClassOnce sync.Once
	balloonClassErr  error
	// balloonAtual é o cartão vivo (um por sessão).
	balloonMutex sync.Mutex
	balloonAtual *sessionBalloon
)

func ensureBalloonClass() error {
	balloonClassOnce.Do(func() {
		balloonClassErr = winui.RegisterWindowClass(
			trayBalloonClassName,
			windows.NewCallback(sessionBalloonWndProc),
			0,
		)
	})
	return balloonClassErr
}

// sessionBalloon é o cartão do aviso da bandeja. Todos os campos de desenho são
// usados somente na thread da janela, exceto os protegidos por mu.
type sessionBalloon struct {
	mu     sync.Mutex
	hwnd   uintptr
	closed bool
	dieMu  sync.Mutex
	die    chan struct{}

	title   string
	message string
	since   time.Time

	width  int
	height int
	radius int
	scale  float64

	theme winui.Theme
	fonts bannerFonts

	// prolongar é sinalizado por update(): uma mensagem nova enquanto o cartão
	// está na tela recomeça a contagem de trayBalloonVisibleMS.
	prolongar chan struct{}
}

// showTrayBalloon exibe o aviso da bandeja para a mensagem informada. Nunca
// retorna erro para o chamador: um aviso que não pôde ser exibido vira um
// aviso no log do agente (a sessão em si não depende dele).
func (t *SessionTray) showTrayBalloon(message string) {
	message = strings.TrimSpace(message)
	if message == "" || t == nil {
		return
	}
	anchorX, anchorY, anchorOk := t.trayBalloonAnchor()
	if err := showTrayBalloonWindow(message, anchorX, anchorY, anchorOk); err != nil {
		log.Printf("[session-tray] aviso próprio indisponível (%v) — usando o balloon do shell", err)
		t.notifyShellBalloon(message)
	}
}

// trayBalloonAnchor é a área do ícone no desktop. O SHELL não expõe a posição
// do ícone, então usamos duas fontes: o cursor (o usuário quase sempre está com
// o mouse perto do ícone quando a sessão começa) e, quando o cursor está longe
// da bandeja, a quina da área de trabalho (onde o shell desenharia o balloon).
func (t *SessionTray) trayBalloonAnchor() (x, y int, ok bool) {
	var pt struct{ X, Y int32 }
	if ret, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt))); ret != 0 {
		// Área útil do monitor onde o cursor está (multi-monitor: a bandeja pode
		// estar em qualquer monitor, não só no primário).
		area := winui.WorkAreaForPoint(int(pt.X), int(pt.Y))
		// Faixa da bandeja: 220px da borda direita/inferior desse monitor.
		pertoDaBandeja := int(pt.X) >= area.Right()-220 || int(pt.Y) >= area.Bottom()-220
		if pertoDaBandeja {
			return int(pt.X), int(pt.Y), true
		}
	}
	area := winui.WorkArea()
	return area.Right() - trayBalloonPadding, area.Bottom() - trayBalloonPadding, false
}

// showTrayBalloonWindow cria e mostra o cartão. Substitui o anterior (uma nova
// mensagem enquanto o cartão está na tela reaproveita a janela: sem pisca e sem
// registro de captura duplicado).
func showTrayBalloonWindow(message string, anchorX, anchorY int, anchorOk bool) error {
	if err := ensureBalloonClass(); err != nil {
		return err
	}

	// Escala do monitor onde o aviso vai aparecer (o do banner/sessão).
	scale := bannerScaleForDPI(dpiMonitorDaCoordenada(anchorX, anchorY))
	width := scalePx(trayBalloonWidth, scale)
	height := scalePx(trayBalloonHeight, scale)

	balloonMutex.Lock()
	atual := balloonAtual
	balloonMutex.Unlock()

	if atual != nil && atual.update(message) {
		return nil
	}

	b := &sessionBalloon{
		die:       make(chan struct{}),
		prolongar: make(chan struct{}, 1),
		title:     trayBalloonTitle,
		message:   message,
		since:     time.Now(),
		width:     width,
		height:    height,
		radius:    scalePx(trayBalloonRadius, scale),
		scale:     scale,
		theme:     bannerThemeLoader(),
		fonts: bannerFonts{
			primary:   winui.CreateFont(scalePx(trayBalloonFontPx, scale), 600),
			secondary: winui.CreateFont(scalePx(trayBalloonSubPx, scale), 400),
		},
	}
	x, y := trayBalloonPosition(width, height, anchorX, anchorY, anchorOk)
	hwnd, err := winui.CreateWindow(
		trayBalloonClassName,
		"Discovery RMM — aviso",
		winui.ExStyleTopmost|winui.ExStyleToolWindow|winui.ExStyleNoActivate|
			winui.ExStyleLayered|winui.ExStyleTransparent,
		winui.WinStylePopup|winui.WinStyleClipSiblings,
		x, y, width, height,
	)
	if err != nil {
		b.fonts.release()
		return err
	}
	b.hwnd = hwnd

	// Publica o cartão ANTES de mostrar: UpdateWindow dispara WM_PAINT de forma
	// síncrona e o wndproc só pinta quando encontra o cartão em balloonAtual —
	// publicando depois, o primeiro quadro saía vazio (cartão em branco por até
	// 1s, até o timer repintar).
	balloonMutex.Lock()
	anterior := balloonAtual
	balloonAtual = b
	balloonMutex.Unlock()

	winui.SetLayeredAlpha(hwnd, trayBalloonAlpha)
	winui.SetRoundedRegion(hwnd, width, height, b.radius)

	// Máscara da região ANTES de mostrar: os pixels de baixo viram o conteúdo
	// que o pipeline reescreve no frame (ver banner_patch_windows.go). Sem isso
	// o aviso apareceria no stream em capturers que não emendam (DXGI).
	// O aviso fica no canto oposto ao banner, então a máscara não captura o
	// cartão da sessão.
	if err := capturarMascaraRegiao(hwnd); err != nil {
		log.Printf("[session-tray] aviso sem máscara de captura: %v", err)
	} else {
		// O aviso nunca aplica afinidade (WDA_*): sem máscara ativa ele vaza
		// visível para o stream nos capturers que não emendam (DXGI).
		ativarMascara(hwnd)
	}

	// Registra como janela extra da emenda do GDI: o cartão fica na tela física e
	// sai do stream igual ao banner. O histórico NÃO é invalidado de propósito
	// (ver registerCapturaExtra): o quadro anterior foi capturado antes de o
	// cartão aparecer, então já é o conteúdo limpo que a emenda precisa.
	registerCapturaExtra(hwnd)

	winui.ShowWindow(hwnd, winui.SwShowNormal)
	winui.UpdateWindow(hwnd)
	winui.SetTimer(hwnd, trayBalloonTextID, trayBalloonRefreshMS)

	if anterior != nil {
		anterior.requestClose()
	}

	go b.expireAfter(trayBalloonVisibleMS)
	return nil
}

// update troca a mensagem do cartão já visível. Devolve false quando o cartão
// não está mais vivo (o chamador cria um novo).
func (b *sessionBalloon) update(message string) bool {
	b.mu.Lock()
	if b.closed || b.hwnd == 0 {
		b.mu.Unlock()
		return false
	}
	b.message = message
	b.since = time.Now()
	hwnd := b.hwnd
	select {
	case b.prolongar <- struct{}{}:
	default:
	}
	b.mu.Unlock()
	winui.Invalidate(hwnd)
	return true
}

// expireAfter fecha o cartão depois do tempo de exibição (recomeçando quando
// uma mensagem nova chega — update sinaliza prolongar). O timer do shell tinha
// o mesmo papel; aqui é do nosso lado porque a janela é nossa.
func (b *sessionBalloon) expireAfter(d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			b.requestClose()
			return
		case <-b.die:
			return
		case <-b.prolongar:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(d)
		}
	}
}

// requestClose pede a destruição na thread dona da janela (idempotente).
func (b *sessionBalloon) requestClose() {
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
	closeOnce(b.die)
	if hwnd != 0 {
		winui.PostMessage(hwnd, winui.MsgClose, 0, 0)
	}
}

// closeOnce fecha o canal de expiração uma única vez.
func closeOnce(ch chan struct{}) {
	defer func() { _ = recover() }()
	close(ch)
}

// trayBalloonPosition ancora o cartão ACIMA/à esquerda da âncora (cursor do
// usuário) e o mantém dentro da área de trabalho — nunca sobre a barra de
// tarefas, que é onde o ícone do agente (e o "Desconectar") está.
func trayBalloonPosition(width, height, anchorX, anchorY int, anchorOk bool) (int, int) {
	area := winui.WorkArea()
	if anchorOk {
		area = winui.WorkAreaForPoint(anchorX, anchorY)
	}
	x := anchorX - width + trayBalloonPadding
	y := anchorY - height - trayBalloonGap
	if !anchorOk {
		x = area.Right() - width - trayBalloonGap
		y = area.Bottom() - height - trayBalloonGap
	}
	if x < area.X {
		x = area.X
	}
	if x+width > area.Right() {
		x = area.Right() - width
	}
	if y < area.Y {
		// Sem espaço acima da âncora: cai abaixo dela (continua fora do ícone
		// quando possível, e sempre dentro da área de trabalho).
		y = anchorY + trayBalloonGap
	}
	if y+height > area.Bottom() {
		y = area.Bottom() - height
	}
	if y < area.Y {
		y = area.Y
	}
	return x, y
}

// trayBalloonWndProc trata as mensagens do cartão.
func sessionBalloonWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	balloonMutex.Lock()
	b := balloonAtual
	balloonMutex.Unlock()
	if b == nil || b.hwnd != hwnd {
		return winui.DefWindowProc(hwnd, msg, wparam, lparam)
	}

	switch msg {
	case winui.MsgEraseBkgnd:
		return 1
	case winui.MsgPaint:
		hdc, paint := winui.BeginPaint(hwnd)
		b.paint(hdc)
		winui.EndPaint(hwnd, &paint)
		return 0
	case winui.MsgPrintClient:
		b.paint(wparam)
		return 0
	case winui.MsgNCHitTest:
		// Click-through de verdade: o clique chega no ícone/atalho de baixo.
		return winui.HtTransparent
	case winui.MsgTimer:
		// Reforça o topo da ordem Z fora do WM_PAINT: chamar SetWindowPos durante
		// a pintura é efeito colateral desnecessário dentro do desenho.
		winui.EnsureTopMost(hwnd)
		winui.Invalidate(hwnd)
		return 0
	case winui.MsgSettingChange:
		// Tema trocado com o cartão na tela: relê a paleta e repinta.
		b.theme = bannerThemeLoader()
		winui.Invalidate(hwnd)
		return 0
	case winui.MsgClose:
		winui.DestroyWindow(hwnd)
		return 0
	case winui.MsgDestroy:
		unregisterBannerCapture(hwnd)
		b.fonts.release()
		b.mu.Lock()
		b.closed = true
		b.hwnd = 0
		b.mu.Unlock()
		balloonMutex.Lock()
		if balloonAtual == b {
			balloonAtual = nil
		}
		balloonMutex.Unlock()
		return 0
	}
	return winui.DefWindowProc(hwnd, msg, wparam, lparam)
}

// paint desenha o cartão (mesma linguagem visual do banner de sessão).
func (b *sessionBalloon) paint(screenDC uintptr) {
	scale := b.scale
	if scale <= 0 {
		scale = 1
	}
	width, height, radius := b.width, b.height, b.radius
	if width <= 0 {
		width = trayBalloonWidth
	}
	if height <= 0 {
		height = trayBalloonHeight
	}
	if radius <= 0 {
		radius = trayBalloonRadius
	}
	sw := func(value int) int { return scalePx(value, scale) }

	memDC, bitmap, oldBitmap := winui.BufferedPaint(screenDC, width, height)
	hdc := memDC
	if hdc == 0 {
		hdc = screenDC
	} else {
		defer winui.FlushBufferedPaint(screenDC, memDC, bitmap, oldBitmap, width, height)
	}

	theme := b.theme
	winui.FillRect(hdc, winui.Rect{W: width, H: height}, theme.Card)

	inset := sw(1)
	winui.StrokeRoundRectAA(hdc,
		winui.Rect{X: inset, Y: inset, W: width - 2*inset, H: height - 2*inset},
		radius-inset, sw(1), theme.Border)

	// Faixa de destaque: identifica o cartão como sendo do agente.
	margin := sw(10)
	winui.FillRoundRectAA(hdc,
		winui.Rect{X: sw(3), Y: margin, W: sw(4), H: height - 2*margin},
		sw(2), theme.Accent)
	winui.FillEllipseAA(hdc, winui.Rect{X: sw(24), Y: sw(26), W: sw(10), H: sw(10)}, theme.Accent)

	textLeft := sw(42)
	textWidth := width - textLeft - sw(trayBalloonPadding)

	b.mu.Lock()
	title := b.title
	message := b.message
	b.mu.Unlock()
	if title == "" {
		title = trayBalloonTitle
	}
	winui.DrawTextSingle(hdc, title,
		winui.Rect{X: textLeft, Y: sw(10), W: textWidth, H: sw(18)}, b.fonts.primary, theme.Text, winui.AlignLeft)
	winui.DrawTextSingle(hdc, message,
		winui.Rect{X: textLeft, Y: sw(32), W: textWidth, H: sw(34)}, b.fonts.secondary, theme.TextMuted, winui.AlignLeft)
}

// notifyShellBalloon é o caminho antigo (balloon do explorer). Fica como
// reserva: se a janela própria não puder ser criada o usuário ainda é avisado,
// mesmo que o aviso apareça também no stream.
func (t *SessionTray) notifyShellBalloon(message string) {
	t.notifyDoShell(message)
}

// EncerrarTrayBalloon fecha o aviso em tela (fim da sessão). Sem isso, uma
// sessão encerrada enquanto o cartão está visível deixaria uma janela órfã.
func EncerrarTrayBalloon() {
	balloonMutex.Lock()
	atual := balloonAtual
	balloonAtual = nil
	balloonMutex.Unlock()
	if atual != nil {
		atual.requestClose()
	}
}
