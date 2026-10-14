package appstore

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"discovery/app/core/database"
)

// serverAppStore sobe um servidor que devolve a política efetiva em uma página
// (modo legado) e conta as requisições.
func serverAppStore(t *testing.T, policy EffectivePolicy) (*httptest.Server, *int) {
	t.Helper()
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Response{
			InstallationType: "Winget",
			Count:            len(policy.Items),
			Items:            policy.Items,
			HasMore:          false,
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func newServiceWithServer(t *testing.T, db *database.DB, srv *httptest.Server) *Service {
	t.Helper()
	enabled := true
	return New(Deps{
		GetDebugConfig: func() DebugConfig {
			return DebugConfig{
				ApiScheme: "http",
				ApiServer: strings.TrimPrefix(srv.URL, "http://"),
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
}

// Bug 1 (medido no banco real: as duas chaves convivendo, 31,6 MB + 4,9 MB): o
// caminho de FETCH gravava o v2 e deixava o v1 legado para sempre, porque só a
// migração de leitura apagava a chave antiga.
func TestFetchDeletesLegacyV1CacheKey(t *testing.T) {
	db := openTestDB(t)
	policy := buildTestPolicy(20)
	srv, _ := serverAppStore(t, policy)
	svc := newServiceWithServer(t, db, srv)

	if err := db.CacheSet(CacheKey, []byte(`{"items":[{"packageId":"legado"}],"fetchedAt":"x"}`), SQLiteCacheTTL); err != nil {
		t.Fatalf("semear v1: %v", err)
	}

	if _, err := svc.LoadEffectivePolicy(t.Context(), true); err != nil {
		t.Fatalf("LoadEffectivePolicy: %v", err)
	}

	legacy, err := db.CacheGetStale(CacheKey)
	if err != nil {
		t.Fatalf("CacheGetStale: %v", err)
	}
	if legacy != nil {
		t.Fatalf("chave v1 deveria ser removida após o fetch (%d bytes restantes)", len(legacy))
	}
	compressed, err := db.CacheGetBytesStale(CacheKeyCompressed)
	if err != nil || len(compressed) == 0 {
		t.Fatalf("v2 deveria existir após o fetch (err=%v bytes=%d)", err, len(compressed))
	}
	if _, ok := svc.readPersistedMeta(db); !ok {
		t.Fatal("meta do cache v2 deveria ter sido gravado")
	}
}

// Bug 3: o TTL de 30 min do cache de memória re-lia e re-parseava o blob
// inteiro (27k itens, ~200 MB alocados) mesmo quando a revisão do disco era a
// mesma. Com a meta, o frescor passa a ser decidido por um registro minúsculo.
func TestLoadEffectivePolicyUsesMetaFastPathWithoutReparsingBlob(t *testing.T) {
	db := openTestDB(t)
	svc := newTestService(t, db)
	policy := buildTestPolicy(500)
	if err := svc.persistCompressedPolicy(db, policy); err != nil {
		t.Fatalf("persistCompressedPolicy: %v", err)
	}

	if _, err := svc.LoadEffectivePolicy(t.Context(), false); err != nil {
		t.Fatalf("primeira carga: %v", err)
	}

	// Aqui o blob persistido é DESTRUÍDO, mas a meta (hash) continua apontando
	// para a revisão residente: se o agente relesse o blob, falharia.
	if err := db.CacheSetBytes(CacheKeyCompressed, []byte("nao-e-gzip"), SQLiteCacheTTL); err != nil {
		t.Fatalf("corromper v2: %v", err)
	}
	svc.cache.mu.Lock()
	svc.cache.loadedAt = time.Now().Add(-2 * time.Hour) // simula o TTL vencido
	svc.cache.mu.Unlock()

	loaded, err := svc.LoadEffectivePolicy(t.Context(), false)
	if err != nil {
		t.Fatalf("caminho rápido deveria servir a política residente: %v", err)
	}
	if len(loaded.Items) != 500 {
		t.Fatalf("itens = %d, want 500", len(loaded.Items))
	}
}

// Contraprova do caminho rápido: quando a revisão do disco MUDA, o agente
// precisa reler (e falhar, aqui, porque o blob está corrompido).
func TestLoadEffectivePolicyRereadsWhenPersistedRevisionChanges(t *testing.T) {
	db := openTestDB(t)
	svc := newTestService(t, db)
	if err := svc.persistCompressedPolicy(db, buildTestPolicy(50)); err != nil {
		t.Fatalf("persistCompressedPolicy: %v", err)
	}
	if _, err := svc.LoadEffectivePolicy(t.Context(), false); err != nil {
		t.Fatalf("primeira carga: %v", err)
	}

	if err := db.CacheSetBytes(CacheKeyCompressed, []byte("nao-e-gzip"), SQLiteCacheTTL); err != nil {
		t.Fatalf("corromper v2: %v", err)
	}
	// Meta com hash diferente da revisão residente.
	meta := fmt.Sprintf(`{"hash":"outra-revisao","items":%d}`, 1)
	if err := db.CacheSet(CacheKeyMeta, []byte(meta), SQLiteCacheTTL); err != nil {
		t.Fatalf("meta: %v", err)
	}
	svc.cache.mu.Lock()
	svc.cache.loadedAt = time.Now().Add(-2 * time.Hour)
	svc.cache.mu.Unlock()

	_, err := svc.LoadEffectivePolicy(t.Context(), false)
	if err == nil {
		t.Fatal("revisão diferente deveria forçar releitura (e falhar com blob corrompido)")
	}
}
