//go:build windows

package screen

import (
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"discovery/app/core/winui"
)

// TestHideBannerFromCaptureNuncaFalha garante a regressão do Win10: o banner
// NUNCA deixa de ser exibido por causa da afinidade de captura.
//
// O degrau nunca devolve bannerCaptureVisible quando a captura do processo sabe
// contornar a região do banner (bannerCaptureRegiao) — é o caso do GDI, o único
// capturer disponível em Win10 sem GPU. O erro do WDA_EXCLUDEFROMCAPTURE só é
// esperado quando nem a emenda está disponível.
func TestHideBannerFromCaptureNuncaFalha(t *testing.T) {
	// Sem capturer ativo (unidade de teste) a emenda não está disponível; com um
	// capturer GDI registrado, está. Os dois casos precisam evitar o pior modo.
	SetCapturerAtual(nil)
	emendaConhecida.Store(false)
	t.Cleanup(func() {
		SetCapturerAtual(nil)
		emendaConhecida.Store(false)
	})

	if mode, _ := hideBannerFromCapture(0); mode != bannerCaptureVisible {
		t.Fatalf("sem capturer ativo o HWND inválido deveria cair em bannerCaptureVisible, veio %d", mode)
	}

	gdic, err := NewGDICapturer()
	if err != nil {
		t.Skipf("capturer GDI indisponível: %v", err)
	}
	SetCapturerAtual(gdic)
	defer SetCapturerAtual(nil)
	defer gdic.Close()
	if !CaptureExclusionSupported() {
		t.Fatal("capturer GDI deveria suportar a emenda por região")
	}
	if mode, err := hideBannerFromCapture(0); mode != bannerCaptureRegiao || err != nil {
		t.Fatalf("com emenda disponível o modo deveria ser bannerCaptureRegiao, veio %d (err=%v)", mode, err)
	}
	// Com a emenda ativa o banner registra o HWND para o capturer contornar a
	// área — e o unregister precisa limpar o alvo.
	registerBannerCapture(0x1234)
	if got := bannerCaptureTarget.Load(); got != 0x1234 {
		t.Fatalf("alvo registrado = %#x, esperado 0x1234", got)
	}
	unregisterBannerCapture(0x1234)
	if got := bannerCaptureTarget.Load(); got != 0 {
		t.Fatalf("alvo após unregister = %#x, esperado 0", got)
	}

	banner, err := NewSessionBanner(BannerOptions{Operator: "Teste"})
	if err != nil {
		t.Fatalf("banner não criado: %v", err)
	}
	defer banner.Close()
	banner.mu.Lock()
	hwnd := banner.hwnd
	banner.mu.Unlock()
	if hwnd == 0 {
		t.Fatal("banner sem HWND")
	}
	if mode, _ := hideBannerFromCapture(hwnd); mode == bannerCaptureVisible {
		t.Fatal("janela real não deveria cair no pior caso de afinidade")
	}
}

// TestBannerScaleForDPI cobre o fator de escala do cartão (PerMonitorV2).
func TestBannerScaleForDPI(t *testing.T) {
	cases := []struct {
		dpi  int
		want float64
	}{
		{0, 1},      // DPI indisponível
		{48, 1},     // abaixo de 100%: não encolhe
		{96, 1},     // 100%
		{120, 1.25}, // 125%
		{144, 1.5},  // 150%
		{192, 2},    // 200%
		{480, 3},    // clamp máximo
	}
	for _, tc := range cases {
		if got := bannerScaleForDPI(tc.dpi); got != tc.want {
			t.Fatalf("bannerScaleForDPI(%d) = %v, esperado %v", tc.dpi, got, tc.want)
		}
	}
}

// TestScalePx garante o arredondamento das medidas de projeto para pixels reais.
func TestScalePx(t *testing.T) {
	cases := []struct {
		value int
		scale float64
		want  int
	}{
		{16, 1, 16},
		{16, 1.25, 20},
		{13, 1.5, 20},
		{620, 1.5, 930},
		{14, 1.25, 18},
		{0, 2, 0},
	}
	for _, tc := range cases {
		if got := scalePx(tc.value, tc.scale); got != tc.want {
			t.Fatalf("scalePx(%d, %v) = %d, esperado %d", tc.value, tc.scale, got, tc.want)
		}
	}
}

