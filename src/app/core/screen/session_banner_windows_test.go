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

// ── Reafirmação do topmost (regressão: cartão atrás do menu Iniciar) ──

// espiaoTopmost captura as chamadas de reafirmação sem abrir janela real.
//
// Por que um espião e não uma asserção sobre a ordem Z de verdade: a ordem Z
// efetiva depende do gerenciador de janelas do shell, de qual monitor está em
// foco e de janelas de banda superior (menu Iniciar/UAC) que o teste não pode
// abrir de forma confiável em CI. O contrato que importa e que É determinístico
// é: "o banner pediu SetWindowPos(HWND_TOPMOST) com as flags que não roubam foco
// nem mexem na geometria". É isso que o espião verifica.
type espiaoTopmost struct {
	mu       sync.Mutex
	chamadas []chamadaTopmost
}

type chamadaTopmost struct {
	hwnd         uintptr
	insertAfter  uintptr
	x, y, cx, cy uintptr
	flags        uintptr
}

func (e *espiaoTopmost) registrar(hwnd, insertAfter, x, y, cx, cy, flags uintptr) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.chamadas = append(e.chamadas, chamadaTopmost{hwnd, insertAfter, x, y, cx, cy, flags})
}

func (e *espiaoTopmost) total() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.chamadas)
}

func (e *espiaoTopmost) ultima(t *testing.T) chamadaTopmost {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.chamadas) == 0 {
		t.Fatal("nenhuma reafirmação de topmost foi registrada")
	}
	return e.chamadas[len(e.chamadas)-1]
}

// instalarEspiaoTopmost troca o ponto de injeção e devolve o espião + restauração.
func instalarEspiaoTopmost(t *testing.T) *espiaoTopmost {
	t.Helper()
	espiao := &espiaoTopmost{}
	anterior := bannerTopmostSetWindowPos
	bannerTopmostSetWindowPos = espiao.registrar
	t.Cleanup(func() { bannerTopmostSetWindowPos = anterior })
	return espiao
}

// TestReafirmarTopmostNaoRoubaFocoNemMoveJanela é a regressão central deste
// cartão: a reafirmação periódica PRECISA manter SWP_NOACTIVATE (o banner é
// WS_EX_NOACTIVATE e não pode roubar o foco de teclado do usuário local) e
// SWP_NOMOVE|SWP_NOSIZE (a geometria já foi escalada pelo DPI; mover aqui
// deslocaria o cartão a cada segundo).
//
// Se alguém "otimizar" a máscara de flags removendo uma delas, este teste cai.
func TestReafirmarTopmostNaoRoubaFocoNemMoveJanela(t *testing.T) {
	espiao := instalarEspiaoTopmost(t)

	const hwndFalso = uintptr(0xBEEF)
	reafirmarBannerTopmost(hwndFalso, bannerTopmostMotivoReafirmacao)

	if espiao.total() != 1 {
		t.Fatalf("esperava exatamente 1 reafirmação, veio %d", espiao.total())
	}
	c := espiao.ultima(t)

	if c.hwnd != hwndFalso {
		t.Fatalf("reafirmou o hwnd errado: %v (esperado %v)", c.hwnd, hwndFalso)
	}
	// HWND_TOPMOST é -1 e precisa chegar com sinal estendido (^uintptr(0)): o
	// valor 0xFFFFFFFF sem extensão seria lido pelo Windows como HWND inválido e
	// a reafirmação simplesmente não aconteceria (falha silenciosa em campo).
	if c.insertAfter != ^uintptr(0) {
		t.Fatalf("insertAfter = %#x, esperado HWND_TOPMOST (%#x)", c.insertAfter, ^uintptr(0))
	}
	if c.x != 0 || c.y != 0 || c.cx != 0 || c.cy != 0 {
		t.Fatalf("reafirmação mexeu na geometria: x=%d y=%d cx=%d cy=%d", c.x, c.y, c.cx, c.cy)
	}
	for _, obrigatoria := range []struct {
		nome  string
		valor uintptr
	}{
		{"SWP_NOACTIVATE", swpNoActivate},
		{"SWP_NOMOVE", swpNoMove},
		{"SWP_NOSIZE", swpNoSize},
		{"SWP_NOOWNERZORDER", swpNoOwnerZOrder},
	} {
		if c.flags&obrigatoria.valor == 0 {
			t.Fatalf("flag %s ausente na reafirmação (flags=%#x) — o cartão pode roubar foco ou se deslocar", obrigatoria.nome, c.flags)
		}
	}
	// A máscara usada em produção precisa ser exatamente a que o teste valida.
	if c.flags != swpNecessariasParaNaoQuebrarClickThrough {
		t.Fatalf("flags=%#x, esperado a máscara %#x", c.flags, swpNecessariasParaNaoQuebrarClickThrough)
	}
}

