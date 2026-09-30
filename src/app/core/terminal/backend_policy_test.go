//go:build windows

package terminal

import (
	"sync/atomic"
	"testing"
	"time"
)

// fakeShell implementa IShell só para exercitar a sonda de startup.
type fakeShell struct {
	alive atomic.Bool
}

func (f *fakeShell) WriteStdin(string) error { return nil }
func (f *fakeShell) Resize(int, int) error   { return nil }
func (f *fakeShell) Close() error            { return nil }
func (f *fakeShell) Wait() error             { return nil }
func (f *fakeShell) ShellKind() ShellKind    { return ShellCmd }
func (f *fakeShell) Alive() bool             { return f.alive.Load() }

func TestProbeForEarlyDeath_DeadProcess(t *testing.T) {
	f := &fakeShell{}
	f.alive.Store(false)
	start := time.Now()
	if !probeForEarlyDeath(f, conptyProbeTimeout, nil) {
		t.Fatal("processo morto deve ser detectado")
	}
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Fatalf("deteccao de morte lenta: %v", elapsed)
	}
}

func TestProbeForEarlyDeath_OutputLiberaCedo(t *testing.T) {
	f := &fakeShell{}
	f.alive.Store(true)
	first := &atomic.Bool{}
	first.Store(true)
	start := time.Now()
	if probeForEarlyDeath(f, conptyProbeTimeout, first) {
		t.Fatal("shell com saida nao deve ser considerado morto")
	}
	// Sem saída o caminho feliz espera a janela estável (500 ms); com saída
	// deve retornar quase imediatamente.
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("saida nao liberou a sonda cedo: %v", elapsed)
	}
}

func TestProbeForEarlyDeath_JanelaEstavel(t *testing.T) {
	f := &fakeShell{}
	f.alive.Store(true)
	start := time.Now()
	if probeForEarlyDeath(f, conptyProbeTimeout, nil) {
		t.Fatal("shell vivo nao deve ser considerado morto")
	}
	elapsed := time.Since(start)
	if elapsed < conptyStableWindow {
		t.Fatalf("saiu antes da janela de risco: %v < %v", elapsed, conptyStableWindow)
	}
	if elapsed > conptyStableWindow+300*time.Millisecond {
		t.Fatalf("lento demais apos a janela estavel: %v", elapsed)
	}
}

func TestProbeForEarlyDeath_TimeoutMenorQueJanela(t *testing.T) {
	f := &fakeShell{}
	f.alive.Store(true)
	start := time.Now()
	if probeForEarlyDeath(f, 100*time.Millisecond, nil) {
		t.Fatal("shell vivo nao deve ser considerado morto")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("timeout nao respeitado: %v", elapsed)
	}
}

func TestResolveBackendPolicy(t *testing.T) {
	cases := []struct {
		env  string
		want TerminalBackendPolicy
	}{
		{"", BackendAuto},
		{"auto", BackendAuto},
		{"conpty", BackendConPTY},
		{"strict", BackendConPTY},
		{"legacy", BackendLegacy},
		{"console", BackendLegacy},
		{"PIPE", BackendLegacy},
	}
	for _, c := range cases {
		t.Setenv("DISCOVERY_TERM_BACKEND", c.env)
		if got := ResolveBackendPolicy(); got != c.want {
			t.Fatalf("DISCOVERY_TERM_BACKEND=%q → %v, want %v", c.env, got, c.want)
		}
	}
}

func TestLegacyAllowExplicit(t *testing.T) {
	for _, v := range []string{"1", "true", "YES", "on"} {
		t.Setenv("DISCOVERY_TERM_ALLOW_LEGACY", v)
		if !legacyAllowExplicit() {
			t.Fatalf("%q deveria liberar o legacy", v)
		}
	}
	for _, v := range []string{"", "0", "false", "off"} {
		t.Setenv("DISCOVERY_TERM_ALLOW_LEGACY", v)
		if legacyAllowExplicit() {
			t.Fatalf("%q NAO deveria liberar o legacy", v)
		}
	}
}
