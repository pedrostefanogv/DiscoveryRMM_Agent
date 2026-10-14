package appstore

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"discovery/app/core/database"
)

func buildTestPolicy(n int) EffectivePolicy {
	items := make([]Item, 0, n)
	for i := 0; i < n; i++ {
		items = append(items, Item{
			InstallationType: "Winget",
			PackageID:        fmt.Sprintf("pkg-%05d", i),
			Name:             fmt.Sprintf("Pacote %05d", i),
			Description:      strings.Repeat("d", 400),
			Publisher:        "Publisher",
			Version:          "1.0.0",
		})
	}
	return EffectivePolicy{Items: items, FetchedAt: time.Now().UTC().Format(time.RFC3339)}
}

func newTestService(t *testing.T, db *database.DB) *Service {
	t.Helper()
	enabled := true
	return New(Deps{
		GetDebugConfig: func() DebugConfig { return DebugConfig{} },
		GetAgentConfiguration: func() AgentConfiguration {
			return AgentConfiguration{AppStoreEnabled: &enabled}
		},
		FeatureEnabled: func(v *bool) bool { return v == nil || *v },
		Logf:           func(string) {},
		DB:             func() *database.DB { return db },
		Cache:          &PolicyCache{},
	})
}

func openTestDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestReadPersistedPolicyMigratesLegacyV1ToCompressedV2(t *testing.T) {
	db := openTestDB(t)
	svc := newTestService(t, db)

	policy := buildTestPolicy(5000)
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := db.CacheSet(CacheKey, raw, SQLiteCacheTTL); err != nil {
		t.Fatalf("CacheSet v1: %v", err)
	}

	got, ok := svc.readPersistedPolicy()
	if !ok {
		t.Fatal("readPersistedPolicy deveria encontrar o cache v1")
	}
	if len(got.Items) != len(policy.Items) {
		t.Fatalf("itens = %d, want %d", len(got.Items), len(policy.Items))
	}

	compressed, err := db.CacheGetBytesStale(CacheKeyCompressed)
	if err != nil {
		t.Fatalf("CacheGetBytesStale v2: %v", err)
	}
	if len(compressed) == 0 {
		t.Fatal("migração deveria ter gravado o cache v2")
	}
	if len(compressed) >= len(raw) {
		t.Fatalf("v2 comprimido = %d bytes, deveria ser menor que o v1 (%d)", len(compressed), len(raw))
	}
	gz, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("v2 não é gzip válido: %v", err)
	}
	_ = gz.Close()

	legacy, err := db.CacheGetStale(CacheKey)
	if err != nil {
		t.Fatalf("CacheGetStale v1: %v", err)
	}
	if legacy != nil {
		t.Fatalf("chave v1 deveria ter sido removida após a migração (%d bytes restantes)", len(legacy))
	}
}

func TestReadPersistedPolicyFromCompressedV2(t *testing.T) {
	db := openTestDB(t)
	svc := newTestService(t, db)

	policy := buildTestPolicy(1000)
	if err := svc.persistCompressedPolicy(db, policy); err != nil {
		t.Fatalf("persistCompressedPolicy: %v", err)
	}
	// Sem cache v1 nenhum: leitura deve vir do v2.
	got, ok := svc.readPersistedPolicy()
	if !ok {
		t.Fatal("readPersistedPolicy deveria ler o cache v2")
	}
	if len(got.Items) != 1000 {
		t.Fatalf("itens = %d, want 1000", len(got.Items))
	}
	if got.Items[7].PackageID != "pkg-00007" {
		t.Fatalf("item 7 = %q", got.Items[7].PackageID)
	}
}

func TestLoadEffectivePolicyUsesFreshCompressedCacheWithoutNetwork(t *testing.T) {
	db := openTestDB(t)
	svc := newTestService(t, db)

	policy := buildTestPolicy(2000)
	policy.FetchedAt = time.Now().UTC().Format(time.RFC3339)
	if err := svc.persistCompressedPolicy(db, policy); err != nil {
		t.Fatalf("persistCompressedPolicy: %v", err)
	}

	// Sem API configurada: um fetch seria impossível (erro). O caminho quente
	// deve vir do cache persistido comprimido.
	loaded, err := svc.LoadEffectivePolicy(t.Context(), false)
	if err != nil {
		t.Fatalf("LoadEffectivePolicy: %v", err)
	}
	if len(loaded.Items) != 2000 {
		t.Fatalf("itens = %d, want 2000", len(loaded.Items))
	}
	if !svc.cache.loaded {
		t.Fatal("política deveria ter sido promovida para o cache de memória")
	}
}

func TestPolicyCacheReleasesExpiredPolicy(t *testing.T) {
	cache := &PolicyCache{}
	cache.Set(buildTestPolicy(10))
	if _, ok := cache.Get(time.Nanosecond); ok {
		// Pode passar se a resolução do relógio não marcou expiração; força.
		time.Sleep(2 * time.Millisecond)
		if _, ok := cache.Get(time.Nanosecond); ok {
			t.Fatal("cache deveria estar expirado")
		}
	}
	if cache.loaded {
		t.Fatal("cache deveria ter sido liberado após a expiração")
	}
	if len(cache.policy.Items) != 0 {
		t.Fatal("itens deveriam ter sido liberados (evita reter ~30 MB indefinidamente)")
	}
}

// BenchmarkLoadEffectivePolicyFromCompressedCache mede o custo do caminho
// quente (ler + decodificar a política persistida) com um volume realista de
// itens. Serve de gate da otimização de RAM: o pico deve ficar próximo do
// tamanho do objeto decodificado, sem a cópia dupla do JSON em texto.
func BenchmarkLoadEffectivePolicyFromCompressedCache(b *testing.B) {
	db, err := database.Open(b.TempDir())
	if err != nil {
		b.Fatalf("database.Open: %v", err)
	}
	defer db.Close()

	enabled := true
	svc := New(Deps{
		GetDebugConfig:        func() DebugConfig { return DebugConfig{} },
		GetAgentConfiguration: func() AgentConfiguration { return AgentConfiguration{AppStoreEnabled: &enabled} },
		FeatureEnabled:        func(v *bool) bool { return v == nil || *v },
		Logf:                  func(string) {},
		DB:                    func() *database.DB { return db },
		Cache:                 &PolicyCache{},
	})

	policy := buildTestPolicy(27374)
	if err := svc.persistCompressedPolicy(db, policy); err != nil {
		b.Fatalf("persistCompressedPolicy: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		svc.cache.Invalidate()
		if _, err := svc.LoadEffectivePolicy(b.Context(), false); err != nil {
			b.Fatalf("LoadEffectivePolicy: %v", err)
		}
	}
}
