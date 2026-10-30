package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"discovery/app/appstore"
	"discovery/app/core/automation"
	"discovery/app/core/database"
	"discovery/app/debug"
)

// newTestAppStoreApp cria um App com o appStoreSvc inicializado para testes.
func newTestAppStoreApp() *App {
	app := &App{ctx: context.Background()}
	app.AppStoreSvc = appstore.New(appstore.Deps{
		GetDebugConfig: func() appstore.DebugConfig {
			cfg := app.GetDebugConfig()
			return appstore.DebugConfig{
				ApiScheme: cfg.ApiScheme,
				ApiServer: cfg.ApiServer,
				AuthToken: cfg.AuthToken,
				AgentID:   cfg.AgentID,
			}
		},
		GetAgentConfiguration: func() appstore.AgentConfiguration {
			cfg := app.GetAgentConfiguration()
			return appstore.AgentConfiguration{AppStoreEnabled: cfg.AppStoreEnabled}
		},
		FeatureEnabled: app.featureEnabled,
		Logf: func(line string) {
			app.Logs.Append(line)
		},
		DB: func() *database.DB {
			return app.CoreAgent.DB
		},
		Cache: app.AppStorePolicy.CachePointer(),
	})
	return app
}

func TestLoadEffectiveAppStorePolicyMergesWingetAndChocolatey(t *testing.T) {
	const token = "mdz_test_token"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Fatalf("Authorization inválido: %q", got)
		}
		if got := r.Header.Get("X-Agent-ID"); got != "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7" {
			t.Fatalf("X-Agent-ID inválido: %q", got)
		}
		if r.URL.Path != "/api/v1/agent-auth/me/app-store" {
			t.Fatalf("path inesperado: %s", r.URL.Path)
		}

		typ := r.URL.Query().Get("installationType")
		switch typ {
		case "Winget":
			_, _ = w.Write([]byte(`{"installationType":"Winget","count":1,"items":[{"installationType":"Winget","packageId":"Google.Chrome","name":"Google Chrome"}]}`))
		case "Chocolatey":
			_, _ = w.Write([]byte(`{"installationType":"Chocolatey","count":1,"items":[{"installationType":"Chocolatey","packageId":"googlechrome","name":"Google Chrome"}]}`))
		case "Custom":
			// O fetch de Custom é tolerante (o servidor pode não suportar
			// metadados completos de apps custom): responde vazio em vez de
			// falhar o teste no handler.
			_, _ = w.Write([]byte(`{"installationType":"Custom","count":0,"items":[]}`))
		default:
			t.Fatalf("installationType inesperado: %s", typ)
		}
	}))
	defer server.Close()

	app := newTestAppStoreApp()
	app.DebugSvc = debug.NewService(debug.Options{})
	app.DebugSvc.ApplyRuntimeConnectionConfig("http", strings.TrimPrefix(server.URL, "http://"), token, "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7", "", "")

	policy, err := app.loadEffectiveAppStorePolicy(context.Background(), true)
	if err != nil {
		t.Fatalf("loadEffectiveAppStorePolicy erro: %v", err)
	}
	if len(policy.Items) != 2 {
		t.Fatalf("esperava 2 itens, recebeu %d", len(policy.Items))
	}
	if policy.FetchedAt == "" {
		t.Fatalf("FetchedAt deve ser preenchido")
	}
}

func TestResolveAllowedPackageDetectsAmbiguousPackageID(t *testing.T) {
	app := newTestAppStoreApp()
	app.AppStorePolicy.Set(AppStoreEffectivePolicy{
		Items: []AppStoreItem{
			{InstallationType: "Winget", PackageID: "Duplicate.Package"},
			{InstallationType: "Chocolatey", PackageID: "duplicate.package"},
		},
	})

	_, err := app.resolveAllowedPackage(context.Background(), "duplicate.package")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "ambíguo") {
		t.Fatalf("esperava erro de pacote ambíguo, recebeu: %v", err)
	}
}