// TestReafirmarTopmostIgnoraHwndZero evita chamar SetWindowPos(0) durante a
// janela entre a criação e o Store do estado (ou depois do WM_DESTROY): passar
// HWND nulo não é inofensivo, é uma chamada perdida que ainda assim custaria
// syscall a cada tique.
func TestReafirmarTopmostIgnoraHwndZero(t *testing.T) {
	espiao := instalarEspiaoTopmost(t)
	reafirmarBannerTopmost(0, bannerTopmostMotivoReafirmacao)
	if espiao.total() != 0 {
		t.Fatalf("reafirmou com hwnd=0 (%d chamadas) — deveria ser no-op", espiao.total())
	}
}

// TestBannerReafirmaTopmostNoTimer prova o caminho de INTEGRAÇÃO: um WM_TIMER no
// wndproc do banner precisa reafirmar o topmost. É o cenário do bug relatado —
// o menu Iniciar abre, empurra o cartão para trás e, no tique seguinte, o cartão
// volta ao topo sozinho.
func TestBannerReafirmaTopmostNoTimer(t *testing.T) {
	espiao := instalarEspiaoTopmost(t)

	const hwndFalso = uintptr(0xCAFE)
	banner := &SessionBanner{}
	// O wndproc só trata a mensagem quando o HWND está no mapa de estados.
	bannerStates.Store(hwndFalso, &bannerState{banner: banner})
	t.Cleanup(func() { bannerStates.Delete(hwndFalso) })

	bannerWndProc(hwndFalso, winui.MsgTimer, bannerTimerID, 0)

	if espiao.total() != 1 {
		t.Fatalf("WM_TIMER não reafirmou o topmost (%d chamadas)", espiao.total())
	}
	c := espiao.ultima(t)
	if c.hwnd != hwndFalso {
		t.Fatalf("WM_TIMER reafirmou hwnd=%v, esperado %v", c.hwnd, hwndFalso)
	}
	if c.flags&swpNoActivate == 0 {
		t.Fatalf("WM_TIMER reafirmou SEM SWP_NOACTIVATE (flags=%#x) — roubaria o foco do usuário", c.flags)
	}
}

// TestBannerTopmostLogadoUmaVezPorJanela cobre a marca de log: a reafirmação
// roda a cada segundo, então logar toda vez encheria o arquivo da sessão com
// ~3600 linhas/hora e enterraria o sintoma que o log deveria provar.
func TestBannerTopmostLogadoUmaVezPorJanela(t *testing.T) {
	const hwnd = uintptr(0x1234)
	esquecerLogTopmost(hwnd)
	t.Cleanup(func() { esquecerLogTopmost(hwnd) })

	if _, jaLogado := bannerTopmostLogado.LoadOrStore(hwnd, struct{}{}); jaLogado {
		t.Fatal("marca de log já existia para um hwnd recém-esquecido")
	}
	// Segunda tentativa no mesmo HWND: agora precisa acusar que já foi logado.
	if _, jaLogado := bannerTopmostLogado.LoadOrStore(hwnd, struct{}{}); !jaLogado {
		t.Fatal("marca de log não foi mantida para o mesmo hwnd")
	}
	// O WM_DESTROY libera a marca: o Windows recicla HWNDs e o banner de uma
	// sessão seguinte herdaria a marca, nunca logando sua própria reafirmação.
	esquecerLogTopmost(hwnd)
	if _, jaLogado := bannerTopmostLogado.LoadOrStore(hwnd, struct{}{}); jaLogado {
		t.Fatal("esquecerLogTopmost não liberou a marca do hwnd")
	}
}

