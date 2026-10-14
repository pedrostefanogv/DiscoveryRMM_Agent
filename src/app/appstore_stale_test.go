package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"discovery/app/core/database"
)

// TestLoadEffectiveAppStorePolicyServesStaleWhenFetchFails valida o fallback
// stale-on-error: com a API fora do ar (config incompleta no teste) e a
// política persistida no SQLite EXPIRADA, a loja serve o cache stale em vez
// de falhar ("se a API estiver fora, ele não carrega os dados da loja").
func TestLoadEffectiveAppStorePolicyServesStaleWhenFetchFails(t *testing.T) {
	app := newTestAppStoreApp()

	// Sem DebugSvc/config: FetchByInstallationType falha com "configuração de
	// servidor API incompleta" — exatamente o cenário reportado.
	app.DebugSvc = nil

	// DB real com política persistida e EXPIRADA (fetchedAt 8 dias atrás).
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatalf("falha ao abrir database: %v", err)
	}
	defer db.Close()
	app.CoreAgent.DB = db

	stalePolicy := AppStoreEffectivePolicy{
		Items: []AppStoreItem{
			{InstallationType: "Winget", PackageID: "Google.Chrome", Name: "Google Chrome"},
			{InstallationType: "Chocolatey", PackageID: "googlechrome", Name: "Google Chrome"},
		},
		FetchedAt: time.Now().UTC().Add(-8 * 24 * time.Hour).Format(time.RFC3339),
	}
	if err := db.CacheSetJSON("app_store_effective", stalePolicy, -1*time.Hour); err != nil {
		t.Fatalf("falha ao persistir política stale: %v", err)
	}

	policy, err := app.loadEffectiveAppStorePolicy(context.Background(), false)
	if err != nil {
		t.Fatalf("esperava fallback stale sem erro, recebeu: %v", err)
	}
	if len(policy.Items) != 2 {
		t.Fatalf("esperava 2 itens do cache stale, recebeu %d", len(policy.Items))
	}
	if policy.FetchedAt != stalePolicy.FetchedAt {
		t.Fatalf("FetchedAt stale preservado: esperava %q, recebeu %q", stalePolicy.FetchedAt, policy.FetchedAt)
	}

	// A entrada stale deve continuar LEGÍVEL no SQLite (não pode ser destruída
	// pela leitura). Com a migração para o formato comprimido, o dado pode estar
	// em app_store_effective_v2 — o que importa é o fallback sobreviver.
	if _, ok, _ := readStaleForTest(db); !ok {
		t.Fatal("entrada stale não deve ser apagada pelo fallback")
	}

	// Prova de que a migração preservou o fallback: uma segunda leitura, agora
	// sem cache de memória, continua servindo os 2 itens stale sem tocar a rede.
	app.AppStorePolicy.Invalidate()
	policyAfterMigration, err := app.loadEffectiveAppStorePolicy(context.Background(), false)
	if err != nil {
		t.Fatalf("fallback stale falhou após a migração do cache: %v", err)
	}
	if len(policyAfterMigration.Items) != 2 {
		t.Fatalf("esperava 2 itens do cache stale após a migração, recebeu %d", len(policyAfterMigration.Items))
	}
}

// readStaleForTest procura a política stale nos dois formatos de cache: v2
// (gzip, formato atual) e v1 (JSON texto, legado em migração).
func readStaleForTest(db *database.DB) ([]byte, bool, error) {
	if data, err := db.CacheGetBytesStale("app_store_effective_v2"); err != nil {
		return nil, false, err
	} else if len(data) > 0 {
		return data, true, nil
	}

	data, err := db.CacheGetStale("app_store_effective")
	if err != nil || len(data) == 0 {
		return nil, false, err
	}
	return data, true, nil
}

// TestLoadEffectiveAppStorePolicyFailsWithoutAnyCache garante que, sem cache
// nenhum (nem stale), o erro original é propagado (comportamento antigo).
func TestLoadEffectiveAppStorePolicyFailsWithoutAnyCache(t *testing.T) {
	app := newTestAppStoreApp()
	app.DebugSvc = nil

	_, err := app.loadEffectiveAppStorePolicy(context.Background(), false)
	if err == nil {
		t.Fatal("esperava erro com config incompleta e sem cache nenhum")
	}
	if !strings.Contains(err.Error(), "configuração de servidor API incompleta") {
		t.Fatalf("erro inesperado: %v", err)
	}
}