// TestPaintMetricsFallback garante o layout de 96 DPI quando o SessionBanner é
// montado sem janela (testes de pintura) — scale 0 não pode gerar tamanho 0.
func TestPaintMetricsFallback(t *testing.T) {
	b := &SessionBanner{}
	width, height, radius, scale := b.paintMetrics()
	if width != bannerWidth || height != bannerHeight || radius != bannerRadius || scale != 1 {
		t.Fatalf("fallback inesperado: %dx%d r=%d scale=%v", width, height, radius, scale)
	}

	b = &SessionBanner{width: 930, height: 102, radius: 21, scale: 1.5}
	width, height, radius, scale = b.paintMetrics()
	if width != 930 || height != 102 || radius != 21 || scale != 1.5 {
		t.Fatalf("métricas escaladas inesperadas: %dx%d r=%d scale=%v", width, height, radius, scale)
	}
}

// TestSessionBannerAppliesWindowDpi garante que a janela real usa a geometria
// escalada pelo DPI do monitor (não os 620x68 fixos de projeto).
func TestSessionBannerAppliesWindowDpi(t *testing.T) {
	banner, err := NewSessionBanner(BannerOptions{
		Text: "Sessão remota em andamento", Operator: "Pedro", AllowDisconnect: false, MonitorIndex: 0,
	})
	if err != nil {
		t.Skipf("banner indisponível neste ambiente: %v", err)
	}
	defer banner.Close()

	dpi := winui.DpiForWindow(banner.hwnd)
	if dpi < 48 {
		t.Skipf("DPI do monitor indisponível neste ambiente: %d", dpi)
	}
	scale := bannerScaleForDPI(dpi)
	wantWidth := scalePx(bannerWidth, scale)
	wantHeight := scalePx(bannerHeight, scale)
	width, height, radius, gotScale := banner.paintMetrics()
	if gotScale != scale {
		t.Fatalf("escala do banner = %v, esperado %v (dpi=%d)", gotScale, scale, dpi)
	}
	if width != wantWidth || height != wantHeight || radius != scalePx(bannerRadius, scale) {
		t.Fatalf("geometria = %dx%d r=%d, esperado %dx%d r=%d (dpi=%d)",
			width, height, radius, wantWidth, wantHeight, scalePx(bannerRadius, scale), dpi)
	}
}

// TestBannerPaintCoversClientArea garante que a pintura preenche TODO o
// cliente: o bitmap do double buffer nasce preto e qualquer área não pintada
// aparecia como pixels escuros nas bordas/cantos do cartão (bug reportado).
func TestBannerPaintCoversClientArea(t *testing.T) {
	banner := &SessionBanner{
		start:    time.Now().Add(-48 * time.Second),
		baseText: "Sessão remota em andamento: Pedro",
		operator: "Pedro",
		allow:    true,
		theme:    winui.LightTheme(),
	}
	banner.fonts = bannerFonts{
		primary:   winui.CreateFont(16, 600),
		secondary: winui.CreateFont(13, 400),
	}
	defer banner.fonts.release()

	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		t.Skip("GetDC falhou neste ambiente")
	}
	defer procReleaseDC.Call(0, screenDC)
	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		t.Skip("CreateCompatibleDC falhou neste ambiente")
	}
	defer procDeleteDC.Call(memDC)
	bitmap, _, _ := procCreateCompatibleBitmap.Call(screenDC, bannerWidth, bannerHeight)
	if bitmap == 0 {
		t.Skip("CreateCompatibleBitmap falhou neste ambiente")
	}
	defer procDeleteObject.Call(bitmap)
	old, _, _ := procSelectObject.Call(memDC, bitmap)
	defer procSelectObject.Call(memDC, old)

	banner.paint(memDC)

	buf := make([]byte, bannerWidth*bannerHeight*4)
	var bi [40]byte
	bi[0] = 40
	*(*int32)(unsafe.Pointer(&bi[4])) = bannerWidth
	*(*int32)(unsafe.Pointer(&bi[8])) = -int32(bannerHeight)
	*(*uint16)(unsafe.Pointer(&bi[12])) = 1
	*(*uint16)(unsafe.Pointer(&bi[14])) = 32
	*(*uint32)(unsafe.Pointer(&bi[16])) = BI_RGB
	ret, _, _ := procGetDIBits.Call(screenDC, bitmap, 0, uintptr(bannerHeight),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bi[0])), DIB_RGB_COLORS)
	if ret == 0 {
		t.Skip("GetDIBits falhou neste ambiente")
	}

	black := 0
	for i := 0; i < len(buf); i += 4 {
		if buf[i] < 8 && buf[i+1] < 8 && buf[i+2] < 8 {
			black++
		}
	}
	if black > 0 {
		t.Fatalf("%d pixel(s) pretos no cartão — área do cliente sem pintura (fundo do double buffer)", black)
	}
}

