//go:build windows

package screen

import (
	"testing"

	"golang.org/x/sys/windows"

	"discovery/app/core/winui"
)

// Procs do user32 usados só por este arquivo. São declarados aqui (e não em um
// var global de produção) porque existem apenas para MEDIR o efeito da
// reafirmação: ler o estilo estendido da janela e a janela em primeiro plano.
var (
	procZorderGetWindowLongPtrW = user32DLL.NewProc("GetWindowLongPtrW")
	procZorderGetForeground     = user32DLL.NewProc("GetForegroundWindow")
)

// ── Reafirmação do topmost: verificação de EFEITO com janela real ──
//
// O restante da cobertura do z-order (função pura, flags, WM_TIMER, marca de
// log) vive em session_banner_windows_test.go, onde a injeção de
// bannerTopmostSetWindowPos conta as chamadas sem abrir janela.
//
// O que NÃO dá para provar por injeção é o efeito no Windows: que a reafirmação
// realmente mantém a janela na banda topmost (WS_EX_TOPMOST) sem perder
// WS_EX_NOACTIVATE (o banner é click-through e não pode capturar foco/cliques) e
// sem roubar o foco de quem o usuário local está usando. É isso que este arquivo
// mede, com uma janela de verdade — e com t.Skip quando o ambiente não tem
// desktop interativo, porque teste de z-order nunca pode falhar por ambiente.

const (
	// zorderGwlExStyle é GWL_EXSTYLE (-20) com sinal estendido para uintptr, do
	// mesmo jeito que winui.EnsureTopMost estende HWND_TOPMOST.
	zorderGwlExStyle = ^uintptr(19)
	// zorderWsExNoActivate é o bit de estilo que mantém o banner click-through
	// (ele não pode capturar foco nem cliques). O bit de topmost já existe no
	// pacote como wsExTopmost, usado também pelo listener de ordem Z.
	zorderWsExNoActivate = 0x08000000
)

// TestTopmostRealPreservaExStyleESemRoubarFoco é a verificação independente de
// EFEITO da correção do z-order.
//
// Regressão que impede: uma reafirmação que saísse da banda topmost (insertAfter
// diferente de HWND_TOPMOST, ou um SetWindowPos que zerasse os estilos) deixaria
// o cartão atrás do menu Iniciar de novo; e uma reafirmação sem SWP_NOACTIVATE
// faria o banner — que é WS_EX_NOACTIVATE justamente para ser click-through —
// roubar o foco de teclado do usuário local.
func TestTopmostRealPreservaExStyleESemRoubarFoco(t *testing.T) {
	if err := ensureBannerClass(); err != nil {
		t.Skipf("classe do banner indisponível neste ambiente: %v", err)
	}
	hwnd, err := winui.CreateWindow(
		bannerClassName,
		"teste topmost",
		winui.ExStyleTopmost|winui.ExStyleToolWindow|winui.ExStyleNoActivate|
			winui.ExStyleLayered|winui.ExStyleTransparent,
		winui.WinStylePopup|winui.WinStyleClipSiblings,
		0, 0, bannerWidth, bannerHeight,
	)
	if err != nil {
		t.Skipf("sem desktop interativo para criar a janela: %v", err)
	}
	defer winui.DestroyWindow(hwnd)

	estiloAntes, _, _ := procZorderGetWindowLongPtrW.Call(hwnd, zorderGwlExStyle)
	if estiloAntes == 0 {
		t.Skipf("GetWindowLongPtr(GWL_EXSTYLE) indisponível neste ambiente (hwnd=%d)", hwnd)
	}
	// Sem WS_EX_TOPMOST de nascença o teste não mede o contrato do banner.
	if estiloAntes&wsExTopmost == 0 {
		t.Skipf("janela de teste não entrou na banda topmost (exstyle=%#x) — sem compositor para medir z-order", estiloAntes)
	}
	if estiloAntes&zorderWsExNoActivate == 0 {
		t.Fatalf("janela de teste nasceu sem WS_EX_NOACTIVATE (exstyle=%#x) — o contrato medido não é o do banner", estiloAntes)
	}

	focoAntes, _, _ := procZorderGetForeground.Call()

	// Caminho REAL de produção (não o ponto de injeção): é o P/Invoke que o
	// binário executa a cada tique do timer.
	reafirmarBannerTopmost(hwnd, bannerTopmostMotivoReafirmacao)
	windows.SleepEx(60, false)

	estiloDepois, _, _ := procZorderGetWindowLongPtrW.Call(hwnd, zorderGwlExStyle)
	if estiloDepois&wsExTopmost == 0 {
		t.Fatalf("WS_EX_TOPMOST foi perdido após a reafirmação (exstyle %#x -> %#x) — o cartão volta a ficar atrás do menu Iniciar", estiloAntes, estiloDepois)
	}
	if estiloDepois&zorderWsExNoActivate == 0 {
		t.Fatalf("WS_EX_NOACTIVATE foi perdido após a reafirmação (exstyle %#x -> %#x) — o banner passaria a roubar foco e cliques", estiloAntes, estiloDepois)
	}

	// O banner é WS_EX_NOACTIVATE: a reafirmação JAMAIS pode ativar a janela.
	focoDepois, _, _ := procZorderGetForeground.Call()
	if focoDepois == hwnd {
		t.Fatalf("a reafirmação trouxe o banner para o primeiro plano (GetForegroundWindow=%v, antes=%v) — o cartão roubou o foco", focoDepois, focoAntes)
	}
	if focoAntes != 0 && focoDepois != focoAntes {
		t.Fatalf("a reafirmação mudou a janela em primeiro plano (%v -> %v) — não pode roubar foco de quem o usuário está usando", focoAntes, focoDepois)
	}
}

// TestMascaraDeFlagsDoTopmostEhCompleta prende o contrato da MÁSCARA de flags
// usada pela reafirmação: se alguém "limpar" uma das quatro flags para enxugar
// o código, este teste e a injeção em session_banner_windows_test.go acusam.
//
// Regressão que impede: perder SWP_NOACTIVATE (rouba foco), SWP_NOMOVE/NOSIZE
// (o cartão pula/redimensiona a cada segundo) ou SWP_NOOWNERZORDER (reordenação
// inesperada entre janelas do mesmo dono).
func TestMascaraDeFlagsDoTopmostEhCompleta(t *testing.T) {
	const esperado = swpNoSize | swpNoMove | swpNoActivate | swpNoOwnerZOrder
	if swpNecessariasParaNaoQuebrarClickThrough != esperado {
		t.Fatalf("máscara de flags = %#x, esperado %#x (NoSize|NoMove|NoActivate|NoOwnerZOrder)",
			swpNecessariasParaNaoQuebrarClickThrough, esperado)
	}
	if hwndTopmost != ^uintptr(0) {
		t.Fatalf("hwndTopmost = %#x, esperado ^uintptr(0) (HWND_TOPMOST = -1 com sinal estendido)", hwndTopmost)
	}
}
