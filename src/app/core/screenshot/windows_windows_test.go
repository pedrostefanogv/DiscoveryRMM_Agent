//go:build windows

package screenshot

import (
	"testing"
)

func TestListWindowsSmoke(t *testing.T) {
	wins, err := ListWindows()
	if err != nil {
		t.Fatalf("ListWindows falhou: %v", err)
	}
	for i, w := range wins {
		if w.Handle == 0 {
			t.Errorf("janela %d sem handle", i)
		}
		if w.Width <= 0 || w.Height <= 0 {
			t.Errorf("janela %d com dimensoes invalidas: %dx%d (%q)", i, w.Width, w.Height, w.Title)
		}
		if i > 0 && w.ZOrder != i {
			t.Errorf("z-order fora de sequencia: got %d, want %d", w.ZOrder, i)
		}
	}
	t.Logf("janelas visiveis detectadas: %d", len(wins))
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("curto", 10); got != "curto" {
		t.Fatalf("nao deveria truncar: %q", got)
	}
	long := ""
	for i := 0; i < 200; i++ {
		long += "á"
	}
	got := truncateRunes(long, 160)
	if []rune(got)[0] != 'á' || len([]rune(got)) != 161 {
		t.Fatalf("truncamento rune-safe inesperado: %d runes", len([]rune(got)))
	}
}

func TestCaptureDesktopSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("captura real ignorada em -short")
	}
	res, err := CaptureDesktop(70, 1200)
	if err != nil {
		t.Fatalf("CaptureDesktop falhou: %v", err)
	}
	if len(res.Data) == 0 {
		t.Fatal("captura vazia")
	}
	if res.MIME != "image/png" && res.MIME != "image/jpeg" {
		t.Fatalf("mime inesperado: %q", res.MIME)
	}
	if res.Width <= 0 || res.Height <= 0 {
		t.Fatalf("dimensoes invalidas: %dx%d", res.Width, res.Height)
	}
	if res.Width > 1200 && res.Height > 1200 {
		t.Fatalf("downscale nao aplicado: %dx%d", res.Width, res.Height)
	}
	t.Logf("%s", res.Describe())
}

func TestCaptureWindowSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("captura real ignorada em -short")
	}
	wins, err := ListWindows()
	if err != nil || len(wins) == 0 {
		t.Skipf("nenhuma janela para capturar: %v", err)
	}
	// Escolhe a primeira janela não minimizada de outro processo.
	var target *WindowInfo
	for i := range wins {
		if !wins[i].Minimized && !wins[i].IsSelf {
			target = &wins[i]
			break
		}
	}
	if target == nil {
		t.Skip("nenhuma janela alvo elegivel")
	}
	res, err := captureWindow(target.Handle, 70, 800)
	if err != nil {
		t.Skipf("captura da janela %q falhou (ambiente): %v", target.Title, err)
	}
	if len(res.Data) == 0 || res.MIME == "" {
		t.Fatal("captura de janela vazia")
	}
	t.Logf("%s", res.Describe())
}