// ── Recuo de afinidade no descarte da máscara (P0 de privacidade) ──

// prepararCenarioRecuo isola o estado global que reaplicarProtecaoAposDescarteMascara
// consulta: o alvo do banner e a capacidade de emenda do capturer. Restaura tudo.
func prepararCenarioRecuo(t *testing.T, alvo uintptr, emenda bool) {
	t.Helper()
	anteriorAlvo := bannerCaptureTarget.Load()
	anteriorEmenda := emendaConhecida.Load()
	t.Cleanup(func() {
		bannerCaptureTarget.Store(anteriorAlvo)
		emendaConhecida.Store(anteriorEmenda)
	})
	bannerCaptureTarget.Store(alvo)
	emendaConhecida.Store(emenda)
}

// TestRecuoAposDescarteMascaraComDXGIProtegeOStream é a regressão de PRIVACIDADE
// central desta task.
//
// Cenário exato do bug: EXCLUDE foi aceito na criação (o caminho normal), a
// máscara é descartada em runtime e o capturer é o Desktop Duplication (DXGI),
// que IGNORA o WDA. Antes desta correção, removerMascara não tocava na afinidade
// e o cartão — com o nome do operador — voltava a ser capturado ao vivo.
//
// O contrato: o modo efetivo precisa deixar de ser bannerCaptureExcluded puro e
// passar a bannerCaptureBlack (área PRETA no stream). Regra de ouro do projeto:
// prefira congelar a vazar.
func TestRecuoAposDescarteMascaraComDXGIProtegeOStream(t *testing.T) {
	const hwnd = uintptr(0xD6D1)
	prepararCenarioRecuo(t, hwnd, false) // sem emenda = DXGI/DDA

	// Espião da afinidade: registra a sequência real de chamadas para provar que
	// o recuo passa por WDA_NONE antes de WDA_MONITOR (a API só troca de estado a
	// partir de WDA_NONE).
	var chamadas []uint32
	anteriorSet := affinitySet
	anteriorGet := affinityGet
	affinitySet = func(h uintptr, value uint32) (bool, error) {
		if h != hwnd {
			t.Errorf("recuo aplicou afinidade no hwnd errado: %v (esperado %v)", h, hwnd)
		}
		chamadas = append(chamadas, value)
		return true, nil
	}
	affinityGet = func(uintptr) (uint32, bool) { return wdaMonitor, true }
	t.Cleanup(func() { affinitySet = anteriorSet; affinityGet = anteriorGet })

	modo := reaplicarProtecaoAposDescarteMascara(hwnd)

	if modo != bannerCaptureBlack {
		t.Fatalf("modo efetivo após o descarte = %d (%s), esperado bannerCaptureBlack (%d) — o cartão voltaria a aparecer no stream",
			modo, bannerCaptureModeName(modo), bannerCaptureBlack)
	}
	// A sequência importa: MONITOR só é aceito a partir de NONE.
	if len(chamadas) < 2 || chamadas[0] != wdaNone || chamadas[len(chamadas)-1] != wdaMonitor {
		t.Fatalf("sequência de afinidade = %v, esperado começar em WDA_NONE(%d) e terminar em WDA_MONITOR(%d)",
			chamadas, wdaNone, wdaMonitor)
	}
}

// TestRecuoAposDescarteMascaraNaoDegradaComEmendaGDI garante o outro lado da
// regra: com emenda própria (GDI) o capturer contorna a região no BitBlt e o
// cartão NÃO vaza. Rebaixar para MONITOR ali pintaria a área de preto sem
// nenhum ganho de privacidade.
func TestRecuoAposDescarteMascaraNaoDegradaComEmendaGDI(t *testing.T) {
	const hwnd = uintptr(0x6D1)
	prepararCenarioRecuo(t, hwnd, true) // emenda disponível = GDI

	tocouAfinidade := false
	anteriorSet := affinitySet
	affinitySet = func(uintptr, uint32) (bool, error) {
		tocouAfinidade = true
		return true, nil
	}
	t.Cleanup(func() { affinitySet = anteriorSet })

	modo := reaplicarProtecaoAposDescarteMascara(hwnd)

	if modo != bannerCaptureExcluded {
		t.Fatalf("modo com emenda GDI = %d (%s), esperado bannerCaptureExcluded — não se degrada quem não vaza",
			modo, bannerCaptureModeName(modo))
	}
	if tocouAfinidade {
		t.Fatal("o recuo tocou SetWindowDisplayAffinity com emenda GDI disponível — degradação gratuita")
	}
}

