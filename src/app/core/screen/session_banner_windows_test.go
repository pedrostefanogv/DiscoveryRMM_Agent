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
