package database

import "testing"

// Garante que o teto de page cache do SQLite está efetivamente aplicado pela
// DSN (e não apenas declarado no código): era 64 MB num Exec avulso e passou
// para 8 MB no DSN, valendo para toda conexão do pool.
func TestOpenAppliesCacheSizePragma(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	var cacheSize int
	if err := db.conn.QueryRow("PRAGMA cache_size").Scan(&cacheSize); err != nil {
		t.Fatalf("PRAGMA cache_size: %v", err)
	}
	if cacheSize != -8000 {
		t.Fatalf("cache_size = %d, want -8000 (8 MB)", cacheSize)
	}

	var journalMode string
	if err := db.conn.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}
}