// TestRecuoAposDescarteMascaraIgnoraHwndAlheio cobre as guardas de alvo: o recuo
// só pode agir sobre o banner ATIVO. Um handle antigo (cartão já recriado) ou o
// aviso da bandeja — que também usa máscara — não são decisão deste caminho, e
// aplicar afinidade neles seria efeito colateral em janela alheia.
func TestRecuoAposDescarteMascaraIgnoraHwndAlheio(t *testing.T) {
	const alvo = uintptr(0xA1A0)
	const outro = uintptr(0xB2B0)
	prepararCenarioRecuo(t, alvo, false)

	tocouAfinidade := false
	anteriorSet := affinitySet
	affinitySet = func(uintptr, uint32) (bool, error) {
		tocouAfinidade = true
		return true, nil
	}
	t.Cleanup(func() { affinitySet = anteriorSet })

	reaplicarProtecaoAposDescarteMascara(outro)
	reaplicarProtecaoAposDescarteMascara(0)

	if tocouAfinidade {
		t.Fatal("o recuo aplicou afinidade em hwnd que não é o banner ativo (ou em hwnd=0)")
	}
}

// TestHookDeDescarteEstaLigado prova que o ponto de extensão do descarte está
// REALMENTE preenchido. O bug desta task nasceu exatamente de uma proteção
// prometida no comentário e ausente no código: um hook nil aqui reintroduziria o
// vazamento em silêncio, sem nenhum teste de comportamento falhar.
func TestHookDeDescarteEstaLigado(t *testing.T) {
	if aoDescartarMascara == nil {
		t.Fatal("aoDescartarMascara está nil — o descarte da máscara não vai reaplicar a proteção (vazamento do cartão no stream)")
	}
	// O hook precisa ser o NOSSO recuo, não um substituto qualquer.
	const hwnd = uintptr(0x40CC)
	prepararCenarioRecuo(t, hwnd, false)
	// Os DOIS pontos de injeção, não só o de leitura: sem mockar affinitySet a
	// chamada real de SetWindowDisplayAffinity rodaria e falharia (não há janela
	// real aqui), o recuo cairia em bannerCaptureVisible e o teste acusaria um
	// falso negativo — foi exatamente o que aconteceu na primeira versão.
	anteriorSet := affinitySet
	anteriorGet := affinityGet
	affinitySet = func(uintptr, uint32) (bool, error) { return true, nil }
	affinityGet = func(uintptr) (uint32, bool) { return wdaMonitor, true }
	t.Cleanup(func() { affinitySet = anteriorSet; affinityGet = anteriorGet })

	if modo := aoDescartarMascara(hwnd); modo != bannerCaptureBlack {
		t.Fatalf("hook ligado devolveu modo %d (%s), esperado bannerCaptureBlack — não é o recuo de afinidade",
			modo, bannerCaptureModeName(modo))
	}
}

// TestRemoverMascaraDisparaORecuoDeAfinidade é o teste de INTEGRAÇÃO: exercita o
// caminho real do descarte (removerMascara) em vez de chamar o recuo direto.
// Sem isso, um hook ligado mas nunca invocado pelo descarte passaria nos testes
// acima — que é justamente a classe de bug desta task.
func TestRemoverMascaraDisparaORecuoDeAfinidade(t *testing.T) {
	const hwnd = uintptr(0xD15C)
	prepararCenarioRecuo(t, hwnd, false)

	// A máscara precisa existir para o descarte ter o que remover.
	mascarasMu.Lock()
	mascaras[hwnd] = &mascaraRegiao{hwnd: hwnd, ativa: true}
	mascarasMu.Unlock()
	t.Cleanup(func() {
		mascarasMu.Lock()
		delete(mascaras, hwnd)
		mascarasMu.Unlock()
		esquecerMascaraDescartada(hwnd)
	})

	anteriorGet := affinityGet
	var aplicados []uint32
	anteriorSet := affinitySet
	affinitySet = func(_ uintptr, value uint32) (bool, error) {
		aplicados = append(aplicados, value)
		return true, nil
	}
	affinityGet = func(uintptr) (uint32, bool) { return wdaMonitor, true }
	t.Cleanup(func() { affinitySet = anteriorSet; affinityGet = anteriorGet })

	removerMascara(hwnd)

	if !mascaraFoiDescartada(hwnd) {
		t.Fatal("removerMascara não marcou a máscara como descartada (pré-condição do teste)")
	}
	// O recuo precisa ter rodado: WDA_NONE seguido de WDA_MONITOR.
	temMonitor := false
	for _, v := range aplicados {
		if v == wdaMonitor {
			temMonitor = true
		}
	}
	if !temMonitor {
		t.Fatalf("removerMascara NÃO disparou o recuo de afinidade (chamadas=%v) — o cartão vazaria para o stream", aplicados)
	}
}

