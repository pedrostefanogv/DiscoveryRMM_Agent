package app

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"discovery/app/core/database"
	"discovery/app/core/testutil"
	"discovery/app/logs"
)

func TestIsAgentDecommissionCommandType(t *testing.T) {
	for _, value := range []string{
		"decommissionagent",
		"DecommissionAgent",
		"agentdecommission",
		"decommission",
		"uninstallagent",
		"  decommissionagent  ",
	} {
		if !isAgentDecommissionCommandType(value) {
			t.Fatalf("esperado true para %q", value)
		}
	}

	for _, value := range []string{"", "softwareuninstall", "uninstall", "decommissionx", "update"} {
		if isAgentDecommissionCommandType(value) {
			t.Fatalf("esperado false para %q", value)
		}
	}
}

func TestParseUninstallerExecutable(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{`"C:/Program Files/Discovery/uninstall.exe" /S`, "C:/Program Files/Discovery/uninstall.exe"},
		{`C:/Program Files/Discovery/uninstall.exe /S`, "C:/Program Files/Discovery/uninstall.exe"},
		{`"C:/Program Files/Discovery/uninstall.exe"`, "C:/Program Files/Discovery/uninstall.exe"},
		{"", ""},
		{`"sem fechamento`, ""},
	}

	for _, tc := range cases {
		if got := parseUninstallerExecutable(tc.raw); got != tc.want {
			t.Fatalf("parseUninstallerExecutable(%q) = %q, esperado %q", tc.raw, got, tc.want)
		}
	}
}

func TestParseAgentDecommissionPayload(t *testing.T) {
	requestedBy, reason := parseAgentDecommissionPayload(map[string]any{
		"requestedBy": "  console  ",
		"reason":      "trash",
	})
	if requestedBy != "console" || reason != "trash" {
		t.Fatalf("payload map: requestedBy=%q reason=%q", requestedBy, reason)
	}

	requestedBy, reason = parseAgentDecommissionPayload(`{"reason":"purge"}`)
	if requestedBy != "" || reason != "purge" {
		t.Fatalf("payload string: requestedBy=%q reason=%q", requestedBy, reason)
	}

	requestedBy, reason = parseAgentDecommissionPayload(nil)
	if requestedBy != "" || reason != "" {
		t.Fatalf("payload nil: requestedBy=%q reason=%q", requestedBy, reason)
	}
}