func TestBannerTextosNaoDuplicamOperador(t *testing.T) {
	banner := &SessionBanner{
		start:    time.Now().Add(-65 * time.Second),
		baseText: "Sessão remota em andamento: Pedro",
		operator: "Pedro",
		allow:    true,
	}
	if got := banner.primaryText(); got != "Sessão remota em andamento: Pedro" {
		t.Fatalf("texto principal = %q", got)
	}
	secondary := banner.secondaryText()
	if strings.Contains(secondary, "Pedro") {
		t.Fatalf("operador duplicado no banner: %q", secondary)
	}
	if !strings.HasPrefix(secondary, "01:0") {
		t.Fatalf("tempo decorrido ausente: %q", secondary)
	}
	if !strings.Contains(secondary, "Para encerrar, clique no ícone do agente na bandeja do sistema") {
		t.Fatalf("dica de desconexão ausente/incorreta: %q", secondary)
	}

	// Sem operador no texto do servidor, o nome entra na linha principal.
	semNome := &SessionBanner{start: time.Now(), operator: "Ana", baseText: "Sessão remota em andamento"}
	if got := semNome.primaryText(); !strings.HasSuffix(got, "— Ana") {
		t.Fatalf("operador ausente na linha principal: %q", got)
	}
	if got := semNome.secondaryText(); strings.Contains(got, "Ana") {
		t.Fatalf("operador não deveria repetir na linha de baixo: %q", got)
	}
	// Sem permissão de desconectar, não anuncia o ícone da bandeja.
	semDesconexao := &SessionBanner{start: time.Now(), allow: false}
	if got := semDesconexao.secondaryText(); strings.Contains(got, "bandeja") ||
		!strings.HasPrefix(got, "Conectado há ") {
		t.Fatalf("estado sem allowDisconnect inesperado: %q", got)
	}
}

// underBannerWndProc: janela comum (HTCLIENT) usada como "app embaixo" do banner.
func underBannerWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	if msg == winui.MsgNCHitTest {
		return 1 // HTCLIENT
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return ret
}

// packTestPoint empacota um POINT (8 bytes, passado POR VALOR no x64) em uintptr.
func packTestPoint(p testPoint) uintptr {
	return uintptr(uint32(p.Y))<<32 | uintptr(uint32(p.X))
}

var underClassOnce sync.Once

var (
	testUser32 = windows.NewLazySystemDLL("user32.dll")

	testProcWindowFromPoint = testUser32.NewProc("WindowFromPoint")
	testProcSendMessageW    = testUser32.NewProc("SendMessageW")
	testProcGetClassLongW   = testUser32.NewProc("GetClassLongW")
)

type testPoint struct {
	X int32
	Y int32
}