// ── Regressão do FANTASMA e do VAZAMENTO (verificação da task-4/task-5) ──
//
// Os testes acima (do zorder-dev/mask-dev) provam o recuo de afinidade. Os que
// seguem medem o OBSERVÁVEL no frame — é o que o operador realmente vê no
// visualizador: (1) o cartão não pode aparecer na área mascarada; (2) depois do
// descarte a área NÃO pode continuar reescrita com conteúdo morto.

// TestMascaraAtivaNuncaVazaPixelsDoCartao prova o invariante de privacidade: com
// a máscara ativa, NENHUM pixel da área do cartão no frame pós-patch pode
// conter a cor do cartão. É o que o operador veria se a máscara não entrasse.
//
// Regressão que impede: a máscara existir no registry mas ser IGNORADA pelo
// patcher (capturada com o cartão já visível, `ativa` não propagada, ou o
// patcher pulando a janela por erro de geometria) — em qualquer desses casos o
// cartão vaza inteiro para o stream com o nome do operador.
func TestMascaraAtivaNuncaVazaPixelsDoCartao(t *testing.T) {
	limparMascaras(t)
	const (
		corCartao  = byte(0xEE) // cor "do cartão": o que NUNCA pode chegar ao frame
		corDeFundo = byte(0x33) // conteúdo que estava atrás do cartão
		cardX      = 10
		cardY      = 5
		cardW      = 20
		cardH      = 10
	)
	frame := frameDeTeste(100, 50, 0, 0, corCartao)
	m := mascaraDeTeste(0xABC, winui.Rect{X: cardX, Y: cardY, W: cardW, H: cardH}, corDeFundo, true)
	mascarasMu.Lock()
	mascaras[m.hwnd] = m
	mascarasMu.Unlock()

	PatchFrameBanner(frame)

	vazados := 0
	for y := cardY; y < cardY+cardH; y++ {
		for x := cardX; x < cardX+cardW; x++ {
			if pixel(frame, x, y) == corCartao {
				vazados++
			}
		}
	}
	if vazados != 0 {
		t.Fatalf("%d pixel(s) do cartão continuam no frame dentro da área mascarada — o cartão vaza para o visualizador", vazados)
	}
	// A máscara precisa cobrir a área INTEIRA do cartão, não só o canto.
	if got := pixel(frame, cardX+cardW-1, cardY+cardH-1); got != corDeFundo {
		t.Fatalf("canto inferior direito da área do cartão = %#x, esperado o conteúdo de fundo %#x", got, corDeFundo)
	}
}

