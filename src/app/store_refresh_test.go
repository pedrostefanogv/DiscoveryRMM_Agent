package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"discovery/app/core/models"
	"discovery/app/debug"
)

// TestRefreshCatalogForcesServerFetch cobre o fluxo do botão "atualizar" da
// loja: depois que a política já está em cache, GetCatalog continua servindo o
// cache (um pacote custom recém-cadastrado não aparece), e só RefreshCatalog
// força um fetch novo que o traz. Sem essa distinção, um modelo custom podia
// demorar até o próximo sync de background para aparecer no agent.
func TestRefreshCatalogForcesServerFetch(t *testing.T) {
	const token = "mdz_test_token"
	var customAvailable atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agent-auth/me/app-store" {
			t.Fatalf("path inesperado: %s", r.URL.Path)
		}

		switch r.URL.Query().Get("installationType") {
		case "Winget":
			_, _ = w.Write([]byte(`{"installationType":"Winget","count":1,"items":[{"installationType":"Winget","packageId":"Google.Chrome","name":"Google Chrome"}]}`))
		case "Chocolatey":
			_, _ = w.Write([]byte(`{"installationType":"Chocolatey","count":0,"items":[]}`))
		case "Custom":
			if customAvailable.Load() {
				_, _ = w.Write([]byte(`{"installationType":"Custom","count":1,"items":[{"installationType":"Custom","packageId":"foxitedito","name":"Foxit editor","description":"Editor de PDF","installCommand":"{{installer}}\\Setup.exe /install /quiet /norestart","fileName":"Setup.exe","fileUrl":"/api/v1/agent-auth/me/app-store/custom/foxitedito/file"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"installationType":"Custom","count":0,"items":[]}`))
		default:
			t.Fatalf("installationType inesperado: %s", r.URL.Query().Get("installationType"))
		}
	}))
	defer server.Close()

	app := newTestAppStoreApp()
	app.DebugSvc = debug.NewService(debug.Options{})
	app.DebugSvc.ApplyRuntimeConnectionConfig("http", strings.TrimPrefix(server.URL, "http://"), token, "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7", "", "")

	ctx := context.Background()

	// 1º carregamento: popula o cache sem o pacote custom.
	initial, err := app.getCatalogFromAppStore(ctx)
	if err != nil {
		t.Fatalf("getCatalogFromAppStore inicial falhou: %v", err)
	}
	if catalogHasPackage(initial, "foxitedito") {
		t.Fatal("o pacote custom não deveria existir no primeiro carregamento")
	}

	// O servidor passa a publicar o modelo custom.
	customAvailable.Store(true)

	// GetCatalog continua no cache (comportamento do antigo botão "atualizar").
	cached, err := app.getCatalogFromAppStore(ctx)
	if err != nil {
		t.Fatalf("getCatalogFromAppStore em cache falhou: %v", err)
	}
	if catalogHasPackage(cached, "foxitedito") {
		t.Fatal("GetCatalog deveria servir o cache e ainda não ver o pacote custom")
	}

	// RefreshCatalog ignora o cache e traz o pacote custom.
	refreshed, err := app.refreshCatalogFromAppStore(ctx)
	if err != nil {
		t.Fatalf("refreshCatalogFromAppStore falhou: %v", err)
	}
	if !catalogHasPackage(refreshed, "foxitedito") {
		t.Fatalf("RefreshCatalog deveria trazer o pacote custom; itens: %d", len(refreshed.Packages))
	}

	var custom *models.AppItem
	for i := range refreshed.Packages {
		if refreshed.Packages[i].ID == "foxitedito" {
			custom = &refreshed.Packages[i]
			break
		}
	}
	if custom == nil {
		t.Fatal("item custom ausente")
	}
	if custom.Name != "Foxit editor" {
		t.Fatalf("nome do item custom: esperava 'Foxit editor', veio %q", custom.Name)
	}
	if custom.InstallationType != "Custom" {
		t.Fatalf("installationType: esperava Custom, veio %q", custom.InstallationType)
	}
	if custom.InstallCommand == "" {
		t.Fatal("installCommand deveria vir do servidor")
	}
}

func catalogHasPackage(catalog models.Catalog, id string) bool {
	for _, pkg := range catalog.Packages {
		if pkg.ID == id {
			return true
		}
	}
	return false
}
