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

func TestPolicyDefaults(t *testing.T) {
	p := DefaultPolicy()
	if !p.FullScreenAllowed() || p.WindowRequired() {
		t.Fatal("defaults inesperados")
	}
	if p.LimitMax() != DefaultMaxCapturesPerWindow {
		t.Fatalf("max default = %d", p.LimitMax())
	}
	if p.LimitWindow() != time.Duration(DefaultCaptureWindowMinutes)*time.Minute {
		t.Fatalf("janela default = %s", p.LimitWindow())
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
