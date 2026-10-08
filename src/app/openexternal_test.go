package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeEnv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestResolveOpenFolderPath_AliasesAndAbsolute(t *testing.T) {
	// Com o hook do SO ativo, "downloads" resolve para a pasta REAL (e
	// redirecionada) da máquina. Aqui testamos o FALLBACK por variáveis de
	// ambiente, então o hook é desligado neste teste.
	origKnown := knownFolderPath
	knownFolderPath = nil
	t.Cleanup(func() { knownFolderPath = origKnown })

	base := t.TempDir()
	downloads := filepath.Join(base, "Downloads")
	if err := os.MkdirAll(downloads, 0o755); err != nil {
		t.Fatal(err)
	}
	env := fakeEnv(map[string]string{"USERPROFILE": base, "TEMP": base})

	got, err := resolveOpenFolderPath("downloads", env)
	if err != nil || got != downloads {
		t.Fatalf("apelido downloads: got=%q err=%v", got, err)
	}
	got, err = resolveOpenFolderPath("  Downloads ", env)
	if err != nil || got != downloads {
		t.Fatalf("apelido case-insensitive: got=%q err=%v", got, err)
	}
	got, err = resolveOpenFolderPath(downloads, env)
	if err != nil || got != downloads {
		t.Fatalf("caminho absoluto: got=%q err=%v", got, err)
	}
}

func TestResolveOpenFolderPath_Rejections(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "arquivo.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := fakeEnv(map[string]string{"USERPROFILE": base, "TEMP": base})

	cases := []struct {
		name  string
		input string
	}{
		{"vazio", "  "},
		{"relativo", "downloads\\sub"},
		{"unc", "\\\\servidor\\share"},
		{"inexistente", filepath.Join(base, "nao-existe")},
		{"arquivo", file},
		{"apelido sem env", "appdata"},
	}
	for _, tc := range cases {
		if got, err := resolveOpenFolderPath(tc.input, env); err == nil {
			t.Fatalf("%s: esperava erro, obteve %q", tc.name, got)
		}
	}
}

func TestResolveLaunchAppTarget_StartMenuAndAppPaths(t *testing.T) {
	base := t.TempDir()
	startMenu := filepath.Join(base, "Microsoft", "Windows", "Start Menu", "Programs")
	if err := os.MkdirAll(filepath.Join(startMenu, "Google"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel string) string {
		p := filepath.Join(startMenu, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("lnk"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	chrome := write("Google\\Google Chrome.lnk")
	write("Acessorios\\Bloco de Notas.lnk")
	env := fakeEnv(map[string]string{"ProgramData": base})

	got, err := resolveLaunchAppTarget("Google Chrome", env, nil)
	if err != nil || got != chrome {
		t.Fatalf("exato: got=%q err=%v", got, err)
	}
	if got, err = resolveLaunchAppTarget("chrome", env, nil); err != nil || got != chrome {
		t.Fatalf("por palavra: got=%q err=%v", got, err)
	}
	if got, err = resolveLaunchAppTarget("Google", env, nil); err != nil || got != chrome {
		t.Fatalf("prefixo: got=%q err=%v", got, err)
	}
	if _, err := resolveLaunchAppTarget("Photoshop", env, nil); err == nil {
		t.Fatal("app inexistente deveria falhar")
	}
	if _, err := resolveLaunchAppTarget("C:\\Windows\\notepad.exe", env, nil); err == nil {
		t.Fatal("caminho no lugar do nome deveria ser recusado")
	}
	if _, err := resolveLaunchAppTarget("cmd /c calc", env, nil); err == nil {
		t.Fatal("argumentos no nome deveriam ser recusados")
	}

	// App Paths do Registro tem prioridade quando existe.
	exe := filepath.Join(base, "custom.exe")
	if err := os.WriteFile(exe, []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = resolveLaunchAppTarget("custom", env, func(string) (string, bool) { return exe, true })
	if err != nil || got != exe {
		t.Fatalf("app paths: got=%q err=%v", got, err)
	}
}

func TestAppCandidatesForMessage_ListsSimilarNames(t *testing.T) {
	base := t.TempDir()
	startMenu := filepath.Join(base, "Microsoft", "Windows", "Start Menu", "Programs")
	if err := os.MkdirAll(startMenu, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"Google Chrome.lnk", "Microsoft Edge.lnk"} {
		if err := os.WriteFile(filepath.Join(startMenu, n), []byte("lnk"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := appCandidatesForMessage("chrome", fakeEnv(map[string]string{"ProgramData": base}))
	if len(got) != 1 || !strings.Contains(got[0], "Chrome") {
		t.Fatalf("candidatos inesperados: %#v", got)
	}
}

// Pasta conhecida REDIRECIONADA: o hook do SO vence o fallback por env — é o
// caso de máquinas com Downloads/Documentos em outro disco (perfil móvel).
func TestResolveOpenFolderPath_PrefersKnownFolder(t *testing.T) {
	base := t.TempDir()
	moved := filepath.Join(base, "DownloadEmOutroDisco")
	if err := os.MkdirAll(moved, 0o755); err != nil {
		t.Fatal(err)
	}
	env := fakeEnv(map[string]string{"USERPROFILE": filepath.Join(base, "perfil-sem-downloads")})
	if err := os.MkdirAll(env("USERPROFILE"), 0o755); err != nil {
		t.Fatal(err)
	}

	orig := knownFolderPath
	t.Cleanup(func() { knownFolderPath = orig })
	knownFolderPath = func(name string) (string, bool) {
		if name == "downloads" {
			return moved, true
		}
		return "", false
	}

	got, err := resolveOpenFolderPath("downloads", env)
	if err != nil || got != moved {
		t.Fatalf("esperava a pasta redirecionada %q, got=%q err=%v", moved, got, err)
	}
}

func TestInstalledAppNames_FilterAndLimit(t *testing.T) {
	base := t.TempDir()
	startMenu := filepath.Join(base, "Microsoft", "Windows", "Start Menu", "Programs")
	if err := os.MkdirAll(startMenu, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"Google Chrome.lnk", "Google Drive.lnk", "Microsoft Edge.lnk"} {
		if err := os.WriteFile(filepath.Join(startMenu, n), []byte("lnk"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := fakeEnv(map[string]string{"ProgramData": base})

	all := installedAppNames("", 0, env)
	if len(all) != 3 {
		t.Fatalf("esperava 3 apps, obtido %#v", all)
	}
	google := installedAppNames("google", 0, env)
	if len(google) != 2 {
		t.Fatalf("filtro google: %#v", google)
	}
	limited := installedAppNames("", 2, env)
	if len(limited) != 2 {
		t.Fatalf("limite nao respeitado: %#v", limited)
	}
	if got := installedAppNames("inexistente", 0, env); len(got) != 0 {
		t.Fatalf("filtro sem match: %#v", got)
	}
}