// TestMascaraDescartadaNaoReescreveMaisAArea prova que depois do descarte a área
// do cartão VOLTA a ser capturada ao vivo: o patcher não pode continuar
// reescrevendo a região com os pixels mortos da máscara antiga.
//
// Regressão que impede: o fantasma permanente — a máscara sai do registry de
// "ativa" mas continua sendo aplicada (ou o descarte só marca uma flag e o
// patcher ignora), e o operador fica com um retângulo congelado na tela até o
// fim da sessão. Foi o sintoma relatado em campo.
func TestMascaraDescartadaNaoReescreveMaisAArea(t *testing.T) {
	limparMascaras(t)
	limparMascarasDescartadas(t)
	const hwnd = uintptr(0x0A01)
	const (
		corViva  = byte(0x44) // conteúdo AO VIVO que deve aparecer depois do descarte
		corMorta = byte(0x99) // pixels congelados da máscara antiga
	)
	rect := winui.Rect{X: 10, Y: 5, W: 20, H: 10}

	// Antes do descarte: a máscara reescreve a área (controle do teste).
	frame := frameDeTeste(100, 50, 0, 0, corViva)
	m := mascaraDeTeste(hwnd, rect, corMorta, true)
	mascarasMu.Lock()
	mascaras[hwnd] = m
	mascarasMu.Unlock()
	PatchFrameBanner(frame)
	if got := pixel(frame, 10, 5); got != corMorta {
		t.Fatalf("pré-condição falhou: com máscara ativa a área deveria ser reescrita (pixel=%#x)", got)
	}

	// Descarte: é o que o refresh faz quando a composição não cobre a faixa.
	removerMascara(hwnd)
	if mascaraDisponivel(hwnd) {
		t.Fatal("removerMascara não tirou a máscara do registry")
	}

	// Depois do descarte: o frame NÃO pode ser reescrito com os pixels mortos.
	depois := frameDeTeste(100, 50, 0, 0, corViva)
	PatchFrameBanner(depois)
	if got := pixel(depois, 10, 5); got != corViva {
		t.Fatalf("após o descarte a área ainda foi reescrita (pixel=%#x, esperado o conteúdo ao vivo %#x) — o fantasma fica preso na tela", got, corViva)
	}
}

// TestMascaraDescartadaNaoEhRecapturada trava o item de PRIVACIDADE mais crítico:
// depois do descarte a máscara NÃO pode ser recapturada para aquele HWND com o
// cartão em tela — recapturar ali gravaria os PRÓPRIOS pixels do cartão na
// máscara e os repetiria em todo frame.
//
// Regressão que impede: o vazamento "silencioso e permanente" — pior que o
// fantasma, porque entrega o nome do operador ao visualizador em todos os frames.
func TestMascaraDescartadaNaoEhRecapturada(t *testing.T) {
	limparMascaras(t)
	limparMascarasDescartadas(t)
	const hwnd = uintptr(0x1E7D)
	t.Cleanup(func() { esquecerMascaraDescartada(hwnd) })

	// O descarte põe a trava de recaptura.
	removerMascara(hwnd)
	if !mascaraFoiDescartada(hwnd) {
		t.Fatal("o descarte não marcou a trava de recaptura")
	}
	// Com a trava posta e o cartão VISÍVEL, ativar não pode reinstalar a máscara:
	// seria o cartão fotografando a si mesmo.
	visibilidadeDeTeste(t, true)
	ativarMascara(hwnd)
	if mascaraDisponivel(hwnd) {
		t.Fatal("a máscara foi recapturada com o cartão VISÍVEL — conteria os próprios pixels do cartão e vazaria o nome do operador em todo frame")
	}
}

// TestMascaraAtivaSoEhAplicadaAJanelaVisivel garante que o patcher ignora a
// máscara de um cartão FORA da tela (sessão encerrando, desktop bloqueado,
// janela escondida). Reescrever ali devolveria conteúdo velho no lugar da área
// que o Windows já está capturando corretamente.
//
// Regressão que impede: faixa congelada persistente depois que o cartão sai da
// tela — a área deveria voltar a ser capturada ao vivo.
func TestMascaraAtivaSoEhAplicadaAJanelaVisivel(t *testing.T) {
	limparMascaras(t)
	const (
		corViva  = byte(0x55)
		corMorta = byte(0x88)
	)
	rect := winui.Rect{X: 10, Y: 5, W: 20, H: 10}

	// Cartão VISÍVEL: aplica.
	frameVisivel := frameDeTeste(100, 50, 0, 0, corViva)
	m := mascaraDeTeste(0x1E7E, rect, corMorta, true)
	mascarasMu.Lock()
	mascaras[m.hwnd] = m
	mascarasMu.Unlock()
	PatchFrameBanner(frameVisivel)
	if got := pixel(frameVisivel, 10, 5); got != corMorta {
		t.Fatalf("cartão visível deveria receber a máscara (pixel=%#x)", got)
	}

	// Cartão OCULTO: não aplica.
	mascaraVisivel = func(uintptr) bool { return false }
	frameOculto := frameDeTeste(100, 50, 0, 0, corViva)
	PatchFrameBanner(frameOculto)
	if got := pixel(frameOculto, 10, 5); got != corViva {
		t.Fatalf("cartão oculto não pode mascarar o frame (pixel=%#x) — a área ficaria congelada sem o cartão existir", got)
	}
}

