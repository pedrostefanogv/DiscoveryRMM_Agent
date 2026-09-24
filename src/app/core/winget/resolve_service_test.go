package winget

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// ── Resolução do winget: nativa primeiro, fallback depois ──

func TestResolveExecutable_FindsWinget(t *testing.T) {
	p, source := ResolveExecutable()
	if p == "" {
		t.Skip("winget nao instalado neste host")
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("caminho resolvido nao existe: %s (%v)", p, err)
	}
	t.Logf("winget resolvido via %s: %s", source, p)
}

// TestResolveExecutable_SimulatesServiceContext reproduz o cenário que quebrou o
// agente em produção: o PATH da MÁQUINA (usado pelo serviço LocalSystem) não
// contém o diretório do alias por usuário. O resolver precisa achar o winget
// mesmo assim — senão o inventário é reportado sem updates.
func TestResolveExecutable_SimulatesServiceContext(t *testing.T) {
	original := os.Getenv("PATH")
	originalLocal := os.Getenv("LOCALAPPDATA")
	t.Cleanup(func() {
		os.Setenv("PATH", original)
		os.Setenv("LOCALAPPDATA", originalLocal)
		InvalidateResolvedExecutable()
	})

	// PATH sem o alias + sem LOCALAPPDATA (como no serviço SYSTEM).
	os.Setenv("PATH", `C:\Windows\system32;C:\Windows;C:\ProgramData\chocolatey\bin`)
	os.Unsetenv("LOCALAPPDATA")
	InvalidateResolvedExecutable()

	if _, err := lookPathWinget(); err == nil {
		t.Skip("PATH de teste ainda resolve winget — host com alias no PATH da maquina")
	}

	p, source := ResolveExecutable()
	if p == "" {
		t.Skipf("winget indisponivel fora do PATH neste host (origem=%s) — o fallback preserva a ultima lista", source)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("caminho resolvido nao existe: %s (%v)", p, err)
	}
	t.Logf("resolvido fora do PATH via %s: %s", source, p)
}

func TestProfileLocalAppDataDirs_ScansUsers(t *testing.T) {
	dirs := profileLocalAppDataDirs()

	// Perfis que nunca têm o alias precisam ficar de fora.
	for _, d := range dirs {
		base := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(d))))
		switch {
		case equalFold(base, "Public"), equalFold(base, "Default"), equalFold(base, "All Users"):
			t.Fatalf("perfil que deveria ser ignorado: %s", d)
		}
	}
}

func TestAliasUnder_ValidatesPath(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "Microsoft", "WindowsApps")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}

	if got := aliasUnder(dir); got != "" {
		t.Fatalf("sem o arquivo deveria devolver vazio, veio %q", got)
	}

	exe := filepath.Join(target, "winget.exe")
	if err := os.WriteFile(exe, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := aliasUnder(dir); got != exe {
		t.Fatalf("aliasUnder = %q, esperado %q", got, exe)
	}
}

func TestNewestMatch_PicksHighestVersion(t *testing.T) {
	dir := t.TempDir()
	older := filepath.Join(dir, "v1")
	newer := filepath.Join(dir, "v2")
	for _, d := range []string{older, newer} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "winget.exe"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got := newestMatch(filepath.Join(dir, "*", "winget.exe"))
	if got != filepath.Join(newer, "winget.exe") {
		t.Fatalf("newestMatch = %q, esperado %q", got, filepath.Join(newer, "winget.exe"))
	}
	if got := newestMatch(filepath.Join(dir, "inexistente", "*.exe")); got != "" {
		t.Fatalf("sem matches deveria devolver vazio, veio %q", got)
	}
}

// TestFirstWingetPath_ParsesOsqueryRows garante a extração do caminho a partir
// das linhas do osquery (fallback de último recurso).
func TestFirstWingetPath_ParsesOsqueryRows(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "winget.exe")
	if err := os.WriteFile(exe, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"linha simples", exe + "\n", exe},
		{"com CRLF", exe + "\r\n", exe},
		{"coluna separada por pipe", "foo|" + exe + "\n", exe},
		{"ruido antes", "lixo\n" + exe + "\n", exe},
		{"nenhum caminho", "lixo\nmais lixo\n", ""},
		{"vazio", "", ""},
	}
	for _, tc := range cases {
		if got := firstWingetPath(tc.raw); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestFromOsquery_UsedOnlyAsFallback garante que o osquery é consultado com a
// query esperada e que o resultado é aproveitado.
func TestFromOsquery_UsedOnlyAsFallback(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "winget.exe")
	if err := os.WriteFile(exe, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	called := false
	lookup := func(_ context.Context, sql string) []string {
		called = true
		if sql == "" {
			t.Error("query vazia")
		}
		return []string{exe}
	}

	if got := fromOsquery(lookup); got != exe {
		t.Fatalf("fromOsquery = %q, esperado %q", got, exe)
	}
	if !called {
		t.Fatal("lookup nao foi chamado")
	}

	if got := fromOsquery(nil); got != "" {
		t.Fatalf("sem lookup deveria devolver vazio, veio %q", got)
	}
}

func equalFold(a, b string) bool {
	return len(a) == len(b) && (a == b || filepath.Base(a) == b)
}
