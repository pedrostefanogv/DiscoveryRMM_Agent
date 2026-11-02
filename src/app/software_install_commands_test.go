package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	appinventory "discovery/app/inventory"
)

// fakeAppsForInstall implementa appinventory.AppsService para os testes do
// comando remoto softwareinstall (a App usa *inventory.Service concreto, sem
// como de injetar um fake direto no campo).
type fakeAppsForInstall struct {
	installOut string
	installErr error
}

func (f *fakeAppsForInstall) Install(_ context.Context, _ string) (string, error) {
	return f.installOut, f.installErr
}

func (f *fakeAppsForInstall) Uninstall(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (f *fakeAppsForInstall) Upgrade(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (f *fakeAppsForInstall) UpgradeAll(_ context.Context) (string, error) {
	return "", nil
}

func (f *fakeAppsForInstall) ListInstalled(_ context.Context) (string, error) {
	return "", nil
}

func newInstallTestInventory(apps appinventory.AppsService) *appinventory.Service {
	return appinventory.NewService(appinventory.Options{
		Apps:          apps,
		Ctx:           func() context.Context { return context.Background() },
		BeginActivity: func(string) func() { return func() {} },
		Logf:          func(string) {},
	})
}

func TestIsSoftwareInstallCommandType(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"softwareinstall", true},
		{"SoftwareInstall", true},
		{"software-install", true},
		{"software_install", true},
		{" powershell ", false},
		{"systeminfo", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsSoftwareInstallCommandType(tc.in); got != tc.want {
			t.Fatalf("IsSoftwareInstallCommandType(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestHandleSoftwareInstallCommandSuccess(t *testing.T) {
	app := &App{}
	app.InventorySvc = newInstallTestInventory(&fakeAppsForInstall{installOut: "instalado com sucesso"})

	payload := map[string]any{"packageId": "  Git.Git  ", "installationType": "winget", "source": "winget"}
	handled, code, body, errMsg := app.handleSoftwareInstallCommand(context.Background(), payload)
	if !handled {
		t.Fatal("esperava handled=true")
	}
	if code != 0 {
		t.Fatalf("code = %d, want 0 (errMsg=%q body=%q)", code, errMsg, body)
	}
	if errMsg != "" {
		t.Fatalf("errMsg = %q, want vazio", errMsg)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("body não é JSON válido: %v (%q)", err, body)
	}
	if parsed["success"] != true {
		t.Fatalf("success = %v, want true", parsed["success"])
	}
	if parsed["packageId"] != "Git.Git" {
		t.Fatalf("packageId = %v, want %q (TrimSpace esperado)", parsed["packageId"], "Git.Git")
	}
	if parsed["installationType"] != "winget" {
		t.Fatalf("installationType = %v, want %q", parsed["installationType"], "winget")
	}
}

func TestHandleSoftwareInstallCommandFallbackToSource(t *testing.T) {
	app := &App{}
	app.InventorySvc = newInstallTestInventory(&fakeAppsForInstall{installOut: "ok"})

	payload := map[string]any{"packageId": "7zip", "source": "winget"}
	handled, code, body, errMsg := app.handleSoftwareInstallCommand(context.Background(), payload)
	if !handled {
		t.Fatal("esperava handled=true")
	}
	if code != 0 {
		t.Fatalf("code = %d, want 0 (errMsg=%q body=%q)", code, errMsg, body)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("body não é JSON válido: %v (%q)", err, body)
	}
	if parsed["installationType"] != "winget" {
		t.Fatalf("installationType = %v, want %q (fallback do campo source)", parsed["installationType"], "winget")
	}
}

func TestHandleSoftwareInstallCommandMissingPackageId(t *testing.T) {
	app := &App{}
	app.InventorySvc = newInstallTestInventory(&fakeAppsForInstall{})

	payload := map[string]any{"installationType": "winget"}
	handled, code, _, errMsg := app.handleSoftwareInstallCommand(context.Background(), payload)
	if !handled {
		t.Fatal("esperava handled=true")
	}
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if errMsg != "campo 'packageId' é obrigatório" {
		t.Fatalf("errMsg = %q, want %q", errMsg, "campo 'packageId' é obrigatório")
	}
}

func TestHandleSoftwareInstallCommandInvalidPayload(t *testing.T) {
	app := &App{}

	handled, code, _, errMsg := app.handleSoftwareInstallCommand(context.Background(), "{payload inválido")
	if !handled {
		t.Fatal("esperava handled=true")
	}
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.HasPrefix(errMsg, "payload softwareinstall inválido:") {
		t.Fatalf("errMsg = %q, want prefixo %q", errMsg, "payload softwareinstall inválido:")
	}
}

func TestHandleSoftwareInstallCommandNilInventory(t *testing.T) {
	app := &App{}

	payload := map[string]any{"packageId": "Git.Git", "installationType": "winget"}
	handled, code, _, errMsg := app.handleSoftwareInstallCommand(context.Background(), payload)
	if !handled {
		t.Fatal("esperava handled=true")
	}
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if errMsg != "serviço de inventário indisponível" {
		t.Fatalf("errMsg = %q, want %q", errMsg, "serviço de inventário indisponível")
	}
}

func TestHandleSoftwareInstallCommandInstallError(t *testing.T) {
	app := &App{}
	app.InventorySvc = newInstallTestInventory(&fakeAppsForInstall{installErr: errors.New("winget falhou")})

	payload := map[string]any{"packageId": "Git.Git", "installationType": "winget"}
	handled, code, _, errMsg := app.handleSoftwareInstallCommand(context.Background(), payload)
	if !handled {
		t.Fatal("esperava handled=true")
	}
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if errMsg != "winget falhou" {
		t.Fatalf("errMsg = %q, want %q", errMsg, "winget falhou")
	}
}