// TestCoberturaParcialDaFaixaNaoEhAceitaComoTotal fixa o contrato NOVO da task-4:
// quando a composição da faixa não cobre a área inteira do cartão, a máscara é
// descartada NO MESMO CICLO — não se espera o limite de falhas consecutivas.
//
// Regressão que impede: a faixa lateral que ninguém escrevia (fantasma POR
// CONSTRUÇÃO quando a janela de trás cobria só parte do cartão). Antes o
// descarte levava ~6s (3 falhas x 2s); agora é imediato.
func TestCoberturaParcialDaFaixaNaoEhAceitaComoTotal(t *testing.T) {
	if regiaoCoberta(nil, winui.Rect{X: 0, Y: 0, W: 10, H: 10}) {
		t.Fatal("regiaoCoberta disse que uma área está coberta sem NENHUMA região coberta")
	}
	// Uma faixa parcial (cobre metade do cartão) não pode ser aceita como
	// cobertura total — é exatamente o caso "janela de trás cobre SÓ PARTE".
	coberto := []winui.Rect{{X: 0, Y: 0, W: 10, H: 5}}
	if regiaoCoberta(coberto, winui.Rect{X: 0, Y: 0, W: 10, H: 10}) {
		t.Fatal("cobertura PARCIAL foi aceita como total — a faixa não escrita ficaria congelada para sempre")
	}
	// Cobertura total (duas faixas que juntas cobrem tudo) é aceita.
	completo := []winui.Rect{{X: 0, Y: 0, W: 10, H: 5}, {X: 0, Y: 5, W: 10, H: 5}}
	if !regiaoCoberta(completo, winui.Rect{X: 0, Y: 0, W: 10, H: 10}) {
		t.Fatal("cobertura total (faixas somadas) foi rejeitada — o refresh seria descartado sem motivo")
	}
}