func TestAuthorizeAutomationPackageUninstallBypassesPolicy(t *testing.T) {
	app := newTestAppStoreApp()
	if err := app.authorizeAutomationPackage(context.Background(), "Winget", "Any.Package", "uninstall"); err != nil {
		t.Fatalf("uninstall deve bypass da policy nesta rodada: %v", err)
	}
}

func TestFindAllowedPackageFailsWhenConfigIncomplete(t *testing.T) {
	app := newTestAppStoreApp()
	_, err := app.findAllowedPackage(context.Background(), "Winget", "Google.Chrome")
	if err == nil {
		t.Fatalf("esperava erro quando config está incompleta")
	}
}

// newTestAutomationService cria um automation.Service com o estado em memória
// já populado (uso do override de policy em authorizeAutomationPackage).
func newTestAutomationService(tasks ...automation.AutomationTask) *automation.Service {
	svc := automation.NewService(func() automation.RuntimeConfig {
		return automation.RuntimeConfig{AgentID: "agent-test"}
	}, nil)
	svc.SetStateForTesting(automation.State{Available: true, Tasks: tasks})
	return svc
}

// Regra de produto: a policy de automação SOBREPÕE a App Store — task ativa
// (install) com pacote fora da política da Loja é autorizado pelo override.
func TestAuthorizeAutomationPackageOverrideByAutomationPolicy(t *testing.T) {
	app := newTestAppStoreApp()
	app.AutomationSvc = newTestAutomationService(automation.AutomationTask{
		TaskID:           "01a18960",
		Name:             "Bleach",
		ActionType:       automation.ActionInstallPackage,
		InstallationType: automation.InstallationWinget,
		PackageID:        "bleachbit.bleachbit",
		LastUpdatedAt:    "2026-10-29T00:00:00Z",
	})
	if err := app.authorizeAutomationPackage(context.Background(), "Winget", "bleachbit.bleachbit", "install"); err != nil {
		t.Fatalf("override da policy de automação deveria autorizar o pacote: %v", err)
	}
}

// UpdateOrInstallPackage cobre as operações install e upgrade.
func TestAuthorizeAutomationPackageOverrideCoversUpgrade(t *testing.T) {
	app := newTestAppStoreApp()
	app.AutomationSvc = newTestAutomationService(automation.AutomationTask{
		TaskID:           "01a17a4f",
		Name:             "Thunder",
		ActionType:       automation.ActionUpdateOrInstallPackage,
		InstallationType: automation.InstallationWinget,
		PackageID:        "mozilla.thunderbird.br",
		LastUpdatedAt:    "2026-10-29T00:00:00Z",
	})
	for _, op := range []string{"install", "upgrade"} {
		if err := app.authorizeAutomationPackage(context.Background(), "Winget", "mozilla.thunderbird.br", op); err != nil {
			t.Fatalf("operação %q deveria ser autorizada pela policy: %v", op, err)
		}
	}
}

// Pacote FORA da policy de automação continua bloqueado pelo gate da Loja
// (exit 13 no executor).
func TestAuthorizeAutomationPackageOutsidePolicyStillBlocked(t *testing.T) {
	app := newTestAppStoreApp()
	app.AutomationSvc = newTestAutomationService(automation.AutomationTask{
		TaskID:           "task-1",
		Name:             "Chrome",
		ActionType:       automation.ActionInstallPackage,
		InstallationType: automation.InstallationWinget,
		PackageID:        "google.chrome",
		LastUpdatedAt:    "2026-10-29T00:00:00Z",
	})
	app.AppStorePolicy.Set(AppStoreEffectivePolicy{
		Items: []AppStoreItem{{InstallationType: "Winget", PackageID: "Google.Chrome"}},
	})
	err := app.authorizeAutomationPackage(context.Background(), "Winget", "bleachbit.bleachbit", "install")
	if err == nil || !strings.Contains(err.Error(), "não autorizado") {
		t.Fatalf("pacote fora da policy deve continuar bloqueado, recebeu: %v", err)
	}
}
