package screenshot

import (
	"testing"
	"time"
)

func TestPolicyBlocksProcessBySubstringAndExact(t *testing.T) {
	p := DefaultPolicy()
	p.BlockedProcesses = []string{"keepass", "notepad.exe"}
	if !p.IsProcessBlocked("KeePassXC.exe") {
		t.Fatal("deveria bloquear por trecho (keepass)")
	}
	if !p.IsProcessBlocked("C:\\Windows\\notepad.exe") {
		t.Fatal("deveria bloquear por nome exato ignorando diretorio/.exe")
	}
	if p.IsProcessBlocked("explorer.exe") {
		t.Fatal("nao deveria bloquear processo fora da lista")
	}
	if p.IsProcessBlocked("") {
		t.Fatal("nome vazio nunca bloqueia")
	}
}

func TestPolicyKeepsExplicitPNGFormat(t *testing.T) {
	p := DefaultPolicy()
	p.ImageFormat = "PNG"
	n := p.Normalize()
	if n.ImageFormat != FormatPNG {
		t.Fatalf("ImageFormat = %q, want %q", n.ImageFormat, FormatPNG)
	}
}

func TestPolicyNormalize(t *testing.T) {
	full := false
	requireWindow := true
	p := Policy{
		BlockedProcesses:     []string{"  ", "keepass", "KeePass", "notepad.exe"},
		AllowFullScreen:      &full,
		RequireWindow:        &requireWindow,
		MaxCapturesPerWindow: 999,
		WindowMinutes:        99999,
	}
	n := p.Normalize()
	if len(n.BlockedProcesses) != 2 {
		t.Fatalf("blocklist normalizada = %v", n.BlockedProcesses)
	}
	if n.FullScreenAllowed() {
		t.Fatal("AllowFullScreen deveria permanecer false")
	}
	if !n.WindowRequired() {
		t.Fatal("RequireWindow deveria permanecer true")
	}
	if n.MaxCapturesPerWindow != 120 {
		t.Fatalf("max normalizado = %d, want 120", n.MaxCapturesPerWindow)
	}
	if n.LimitWindow() != 1440*time.Minute {
		t.Fatalf("janela normalizada = %s", n.LimitWindow())
	}
}

// Padrões de produto: permitir pedidos da IA, tela inteira permitida, janela do
// agente oculta, formato automático e 10 capturas a cada 1 minuto (o mesmo
// alvo do botão "Voltar ao padrão").
func TestPolicyDefaults(t *testing.T) {
	p := DefaultPolicy()
	if !p.FullScreenAllowed() || p.WindowRequired() {
		t.Fatal("defaults inesperados")
	}
	if !p.AiCaptureAllowed() {
		t.Fatal("pedidos de captura da IA deveriam ser permitidos por padrão")
	}
	if !p.HideWindowOnCapture() {
		t.Fatal("a janela do agente deveria ser ocultada por padrão")
	}
	if p.ImageFormat != FormatAuto {
		t.Fatalf("formato default = %q, want %q", p.ImageFormat, FormatAuto)
	}
	if p.LimitMax() != 10 {
		t.Fatalf("max default = %d, want 10", p.LimitMax())
	}
	if p.LimitWindow() != time.Minute {
		t.Fatalf("janela default = %s, want 1 minuto", p.LimitWindow())
	}
	if p.LimitWindow() != time.Duration(DefaultCaptureWindowMinutes)*time.Minute {
		t.Fatalf("janela default = %s", p.LimitWindow())
	}
	// DefaultPolicy é o alvo do reset: precisa sobreviver ao Normalize sem
	// perder nenhum dos campos (cópia defensiva dos ponteiros).
	n := p.Normalize()
	if !n.AiCaptureAllowed() || !n.FullScreenAllowed() || n.WindowRequired() || !n.HideWindowOnCapture() {
		t.Fatal("Normalize alterou os defaults")
	}
	if n.ImageFormat != FormatAuto || n.MaxCapturesPerWindow != 10 || n.WindowMinutes != 1 {
		t.Fatalf("defaults apos Normalize = %+v", n)
	}
}

func TestNormalizeProcessName(t *testing.T) {
	cases := map[string]string{
		"Notepad.EXE":          "notepad",
		"C:\\Apps\\chrome.exe": "chrome",
		"  KeePassXC.exe  ":    "keepassxc",
		"":                     "",
	}
	for in, want := range cases {
		if got := NormalizeProcessName(in); got != want {
			t.Errorf("NormalizeProcessName(%q) = %q, want %q", in, got, want)
		}
	}
}