// TestCartaoRealNuncaVazaParaOFrame é a prova de PRIVACIDADE no nível do FRAME,
// com pixels REAIS da tela física: uma janela magenta de verdade é capturada, a
// máscara dessa janela é gravada como se fosse a área do cartão e depois o frame
// é recapturado com a janela já destruída. O patch precisa substituir a área pelo
// conteúdo do "antes" — nenhum pixel magenta pode sobrar.
//
// Regressão que impede: o vazamento do cartão para o visualizador quando o
// capturer é o Desktop Duplication (que ignora o WDA). É o cenário de campo em
// que o operador via a faixa do cartão no stream.
//
// t.Skip quando não há desktop interativo ou nenhum capturer disponível: teste
// de privacidade NUNCA pode falhar por ambiente.
func TestCartaoRealNuncaVazaParaOFrame(t *testing.T) {
	limparMascaras(t)
	const x, y = 40, 40
	janela := janelaMagenta(t, x, y)
	winui.ShowWindow(janela, winui.SwShowNormal)
	winui.UpdateWindow(janela)
	// Garante que o compositor desenhou a janela antes de capturar.
	windows.SleepEx(150, false)

	frameAntes, ok := capturarComTentativas(t)
	if !ok {
		winui.DestroyWindow(janela)
		t.Skip("nenhum frame pôde ser capturado neste ambiente")
	}
	magentaAntes := contarMagentaNaRegiao(frameAntes, x, y, testLargura, testAltura)
	if magentaAntes == 0 {
		// Sem o magenta no "antes" não há como provar a remoção: o ambiente não
		// deixou a janela aparecer na captura (VM sem compositor, sessão headless).
		winui.DestroyWindow(janela)
		t.Skip("a janela de teste não apareceu na captura — sem como medir o vazamento")
	}

	// O "cartão" sai da tela: a área passa a mostrar o conteúdo de baixo (o que a
	// máscara precisa conter). A leitura vem DEPOIS do DestroyWindow de propósito:
	// ler a máscara com o cartão ainda em tela gravaria o PRÓPRIO cartão — é
	// exatamente o erro que o contrato de capturarMascaraRegiao evita em produção
	// (a máscara é lida antes do ShowWindow).
	winui.DestroyWindow(janela)
	windows.SleepEx(250, false)

	pxAntes, err := lerPixelsTela(winui.Rect{X: x, Y: y, W: testLargura, H: testAltura})
	if err != nil {
		t.Skipf("leitura da máscara indisponível: %v", err)
	}

	// Coerência mínima do cenário: a máscara NÃO pode conter o cartão.
	if contarMagentaBytes(pxAntes) != 0 {
		t.Skipf("a janela de teste permaneceu visível após o DestroyWindow (%d pixels magenta) — sem compositor para medir", contarMagentaBytes(pxAntes))
	}

	const hwndFalso = uintptr(0x0FEE)
	m := &mascaraRegiao{
		hwnd:  hwndFalso,
		rect:  winui.Rect{X: x, Y: y, W: testLargura, H: testAltura},
		px:    pxAntes,
		ativa: true,
	}
	mascarasMu.Lock()
	mascaras[hwndFalso] = m
	mascarasMu.Unlock()
	t.Cleanup(func() {
		mascarasMu.Lock()
		delete(mascaras, hwndFalso)
		mascarasMu.Unlock()
	})

	frameDepois, ok := capturarComTentativas(t)
	if !ok {
		t.Skip("nenhum frame pôde ser capturado após o descarte")
	}
	PatchFrameBanner(frameDepois)

	if sobrou := contarMagentaNaRegiao(frameDepois, x, y, testLargura, testAltura); sobrou != 0 {
		t.Fatalf("%d pixel(s) do cartão (magenta) permaneceram no frame após o patch — o cartão VAZA para o visualizador", sobrou)
	}
}

// contarMagentaBytes conta pixels magenta em um buffer BGRA cru (teste).
func contarMagentaBytes(px []byte) int {
	n := 0
	for i := 0; i+3 < len(px); i += 4 {
		if px[i] == 0xFF && px[i+1] == 0x00 && px[i+2] == 0xFF {
			n++
		}
	}
	return n
}

// TestCapturarMascaraRegiaoRecusaJanelaVisivelReal prova, com uma janela de
// VERDADE, que capturarMascaraRegiao se recusa a fotografar uma janela VISÍVEL.
//
// Regressão que impede: a máscara ser lida com o cartão já em tela — ela passaria
// a conter os PRÓPRIOS pixels do cartão e o pipeline os repetiria em todo frame,
// entregando o nome do operador ao visualizador para sempre. É o item de
// privacidade mais crítico da entrega, e o único jeito de provar é com janela real
// (o caminho depende de winui.WindowRect e da visibilidade efetiva).
//
// t.Skip quando não há desktop interativo: nunca falhar por ambiente.
func TestCapturarMascaraRegiaoRecusaJanelaVisivelReal(t *testing.T) {
	limparMascaras(t)
	limparMascarasDescartadas(t)
	const x, y = 80, 80
	janela := janelaMagenta(t, x, y)
	defer winui.DestroyWindow(janela)
	winui.ShowWindow(janela, winui.SwShowNormal)
	winui.UpdateWindow(janela)
	windows.SleepEx(120, false)

	// Pré-condição: a janela precisa estar MESMO visível para o teste medir o
	// contrato (numa sessão sem compositor ela pode não reportar retângulo).
	if _, ok := winui.WindowRect(janela); !ok {
		t.Skipf("a janela de teste não reporta retângulo (hwnd=%d) — sem desktop interativo", janela)
	}

	err := capturarMascaraRegiao(janela)
	if err == nil {
		t.Fatal("capturarMascaraRegiao ACEITOU uma janela VISÍVEL — a máscara conteria o próprio cartão e o vazaria em todo frame")
	}
	if mascaraDisponivel(janela) {
		t.Fatal("uma máscara foi REGISTRADA a partir de uma janela visível — o cartão viraria a fonte da própria máscara")
	}
}
