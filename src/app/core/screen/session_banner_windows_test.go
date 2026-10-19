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

	point := testPoint{X: int32(x + bannerWidth/2), Y: int32(y + bannerHeight/2)}
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
		uintptr(uint32(uint16(y+bannerHeight/2))<<16|uint32(uint16(x+bannerWidth/2))))
	if uintptr(got) != ^uintptr(0) {
		t.Fatalf("WM_NCHITTEST = %#x, esperado HTTRANSPARENT", got)
	}
}
