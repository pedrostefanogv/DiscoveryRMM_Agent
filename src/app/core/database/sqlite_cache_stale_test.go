package database

import (
	"testing"
	"time"
)

// TestCacheGetStaleReturnsExpiredValue valida o fallback stale-on-error:
// CacheGetStale devolve o valor MESMO com expires_at no passado e NÃO apaga a
// entrada (diferente de CacheGet, que deleta expirados na leitura).
func TestCacheGetStaleReturnsExpiredValue(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("falha ao abrir database: %v", err)
	}
	defer db.Close()

	if err := db.CacheSet("stale-key", []byte(`{"items":[1,2]}`), time.Hour); err != nil {
		t.Fatalf("falha ao gravar cache: %v", err)
	}
	// Expira manualmente (mesmo pacote): ttl negativo no CacheSet significaria
	// "sem expiração", então forçamos expires_at no passado via SQL.
	past := time.Now().Add(-time.Hour).Unix()
	if _, err := db.conn.Exec("UPDATE cache SET expires_at = ? WHERE key = ?", past, "stale-key"); err != nil {
		t.Fatalf("falha ao expirar entrada: %v", err)
	}

	// CacheGet (comportamento antigo): expirado → apagado, não encontrado.
	found, err := db.CacheGetJSON("stale-key", &map[string]any{})
	if err != nil {
		t.Fatalf("CacheGetJSON erro: %v", err)
	}
	if found {
		t.Fatal("CacheGetJSON não deveria encontrar entrada expirada")
	}

	// Reinsere e expira de novo; CacheGetStale deve retornar o valor mesmo
	// expirado e preservar a entrada.
	if err := db.CacheSet("stale-key", []byte(`{"items":[1,2]}`), time.Hour); err != nil {
		t.Fatalf("falha ao gravar cache: %v", err)
	}
	if _, err := db.conn.Exec("UPDATE cache SET expires_at = ? WHERE key = ?", past, "stale-key"); err != nil {
		t.Fatalf("falha ao expirar entrada: %v", err)
	}
	data, err := db.CacheGetStale("stale-key")
	if err != nil {
		t.Fatalf("CacheGetStale erro: %v", err)
	}
	if string(data) != `{"items":[1,2]}` {
		t.Fatalf("valor inesperado: %q", string(data))
	}

	// A entrada não pode ter sido apagada pela leitura stale.
	data2, err := db.CacheGetStale("stale-key")
	if err != nil {
		t.Fatalf("CacheGetStale (2a leitura) erro: %v", err)
	}
	if len(data2) == 0 {
		t.Fatal("CacheGetStale não deve deletar a entrada na leitura")
	}
}

// TestCacheGetStaleMissingKey valida (nil, nil) para chave inexistente.
func TestCacheGetStaleMissingKey(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("falha ao abrir database: %v", err)
	}
	defer db.Close()

	data, err := db.CacheGetStale("nao-existe")
	if err != nil {
		t.Fatalf("CacheGetStale erro: %v", err)
	}
	if data != nil {
		t.Fatalf("esperava nil para chave inexistente, recebeu %q", string(data))
	}
}