func TestValidateUninstallerPath(t *testing.T) {
	dir := testutil.TempDir(t)
	valid := filepath.Join(dir, uninstallerFileName)
	if err := os.WriteFile(valid, []byte("stub"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	origTrusted := trustedUninstallerRootsFn
	origUntrusted := untrustedUninstallerRootsFn
	origACL := verifyUninstallerACLFn
	t.Cleanup(func() {
		trustedUninstallerRootsFn = origTrusted
		untrustedUninstallerRootsFn = origUntrusted
		verifyUninstallerACLFn = origACL
	})
	trustedUninstallerRootsFn = func() []string { return []string{dir} }
	untrustedUninstallerRootsFn = func() []string { return nil }
	verifyUninstallerACLFn = func(string) error { return nil }

	got, err := validateUninstallerPath(valid)
	if err != nil || got != valid {
		t.Fatalf("validate valid: got=%q err=%v", got, err)
	}

	if _, err := validateUninstallerPath(filepath.Join(dir, "setup.exe")); err == nil {
		t.Fatal("esperado erro para executÁ¡vel com nome inesperado")
	}
	if _, err := validateUninstallerPath(dir); err == nil {
		t.Fatal("esperado erro para diretório")
	}
	if _, err := validateUninstallerPath(filepath.Join(dir, "missing.exe")); err == nil {
		t.Fatal("esperado erro para arquivo inexistente")
	}
}

func TestValidateUninstallerPathRejectsUntrustedPaths(t *testing.T) {
	tempDir := testutil.TempDir(t)
	tempUninstall := filepath.Join(tempDir, uninstallerFileName)
	if err := os.WriteFile(tempUninstall, []byte("stub"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Sem override das roots confiÁ¡veis, qualquer arquivo em %TEMP%/AppData/ProgramData é rejeitado.
	if _, err := validateUninstallerPath(tempUninstall); err == nil {
		t.Fatalf("esperado erro para uninstaller em tempDir (%s)", tempUninstall)
	}

	// Mesmo quando roots confiÁ¡veis incluem parentDir, ACL insegura deve rejeitar.
	origTrusted := trustedUninstallerRootsFn
	origUntrusted := untrustedUninstallerRootsFn
	origACL := verifyUninstallerACLFn
	t.Cleanup(func() {
		trustedUninstallerRootsFn = origTrusted
		untrustedUninstallerRootsFn = origUntrusted
		verifyUninstallerACLFn = origACL
	})
	trustedUninstallerRootsFn = func() []string { return []string{tempDir} }
	untrustedUninstallerRootsFn = func() []string { return nil }
	verifyUninstallerACLFn = func(string) error { return errors.New("owner nao confiavel") }

	if _, err := validateUninstallerPath(tempUninstall); err == nil {
		t.Fatal("esperado erro quando verifyUninstallerACLFn falha")
	}

	// Diretório explicitamente proibido (ex: ProgramData) sempre rejeitado mesmo se listado em trusted.
	untrustedUninstallerRootsFn = func() []string { return []string{filepath.Dir(tempDir)} }
	verifyUninstallerACLFn = func(string) error { return nil }
	if _, err := validateUninstallerPath(tempUninstall); err == nil {
		t.Fatal("esperado erro quando caminho reside sob untrusted root")
	}
}

func TestResumeAgentDecommissionIfPending_RejectsPoisonedCachePath(t *testing.T) {
	resetDecommissionTestState(t)

	db := database.OpenForTest(t)

	poisonedPath := filepath.Join(testutil.TempDir(t), uninstallerFileName)
	if err := os.WriteFile(poisonedPath, []byte("evil"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := db.CacheSetJSON(agentDecommissionRequestCacheKey, agentDecommissionRequest{
		RequestedAtUTC:  time.Now().UTC().Format(time.RFC3339),
		UninstallerPath: poisonedPath,
	}, time.Hour); err != nil {
		t.Fatalf("CacheSetJSON: %v", err)
	}

	var launched atomic.Bool
	launchAgentUninstaller = func(string) (uint32, error) {
		launched.Store(true)
		return 99, nil
	}

	a := &App{}
	a.Logs.Buffer = logs.New()
	a.CoreAgent.DB = db
	a.resumeAgentDecommissionIfPending()

	time.Sleep(100 * time.Millisecond)
	if launched.Load() {
		t.Fatal("uninstaller envenenado no cache SQLite não deveria ter sido executado")
	}
	var after agentDecommissionRequest
	ok, err := db.CacheGetJSON(agentDecommissionRequestCacheKey, &after)
	if err != nil {
		t.Fatalf("CacheGetJSON: %v", err)
	}
	if ok {
		t.Fatalf("marcador envenenado deveria ter sido removido do cache, mas permaneceu: %+v", after)
	}
}

// resetDecommissionTestState isola as variÁ¡veis de pacote entre testes.
func resetDecommissionTestState(t *testing.T) {
	t.Helper()
	origElevated := agentDecommissionIsElevated
	origResolve := agentDecommissionResolveUninstaller
	origValidate := validateUninstallerPathFn
	origLaunch := launchAgentUninstaller
	origDelay := agentDecommissionLaunchDelay

	agentDecommissionLaunching.Store(false)

	t.Cleanup(func() {
		agentDecommissionIsElevated = origElevated
		agentDecommissionResolveUninstaller = origResolve
		validateUninstallerPathFn = origValidate
		launchAgentUninstaller = origLaunch
		agentDecommissionLaunchDelay = origDelay
		agentDecommissionLaunching.Store(false)
	})
}

func TestHandleAgentDecommissionCommand_RefusesWhenNotElevated(t *testing.T) {
	resetDecommissionTestState(t)
	agentDecommissionIsElevated = func() bool { return false }

	handled, code, _, errText := (&App{}).handleAgentDecommissionCommand(nil, nil)

	if !handled || code == 0 || errText == "" {
		t.Fatalf("esperado recusa por falta de elevaçãoo: handled=%v code=%d err=%q", handled, code, errText)
	}
}

func TestHandleAgentDecommissionCommand_ReportsMissingUninstaller(t *testing.T) {
	resetDecommissionTestState(t)
	agentDecommissionIsElevated = func() bool { return true }
	agentDecommissionResolveUninstaller = func() (string, error) {
		return "", os.ErrNotExist
	}

	launched := make(chan string, 1)
	launchAgentUninstaller = func(path string) (uint32, error) {
		launched <- path
		return 1, nil
	}

	handled, code, _, errText := (&App{}).handleAgentDecommissionCommand(nil, nil)

	if !handled || code == 0 || errText == "" {
		t.Fatalf("esperado erro de uninstaller ausente: handled=%v code=%d err=%q", handled, code, errText)
	}
	select {
	case path := <-launched:
		t.Fatalf("uninstaller não deveria ser lanÁçado (path=%q)", path)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestHandleAgentDecommissionCommand_LaunchesUninstaller(t *testing.T) {
	resetDecommissionTestState(t)
	agentDecommissionIsElevated = func() bool { return true }
	agentDecommissionLaunchDelay = 0
	agentDecommissionResolveUninstaller = func() (string, error) { return "C:/tmp/uninstall.exe", nil }
	validateUninstallerPathFn = func(p string) (string, error) { return p, nil }

	launched := make(chan string, 1)
	launchAgentUninstaller = func(path string) (uint32, error) {
		launched <- path
		return 4242, nil
	}

	handled, code, _, errText := (&App{}).handleAgentDecommissionCommand(nil, map[string]any{"reason": "trash"})
	if !handled || code != 0 || errText != "" {
		t.Fatalf("esperado sucesso: handled=%v code=%d err=%q", handled, code, errText)
	}

	select {
	case path := <-launched:
		if path != "C:/tmp/uninstall.exe" {
			t.Fatalf("path inesperado: %q", path)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("uninstaller não foi lanÁçado")
	}
}

func TestHandleAgentDecommissionCommand_NotElevatedDoesNotLeakLock(t *testing.T) {
	resetDecommissionTestState(t)
	agentDecommissionLaunchDelay = 0
	agentDecommissionResolveUninstaller = func() (string, error) { return "C:/tmp/uninstall.exe", nil }
	validateUninstallerPathFn = func(p string) (string, error) { return p, nil }

	launched := make(chan struct{}, 1)
	launchAgentUninstaller = func(string) (uint32, error) {
		launched <- struct{}{}
		return 1, nil
	}

	app := &App{}
	agentDecommissionIsElevated = func() bool { return false }
	app.handleAgentDecommissionCommand(nil, nil)

	// A recusa por falta de elevaçãoo não pode deixar a trava presa: depois de
	// elevado (ex.: serviÁço subiu como SYSTEM), o pedido precisa funcionar.
	agentDecommissionIsElevated = func() bool { return true }
	app.handleAgentDecommissionCommand(nil, nil)

	select {
	case <-launched:
	case <-time.After(2 * time.Second):
		t.Fatal("trava não liberada após a recusa por falta de elevaçãoo")
	}
}

func TestHandleAgentDecommissionCommand_RetriesAfterLaunchFailure(t *testing.T) {
	resetDecommissionTestState(t)
	agentDecommissionIsElevated = func() bool { return true }
	agentDecommissionLaunchDelay = 0
	agentDecommissionResolveUninstaller = func() (string, error) { return "C:/tmp/uninstall.exe", nil }
	validateUninstallerPathFn = func(p string) (string, error) { return p, nil }

	var attempts atomic.Int32
	attemptDone := make(chan int32, 4)
	launchAgentUninstaller = func(string) (uint32, error) {
		n := attempts.Add(1)
		attemptDone <- n
		if n == 1 {
			return 0, errors.New("CreateProcess falhou (teste)")
		}
		return 7, nil
	}

	app := &App{}
	app.handleAgentDecommissionCommand(nil, nil)

	select {
	case n := <-attemptDone:
		if n != 1 {
			t.Fatalf("esperado tentativa 1, obtido %d", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("primeira tentativa de lançamento não executou")
	}

	// Aguarda runAgentUninstaller liberar agentDecommissionLaunching após retornar erro.
	deadline := time.Now().Add(2 * time.Second)
	for agentDecommissionLaunching.Load() {
		if time.Now().After(deadline) {
			t.Fatal("trava agentDecommissionLaunching não foi liberada após falha")
		}
		time.Sleep(time.Millisecond)
	}

	// A primeira tentativa falhou e liberou a trava: um novo pedido (purge,
	// reentrega ou novo delete no painel) precisa tentar novamente. Sem isso o
	// marcador bloqueava a máquina para sempre.
	app.handleAgentDecommissionCommand(nil, nil)

	select {
	case n := <-attemptDone:
		if n != 2 {
			t.Fatalf("esperado tentativa 2, obtido %d", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("segunda tentativa de lançamento não executou")
	}

	if got := attempts.Load(); got != 2 {
		t.Fatalf("esperado 2 tentativas após falha de lançamento, obtido %d", got)
	}
}

func TestHandleAgentDecommissionCommand_IsIdempotent(t *testing.T) {
	resetDecommissionTestState(t)
	agentDecommissionIsElevated = func() bool { return true }
	agentDecommissionLaunchDelay = 0
	agentDecommissionResolveUninstaller = func() (string, error) { return "C:/tmp/uninstall.exe", nil }
	validateUninstallerPathFn = func(p string) (string, error) { return p, nil }

	var launches atomic.Int32
	launched := make(chan struct{}, 2)
	launchAgentUninstaller = func(string) (uint32, error) {
		launches.Add(1)
		launched <- struct{}{}
		return 1, nil
	}

	app := &App{}
	app.handleAgentDecommissionCommand(nil, nil)
	app.handleAgentDecommissionCommand(nil, nil)

	select {
	case <-launched:
	case <-time.After(5 * time.Second):
		t.Fatal("uninstaller não foi lançado")
	}

	if got := launches.Load(); got != 1 {
		t.Fatalf("esperado exatamente um lançamento, obtido %d", got)
	}
}
