package appstore

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"discovery/app/core/database"
)

// valida o loop de paginação do client contra um servidor que fatia a resposta
// (contrato do item 14: a API precisava honrar pageSize/cursor; o agent já
// seguia hasMore/nextCursor).
func TestFetchByInstallationTypeFollowsCursorAndRequestsPaged(t *testing.T) {
	const pageSize = 2000
	total := pageSize + 7

	var requests int
	var sawPaged, sawPageSize string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		q := r.URL.Query()
		if requests == 1 {
			sawPaged = q.Get("paged")
			sawPageSize = q.Get("pageSize")
		}
		cursor := q.Get("cursor")
		start := 0
		if cursor == "page-2" {
			start = pageSize
		}

		items := make([]Item, 0, pageSize)
		for i := start; i < total && len(items) < pageSize; i++ {
			items = append(items, Item{
				InstallationType: "Winget",
				PackageID:        fmt.Sprintf("pkg-%05d", i),
				Name:             "Pacote",
			})
		}
		hasMore := start+len(items) < total
		next := ""
		if hasMore {
			next = "page-2"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Response{
			InstallationType: "Winget",
			Count:            total,
			Items:            items,
			HasMore:          hasMore,
			NextCursor:       next,
		})
	}))
	defer server.Close()

	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	defer db.Close()

	enabled := true
	svc := New(Deps{
		GetDebugConfig: func() DebugConfig {
			return DebugConfig{
				ApiScheme: "http",
				ApiServer: strings.TrimPrefix(server.URL, "http://"),
				AuthToken: "mdz_test-token",
				AgentID:   "019faa6f-8eaa-7c38-9878-36cc96f9be3e",
			}
		},
		GetAgentConfiguration: func() AgentConfiguration { return AgentConfiguration{AppStoreEnabled: &enabled} },
		FeatureEnabled:        func(v *bool) bool { return v == nil || *v },
		Logf:                  func(string) {},
		DB:                    func() *database.DB { return db },
		Cache:                 &PolicyCache{},
	})

	resp, err := svc.FetchByInstallationType(t.Context(), InstallationWinget)
	if err != nil {
		t.Fatalf("FetchByInstallationType: %v", err)
	}
	if len(resp.Items) != total {
		t.Fatalf("itens = %d, want %d (paginação incompleta)", len(resp.Items), total)
	}
	if requests != 2 {
		t.Fatalf("requisições = %d, want 2 (uma por página)", requests)
	}
	if sawPaged != "true" {
		t.Fatalf("paged = %q, want true (o servidor só pagina com opt-in)", sawPaged)
	}
	if sawPageSize == "" || sawPageSize == "200" {
		t.Fatalf("pageSize = %q, want o valor configurado no agent", sawPageSize)
	}
	if resp.Items[len(resp.Items)-1].PackageID != fmt.Sprintf("pkg-%05d", total-1) {
		t.Fatalf("último item = %q", resp.Items[len(resp.Items)-1].PackageID)
	}
}

// servidor que devolve a coleção inteira numa página (comportamento dos
// agents/servidores antigos) precisa continuar funcionando.
func TestFetchByInstallationTypeHandlesLegacySinglePage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Response{
			InstallationType: "Winget",
			Count:            3,
			Items: []Item{
				{InstallationType: "Winget", PackageID: "a"},
				{InstallationType: "Winget", PackageID: "b"},
				{InstallationType: "Winget", PackageID: "c"},
			},
			HasMore: false,
		})
	}))
	defer server.Close()

	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	defer db.Close()

	enabled := true
	svc := New(Deps{
		GetDebugConfig: func() DebugConfig {
			return DebugConfig{
				ApiScheme: "http",
				ApiServer: strings.TrimPrefix(server.URL, "http://"),
				AuthToken: "mdz_test-token",
				AgentID:   "019faa6f-8eaa-7c38-9878-36cc96f9be3e",
			}
		},
		GetAgentConfiguration: func() AgentConfiguration { return AgentConfiguration{AppStoreEnabled: &enabled} },
		FeatureEnabled:        func(v *bool) bool { return v == nil || *v },
		Logf:                  func(string) {},
		DB:                    func() *database.DB { return db },
		Cache:                 &PolicyCache{},
	})

	resp, err := svc.FetchByInstallationType(t.Context(), InstallationWinget)
	if err != nil {
		t.Fatalf("FetchByInstallationType: %v", err)
	}
	if len(resp.Items) != 3 {
		t.Fatalf("itens = %d, want 3", len(resp.Items))
	}
}
