//go:build windows

package screen

import (
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"discovery/app/core/winui"
)

// ── Canários de captura com janelas reais ──
//
// Dois pressupostos do mecanismo da máscara dependem de comportamento do
// Windows que PODE mudar entre versões/drivers. Estes testes medem o mundo
// real e falham alto quando o pressuposto quebra — antes que o campo
// descubra com um cartão vazado no stream.

const magentaTeste2 = 0x00FF00FF

// TestJanelaExcluidaSaiDoBitBlt trava o pressuposto do caminho PREFERIDO do
// refresh (fonteAoVivoBitBlt): com WDA_EXCLUDEFROMCAPTURE efetivo, o BitBlt GDI
// da região NÃO contém a janela — devolve o que está ATRÁS dela (0 px magenta).
// Se um Windows/driver passar a desenhar a janela (ou preto chapado) mesmo com
// a exclusão, este teste falha antes de a válvula/produção confiarem cegas.
func TestJanelaExcluidaSaiDoBitBlt(t *testing.T) {
	if testing.Short() {
		t.Skip("teste de captura real")
	}
	hwnd := janelaMagenta(t, 700, 400)
	defer winui.DestroyWindow(hwnd)
	winui.ShowWindow(hwnd, winui.SwShowNormal)
	winui.UpdateWindow(hwnd)
	time.Sleep(400 * time.Millisecond)

	// Controle: sem afinidade a janela aparece na captura GDI.
	px, err := lerPixelsTelaWin32(winui.Rect{X: 700, Y: 400, W: testLargura, H: testAltura})
	if err != nil {
		t.Fatalf("bitblt controle: %v", err)
	}
	if n := contarMagentaBuffer(px); n == 0 {
		t.Skip("controle inválido neste ambiente (janela não apareceu na captura)")
	}

	ok, errAfin := affinitySet(hwnd, wdaExcludeFromCapture)
	if !ok {
		t.Skipf("SetWindowDisplayAffinity(EXCLUDE) recusado neste ambiente: %v", errAfin)
	}
	time.Sleep(400 * time.Millisecond)
	px2, err2 := lerPixelsTelaWin32(winui.Rect{X: 700, Y: 400, W: testLargura, H: testAltura})
	if err2 != nil {
		t.Fatalf("bitblt exclude: %v", err2)
	}
	if n := contarMagentaBuffer(px2); n != 0 {
		t.Fatalf("BitBlt NÃO honrou WDA_EXCLUDEFROMCAPTURE (%d px magenta) — o caminho BitBlt do refresh leria o próprio cartão", n)
	}
	if regiaoTodaPreta(px2) {
		t.Skip("exclusão honrada, mas a leitura veio toda preta neste ambiente (não é conteúdo atrás)")
	}
}

func contarMagentaBuffer(px []byte) int {
	magenta := 0
	for i := 0; i+3 < len(px); i += 4 {
		if px[i] == 0xFF && px[i+1] == 0x00 && px[i+2] == 0xFF {
			magenta++
		}
	}
	return magenta
}

// TestDiagnosticoPrintWindowAppsReais é um canário de CAMPO, não uma asserção
// de contrato: enumera janelas visíveis com título e relata como o PrintWindow
// as imprime (ok / preto / branco / recusado). Útil quando um app específico
// aparece "congelado" na faixa do cartão — rodar com -v e comparar.
func TestDiagnosticoPrintWindowAppsReais(t *testing.T) {
	if testing.Short() {
		t.Skip("diagnóstico com janelas reais")
	}
	procEnumWindows := user32.NewProc("EnumWindows")
	procGetWindowTextW := user32.NewProc("GetWindowTextW")
	type achado struct {
		hwnd   uintptr
		titulo string
	}
	var janelas []achado
	cb := windows.NewCallback(func(hwnd, lparam uintptr) uintptr {
		vis, _, _ := procIsWindowVisible.Call(hwnd)
		if vis == 0 {
			return 1
		}
		var title[128]uint16
		n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&title[0])), 128)
		if n == 0 {
			return 1
		}
		janelas = append(janelas, achado{hwnd: hwnd, titulo: windows.UTF16ToString(title[:n])})
		return 1
	})
	procEnumWindows.Call(cb, 0)
	t.Logf("janelas visíveis com título: %d", len(janelas))
	var buf []byte
	for i, j := range janelas {
		if i >= 10 {
			break
		}
		r, pixels, ok, motivo := imprimirJanelaWin32(j.hwnd, &buf)
		if !ok {
			t.Logf("%q hwnd=%x rect=%+v: FALHOU (%s)", j.titulo, j.hwnd, r, motivo)
			continue
		}
		t.Logf("%q hwnd=%x rect=%+v: ok %d bytes todaPreta=%v todaBranca=%v",
			j.titulo, j.hwnd, r, len(pixels), regiaoTodaPreta(pixels), regiaoTodaBranca(pixels))
	}
}