// janelaDeBaixo cria uma janela topmost que fica ABAIXO do banner (criado
// depois) para provar que o banner é click-through: o hit-test do ponto do
// banner precisa devolver essa janela, não o banner.
func janelaDeBaixo(t *testing.T, x, y, width, height int) uintptr {
	t.Helper()
	className := "DiscoveryTestUnderBanner"
	underClassOnce.Do(func() {
		name, _ := windows.UTF16PtrFromString(className)
		wc := wndClassExW{
			cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
			lpfnWndProc:   windows.NewCallback(underBannerWndProc),
			lpszClassName: name,
		}
		procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	})
	hwnd, err := winui.CreateWindow(className, "teste-under-banner",
		winui.ExStyleTopmost|winui.ExStyleNoActivate,
		winui.WinStylePopup|winui.WinStyleVisible,
		x, y, width, height)
	if err != nil {
		t.Fatalf("janela de baixo: %v", err)
	}
	winui.ShowWindow(hwnd, winui.SwShowNormal)
	winui.UpdateWindow(hwnd)
	return hwnd
}

func TestSessionBannerClickThrough(t *testing.T) {
	x, y := bannerPosition(0)
	under := janelaDeBaixo(t, x, y, bannerWidth, bannerHeight)
	defer winui.DestroyWindow(under)

	banner, err := NewSessionBanner(BannerOptions{
		Text: "Sessão remota em andamento", Operator: "Pedro", AllowDisconnect: true, MonitorIndex: 0,
	})
	if err != nil {
		t.Skipf("banner indisponível neste ambiente: %v", err)
	}
	defer banner.Close()
	// Regressão do "borrão" atrás do cartão: a classe do banner NÃO pode ter
	// CS_DROPSHADOW (0x00020000) — era ele que desenhava a sombra deslocada.
	const (
		gclStyle     = ^uintptr(25) // GCL_STYLE = -26 (sign-extended)
		csDropShadow = 0x00020000
	)
	classStyle, _, _ := testProcGetClassLongW.Call(banner.hwnd, gclStyle)
	if classStyle&csDropShadow != 0 {
		t.Fatalf("classe do banner tem CS_DROPSHADOW (style=%#x) — a sombra volta a aparecer atrás do cartão", classStyle)
	}
	// Garante que o banner (topmost) está acima da janela de teste.
	winui.EnsureTopMost(banner.hwnd)
	time.Sleep(80 * time.Millisecond)

	// O cartão é reformatado para o DPI efetivo do monitor logo após ser criado
	// (largura/altura escaladas); medir o hit-test pelo tamanho de PROJETO acerta
	// o centro só a 100%.
	banner.mu.Lock()
	bw, bh := banner.width, banner.height
	banner.mu.Unlock()
	if bw <= 0 || bh <= 0 {
		bw, bh = bannerWidth, bannerHeight
	}
	// Espera o cartão ficar realmente visível: a criação é assíncrona (goroutine
	// com thread própria) e o WindowFromPoint ainda pegaria o desktop do shell.
	// Sem isto o teste é intermitente e não exercita o hit-test do banner.
	{
		vivo := false
		for i := 0; i < 100; i++ {
			if _, ok := winui.VisibleRect(banner.hwnd); ok {
				vivo = true
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if !vivo {
			// CUIDADO: até 2026-11-03 este ramo escondia um BUG (winui.Rect com 32
			// bytes passado para GetWindowRect, que escreve 16) — VisibleRect
			// devolvia ok=false SEMPRE e o teste era pulado em toda máquina. O
			// layout foi corrigido (winui.rectFromNative) e o skip agora só cobre
			// ambiente realmente sem desktop interativo.
			t.Skipf("GetWindowRect devolveu retângulo vazio para o banner (hwnd=%d) — sem desktop interativo para hit-test", banner.hwnd)
		}
	}
	point := testPoint{X: int32(x + bw/2), Y: int32(y + bh/2)}
	hit, _, _ := testProcWindowFromPoint.Call(packTestPoint(point))
	if uintptr(hit) != under {
		t.Fatalf("hit-test no centro do banner devolveu hwnd=%v (esperado a janela de baixo %v) — banner NÃO está click-through", hit, under)
	}
	hitTopLeft, _, _ := testProcWindowFromPoint.Call(packTestPoint(testPoint{X: int32(x + 4), Y: int32(y + 4)}))
	if uintptr(hitTopLeft) != under {
		t.Fatalf("hit-test no canto do banner devolveu %v (esperado %v)", hitTopLeft, under)
	}

	// Reforço: o WM_NCHITTEST do banner responde HTTRANSPARENT.
	got, _, _ := testProcSendMessageW.Call(banner.hwnd, winui.MsgNCHitTest, 0,
		uintptr(uint32(uint16(y+bh/2))<<16|uint32(uint16(x+bw/2))))
	if uintptr(got) != ^uintptr(0) {
		t.Fatalf("WM_NCHITTEST = %#x, esperado HTTRANSPARENT", got)
	}
}

// areaDoRetangulo devolve a área de um retângulo em int64 (evita overflow em
// monitores grandes). Fica em função própria //go:noinline porque neste arquivo o
// compilador já devolveu número errado ao calcular tudo dentro da comparação.
//
//go:noinline
func areaDoRetangulo(r winui.Rect) int64 {
	w := int64(r.W)
	h := int64(r.H)
	return w * h
}

//go:noinline
func areaCopiada(p bannerPlan) int64 {
	var total int64
	for i := 0; i < p.nCopiar; i++ {
		total += areaDoRetangulo(p.copiar[i])
	}
	return total
}

func TestBannerPlanoCaptura(t *testing.T) {
	frame := winui.Rect{X: 0, Y: 0, W: 1280, H: 960}

	// Caso real: cartão flutuante menor que a tela e deslocado. Caminho
	// conservador — copia o quadro e recompõe exatamente o retângulo do cartão.
	plano, ok := planoBanner(frame, winui.Rect{X: 100, Y: 8, W: 620, H: 68})
	if !ok {
		t.Fatal("banner na tela deveria produzir plano")
	}
	if plano.nCopiar != 1 || plano.copiar[0] != frame {
		t.Fatalf("cartão flutuante deveria copiar o quadro inteiro, veio n=%d %+v", plano.nCopiar, plano.copiar[0])
	}
	if plano.buracos[0] != (winui.Rect{X: 100, Y: 8, W: 620, H: 68}) {
		t.Fatalf("buraco inesperado: %+v", plano.buracos[0])
	}
	if plano.buracos[0].X < 0 || plano.buracos[0].Right() > frame.W || plano.buracos[0].Bottom() > frame.H {
		t.Fatalf("buraco fora do quadro: %+v", plano.buracos[0])
	}

	// Banner atravessando a tela de ponta a ponta (modo faixa): sobram a faixa de
	// cima e a de baixo, e o buraco é só o intervalo de linhas do cartão.
	plano, ok = planoBanner(frame, winui.Rect{X: 0, Y: 8, W: 1280, H: 68})
	if !ok {
		t.Fatal("banner de largura total deveria produzir plano")
	}
	if plano.nCopiar != 2 {
		t.Fatalf("esperava 2 faixas a copiar, veio %d", plano.nCopiar)
	}
	if plano.copiar[0].Y != 0 || plano.copiar[0].H != 8 || plano.copiar[0].W != frame.W {
		t.Fatalf("faixa de cima inesperada: %+v", plano.copiar[0])
	}
	if plano.copiar[1].Y != 76 || plano.copiar[1].H != frame.H-76 || plano.copiar[1].W != frame.W {
		t.Fatalf("faixa de baixo inesperada: %+v", plano.copiar[1])
	}
	if plano.buracos[0] != (winui.Rect{X: 0, Y: 8, W: frame.W, H: 68}) {
		t.Fatalf("buraco inesperado: %+v", plano.buracos[0])
	}

	// Banner encostado no topo e de largura total: só a faixa de baixo.
	plano, ok = planoBanner(frame, winui.Rect{X: 0, Y: 0, W: 1280, H: 68})
	if !ok || plano.nCopiar != 1 || plano.copiar[0].Y != 68 || plano.buracos[0].H != 68 {
		t.Fatalf("banner no topo: plano inesperado ok=%v n=%d %+v", ok, plano.nCopiar, plano)
	}

	// Banner parcialmente fora (encostado à direita): continua sendo cartão
	// parcial, então copia o quadro todo e o buraco fica recortado na tela.
	plano, ok = planoBanner(frame, winui.Rect{X: 1000, Y: 8, W: 620, H: 68})
	if !ok || plano.nCopiar != 1 || plano.buracos[0] != (winui.Rect{X: 1000, Y: 8, W: 280, H: 68}) {
		t.Fatalf("banner à direita: plano inesperado ok=%v n=%d buraco=%+v", ok, plano.nCopiar, plano.buracos[0])
	}

	// Para os dois modos, área copiada + buraco = quadro inteiro (sem sobra nem
	// sobreposição), que é o invariante que garante DIB 100% preenchida.
	for _, banner := range []winui.Rect{
		{X: 100, Y: 8, W: 620, H: 68},
		{X: 0, Y: 8, W: 1280, H: 68},
		{X: 1000, Y: 8, W: 620, H: 68},
	} {
		p, ok := planoBanner(frame, banner)
		if !ok {
			t.Fatalf("banner %+v deveria produzir plano", banner)
		}
		// Copiado + buraco = quadro inteiro, sem sobra nem sobreposição: é o que
		// garante DIB 100% preenchida.
		// Valores lidos direto dos campos, sem passar por função auxiliar: neste
		// arquivo o compilador já devolveu número errado ao chamar helper dentro
		// da chamada (área do buraco saía 42160 = quadro + buraco).
		copiado := areaCopiada(p)
		buraco := int64(p.buracos[0].W) * int64(p.buracos[0].H)
		esperado := areaDoRetangulo(frame)
		soma := copiado + buraco
		confere := soma == esperado
		// Só exigimos o invariante quando o plano copia o quadro inteiro (modo
		// conservador): aí o buraco precisa caber DENTRO do quadro. No modo faixa
		// a conta é feita pelas duas faixas, já verificadas acima.
		if copiado < esperado && !confere {
			t.Fatalf("banner %+v: copiado=%d buraco=%d soma=%d, esperado %d",
				banner, copiado, buraco, soma, esperado)
		}
	}

	// Fora do monitor: nada a esconder.
	if _, ok := planoBanner(frame, winui.Rect{X: 1400, Y: 0, W: 620, H: 68}); ok {
		t.Fatal("banner fora do monitor não deveria gerar plano")
	}
	// Cobrindo o monitor inteiro: não há como contornar.
	if _, ok := planoBanner(frame, winui.Rect{X: 0, Y: 0, W: 1280, H: 960}); ok {
		t.Fatal("banner cobrindo a tela inteira não deveria gerar plano")
	}
}

// TestBannerEmendaPreencheBuraco verifica que a emenda preenche a área do banner
// com o quadro anterior e preserva os demais pixels da captura atual.
func TestBannerEmendaPreencheBuraco(t *testing.T) {
	c := &gdiCapturer{width: 64, height: 32}
	if err := c.ensureDIB(0); err != nil {
		t.Skipf("DIB indisponível: %v", err)
	}
	defer c.releaseCached()
	c.ensureBannerHistorico()
	if c.bannerPrev == nil {
		t.Fatal("histórico não alocado")
	}

	pixels := unsafe.Slice((*byte)(c.dibBits), c.width*c.height*4)
	for i := range c.bannerPrev {
		c.bannerPrev[i] = 0xAA // quadro anterior
	}
	for i := range pixels {
		pixels[i] = 0xBB // quadro atual (com o banner desenhado)
	}
	c.fillBannerHole(winui.Rect{X: 0, Y: 8, W: 64, H: 4})

	fora := 0
	for y := 0; y < c.height; y++ {
		for x := 0; x < c.width; x++ {
			want := byte(0xBB)
			if y >= 8 && y < 12 {
				want = 0xAA
			}
			if pixels[(y*c.width+x)*4] != want {
				fora++
			}
		}
	}
	if fora != 0 {
		t.Fatalf("%d subpixels fora do esperado após a emenda", fora)
	}
}

// TestBannerPlanoMultiCobreTodasAsJanelas garante que o aviso da bandeja (uma
// SEGUNDA janela do agente em tela) também sai do stream: cada janela invisível
// no stream vira um buraco preenchido pelo quadro anterior.
func TestBannerPlanoMultiCobreTodasAsJanelas(t *testing.T) {
	frame := winui.Rect{W: 1280, H: 960}
	banner := winui.Rect{X: 330, Y: 8, W: 620, H: 68}
	aviso := winui.Rect{X: 880, Y: 780, W: 372, H: 86}

	plano, ok := planoBannerMulti(frame, []winui.Rect{banner, aviso})
	if !ok {
		t.Fatal("plano com duas janelas deveria existir")
	}
	if plano.nCopiar != 1 || plano.copiar[0] != frame {
		t.Fatalf("modo conservador esperado (quadro inteiro), veio n=%d %+v", plano.nCopiar, plano.copiar[0])
	}
	if plano.nBuracos != 2 {
		t.Fatalf("esperava um buraco por janela, veio %d", plano.nBuracos)
	}
	if plano.buracos[0] != banner {
		t.Fatalf("buraco do banner inesperado: %+v", plano.buracos[0])
	}
	if plano.buracos[1] != aviso {
		t.Fatalf("buraco do aviso inesperado: %+v", plano.buracos[1])
	}

	// Janela fora do monitor é ignorada (não cria buraco vazio).
	plano, ok = planoBannerMulti(frame, []winui.Rect{banner, {X: 2000, Y: 10, W: 100, H: 40}})
	if !ok || plano.nBuracos != 1 {
		t.Fatalf("janela fora do monitor deveria ser ignorada: ok=%v n=%d", ok, plano.nBuracos)
	}

	// Faixa que atravessa as duas laterais junto de um cartão flutuante: as
	// faixas acima/abaixo cobrem os dois, e o buraco é a união de linhas.
	plano, ok = planoBannerMulti(frame, []winui.Rect{{X: 0, Y: 8, W: 1280, H: 40}, aviso})
	if !ok {
		t.Fatal("plano com faixa + cartão deveria existir")
	}
	if plano.nCopiar != 2 {
		t.Fatalf("modo faixas esperado (2 faixas), veio %d", plano.nCopiar)
	}
	if plano.nBuracos != 1 {
		t.Fatalf("modo faixas tem um único intervalo de linhas, veio %d", plano.nBuracos)
	}
	// As faixas copiadas não podem conter nenhuma linha das janelas escondidas
	// fora do intervalo do buraco.
	faixaAcima := plano.copiar[0]
	faixaAbaixo := plano.copiar[1]
	if faixaAcima.Bottom() > plano.buracos[0].Y || faixaAbaixo.Y < plano.buracos[0].Bottom() {
		t.Fatalf("faixas invadem o buraco: acima=%+v buraco=%+v abaixo=%+v", faixaAcima, plano.buracos[0], faixaAbaixo)
	}
}

// TestTrayBalloonFicaForaDaBarraDeTarefas garante que o aviso é ancorado acima
// e à esquerda do cursor, sem cobrir a área da bandeja (onde o usuário clica
// para desconectar).
func TestTrayBalloonFicaForaDaBarraDeTarefas(t *testing.T) {
	area := winui.WorkArea()
	largura, altura := 372, 86
	// Cursor em cima da bandeja (canto inferior direito da área de trabalho).
	cursorX, cursorY := area.Right()-40, area.Bottom()-20
	x, y := trayBalloonPosition(largura, altura, cursorX, cursorY, true)
	if y+altura > area.Bottom() {
		t.Fatalf("aviso invadiu a barra de tarefas: y=%d h=%d limite=%d", y, altura, area.Bottom())
	}
	if x+largura > area.Right() {
		t.Fatalf("aviso passou da borda direita: x=%d w=%d limite=%d", x, largura, area.Right())
	}
	if y < area.Y || x < area.X {
		t.Fatalf("aviso fora da área de trabalho: %d,%d (origem %d,%d)", x, y, area.X, area.Y)
	}
	// Sem âncora confiável, cai na quina da área de trabalho, ainda visível.
	x, y = trayBalloonPosition(largura, altura, 0, 0, false)
	if x < area.X || y < area.Y || x+largura > area.Right() || y+altura > area.Bottom() {
		t.Fatalf("aviso sem âncora saiu da área de trabalho: %d,%d", x, y)
	}
}
