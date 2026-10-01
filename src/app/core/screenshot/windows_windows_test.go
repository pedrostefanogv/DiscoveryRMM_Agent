//go:build windows

package screenshot

import (
	"bytes"
	"image"
	"os"
	"path/filepath"
	"testing"
)

func TestRunPrintWorkerRequiresArgs(t *testing.T) {
	if code := RunPrintWorker(nil); code != 2 {
		t.Fatalf("RunPrintWorker(nil) = %d, want 2", code)
	}
	if code := RunPrintWorker([]string{"--hwnd=0", "--out=x.png"}); code != 2 {
		t.Fatalf("hwnd 0 = %d, want 2", code)
	}
	if code := RunPrintWorker([]string{"/screenshot-print-worker", "--out=x.png"}); code != 2 {
		t.Fatalf("sem hwnd = %d, want 2", code)
	}
}

func TestPrintHelperDisabledInTests(t *testing.T) {
	if !printHelperDisabled() {
		t.Fatal("o processo auxiliar deve ficar desabilitado em testes (binario .test.exe)")
	}
}

func TestWritePrintWindowPNGSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("captura real ignorada em -short")
	}
	wins, err := ListWindows()
	if err != nil || len(wins) == 0 {
		t.Skipf("nenhuma janela disponivel: %v", err)
	}
	var target *WindowInfo
	for i := range wins {
		if !wins[i].Minimized && !wins[i].IsSelf && wins[i].Width > 50 {
			target = &wins[i]
			break
		}
	}
	if target == nil {
		t.Skip("nenhuma janela alvo elegivel")
	}
	out := filepath.Join(t.TempDir(), "worker.png")
	if err := writePrintWindowPNG(target.Handle, out); err != nil {
		t.Skipf("PrintWindow do alvo %q falhou no ambiente: %v", target.Title, err)
	}
	data, err := os.ReadFile(out)
	if err != nil || len(data) == 0 {
		t.Fatalf("PNG do worker vazio: %v", err)
	}
	if !bytes.HasPrefix(data, []byte{0x89, 0x50, 0x4E, 0x47}) {
		t.Fatal("assinatura PNG ausente no arquivo do worker")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("PNG do worker invalido: %v", err)
	}
	if img.Bounds().Dx() <= 0 || img.Bounds().Dy() <= 0 {
		t.Fatal("dimensoes invalidas no PNG do worker")
	}
}
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
	if res.MIME != "image/png" && res.MIME != "image/jpeg" && res.MIME != "image/webp" {
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
