package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
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
	dir := t.TempDir()
	valid := filepath.Join(dir, uninstallerFileName)
	if err := os.WriteFile(valid, []byte("stub"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := validateUninstallerPath(valid)
	if err != nil || got != valid {
		t.Fatalf("validate valid: got=%q err=%v", got, err)
	}

	if _, err := validateUninstallerPath(filepath.Join(dir, "setup.exe")); err == nil {
		t.Fatal("esperado erro para executável com nome inesperado")
	}
	if _, err := validateUninstallerPath(dir); err == nil {
		t.Fatal("esperado erro para diretório")
	}
	if _, err := validateUninstallerPath(filepath.Join(dir, "missing.exe")); err == nil {
		t.Fatal("esperado erro para arquivo inexistente")
	}
}

// resetDecommissionTestState isola as variáveis de pacote entre testes.
func resetDecommissionTestState(t *testing.T) {
	t.Helper()
	origElevated := agentDecommissionIsElevated
	origResolve := agentDecommissionResolveUninstaller
	origLaunch := launchAgentUninstaller
	origDelay := agentDecommissionLaunchDelay

	agentDecommissionLaunching.Store(false)

	t.Cleanup(func() {
		agentDecommissionIsElevated = origElevated
		agentDecommissionResolveUninstaller = origResolve
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
		t.Fatalf("esperado recusa por falta de elevação: handled=%v code=%d err=%q", handled, code, errText)
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
		t.Fatalf("uninstaller não deveria ser lançado (path=%q)", path)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestHandleAgentDecommissionCommand_LaunchesUninstaller(t *testing.T) {
	resetDecommissionTestState(t)
	agentDecommissionIsElevated = func() bool { return true }
	agentDecommissionLaunchDelay = 0
	agentDecommissionResolveUninstaller = func() (string, error) { return "C:/tmp/uninstall.exe", nil }

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
		t.Fatal("uninstaller não foi lançado")
	}
}

func TestHandleAgentDecommissionCommand_NotElevatedDoesNotLeakLock(t *testing.T) {
	resetDecommissionTestState(t)
	agentDecommissionLaunchDelay = 0
	agentDecommissionResolveUninstaller = func() (string, error) { return "C:/tmp/uninstall.exe", nil }

	launched := make(chan struct{}, 1)
	launchAgentUninstaller = func(string) (uint32, error) {
		launched <- struct{}{}
		return 1, nil
	}

	app := &App{}
	agentDecommissionIsElevated = func() bool { return false }
	app.handleAgentDecommissionCommand(nil, nil)

	// A recusa por falta de elevação não pode deixar a trava presa: depois de
	// elevado (ex.: serviço subiu como SYSTEM), o pedido precisa funcionar.
	agentDecommissionIsElevated = func() bool { return true }
	app.handleAgentDecommissionCommand(nil, nil)

	select {
	case <-launched:
	case <-time.After(2 * time.Second):
		t.Fatal("trava não liberada após a recusa por falta de elevação")
	}
}

func TestHandleAgentDecommissionCommand_RetriesAfterLaunchFailure(t *testing.T) {
	resetDecommissionTestState(t)
	agentDecommissionIsElevated = func() bool { return true }
	agentDecommissionLaunchDelay = 0
	agentDecommissionResolveUninstaller = func() (string, error) { return "C:/tmp/uninstall.exe", nil }

	var attempts int
	launchAgentUninstaller = func(string) (uint32, error) {
		attempts++
		if attempts == 1 {
			return 0, errors.New("CreateProcess falhou (teste)")
		}
		return 7, nil
	}

	app := &App{}
	app.handleAgentDecommissionCommand(nil, nil)
	time.Sleep(300 * time.Millisecond)

	// A primeira tentativa falhou e liberou a trava: um novo pedido (purge,
	// reentrega ou novo delete no painel) precisa tentar novamente. Sem isso o
	// marcador bloqueava a máquina para sempre.
	app.handleAgentDecommissionCommand(nil, nil)
	time.Sleep(300 * time.Millisecond)

	if attempts != 2 {
		t.Fatalf("esperado 2 tentativas após falha de lançamento, obtido %d", attempts)
	}
}

func TestHandleAgentDecommissionCommand_IsIdempotent(t *testing.T) {
	resetDecommissionTestState(t)
	agentDecommissionIsElevated = func() bool { return true }
	agentDecommissionLaunchDelay = 0
	agentDecommissionResolveUninstaller = func() (string, error) { return "C:/tmp/uninstall.exe", nil }

	var launches int
	launchAgentUninstaller = func(string) (uint32, error) {
		launches++
		return 1, nil
	}

	app := &App{}
	app.handleAgentDecommissionCommand(nil, nil)
	app.handleAgentDecommissionCommand(nil, nil)

	time.Sleep(300 * time.Millisecond)

	if launches != 1 {
		t.Fatalf("esperado exatamente um lançamento, obtido %d", launches)
	}
}
