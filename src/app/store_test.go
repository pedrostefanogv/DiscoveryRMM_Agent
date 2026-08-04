package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"discovery/app/appstore"
	"discovery/app/core/database"
	"discovery/app/debug"
)

// newTestAppStoreApp cria um App com o appStoreSvc inicializado para testes.
func newTestAppStoreApp() *App {
	app := &App{ctx: context.Background()}
	app.appStoreSvc = appstore.New(appstore.Deps{
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
			app.logs.append(line)
		},
		DB: func() *database.DB {
			return app.db
		},
		Cache: &app.appStorePolicy.inner,
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
		default:
			t.Fatalf("installationType inesperado: %s", typ)
		}
	}))
	defer server.Close()

	app := newTestAppStoreApp()
	app.debugSvc = debug.NewService(debug.Options{})
	app.debugSvc.ApplyRuntimeConnectionConfig("http", strings.TrimPrefix(server.URL, "http://"), token, "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7", "", "")

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
	app.appStorePolicy.set(AppStoreEffectivePolicy{
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
