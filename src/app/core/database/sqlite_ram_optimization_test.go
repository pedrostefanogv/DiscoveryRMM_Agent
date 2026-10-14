package database

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestCacheSetBytesRoundTripAndStaleRead(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Bytes binários reais (gzip tem 0x00/0xff), não texto válido em UTF-8.
	payload := []byte{0x1f, 0x8b, 0x08, 0x00, 0xff, 0x00, 'a', 'b', 0xc3, 0x28}

	if err := db.CacheSetBytes("blob", payload, 7*24*time.Hour); err != nil {
		t.Fatalf("CacheSetBytes: %v", err)
	}
	got, err := db.CacheGetBytesStale("blob")
	if err != nil {
		t.Fatalf("CacheGetBytesStale: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("roundtrip = %v, want %v", got, payload)
	}

	// Expirado continua legível pelo caminho stale (fallback offline).
	if err := db.CacheSetBytes("expired", payload, -1*time.Hour); err != nil {
		t.Fatalf("CacheSetBytes expirado: %v", err)
	}
	if expired, err := db.CacheGetBytesStale("expired"); err != nil || !bytes.Equal(expired, payload) {
		t.Fatalf("CacheGetBytesStale expirado = %v, err = %v", expired, err)
	}
	if missing, err := db.CacheGetBytesStale("nao-existe"); err != nil || missing != nil {
		t.Fatalf("chave inexistente = %v, err = %v", missing, err)
	}
}

func TestCleanOldInventoryKeepingLatestPreservesNewestPerAgent(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	insert := func(agent string, ageDays int, tag string) {
		ts := time.Now().AddDate(0, 0, -ageDays).Unix()
		if _, err := db.conn.Exec(
			"INSERT INTO inventory_history (agent_id, collected_at, hardware_json, software_json) VALUES (?, ?, ?, ?)",
			agent, ts, tag, tag,
		); err != nil {
			t.Fatalf("insert %s/%s: %v", agent, tag, err)
		}
	}

	// agent-a: 1 recente (1 dia) + 3 antigas; agent-b: 2 antigas.
	insert("agent-a", 1, "a-novo")
	insert("agent-a", 30, "a-antigo-1")
	insert("agent-a", 40, "a-antigo-2")
	insert("agent-a", 50, "a-antigo-3")
	insert("agent-b", 30, "b-antigo-1")
	insert("agent-b", 60, "b-antigo-2")

	removed, err := db.CleanOldInventoryKeepingLatest(7, 1)
	if err != nil {
		t.Fatalf("CleanOldInventoryKeepingLatest: %v", err)
	}
	// Mantém a mais recente de cada agente: 4 linhas antigas removidas.
	if removed != 4 {
		t.Fatalf("removed = %d, want 4", removed)
	}

	var remaining int
	if err := db.conn.QueryRow("SELECT COUNT(*) FROM inventory_history").Scan(&remaining); err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 2 {
		t.Fatalf("remaining = %d, want 2", remaining)
	}

	// O snapshot mais recente do agent-a precisa sobreviver (base do delta sync).
	var hw string
	if err := db.conn.QueryRow(
		"SELECT hardware_json FROM inventory_history WHERE agent_id = 'agent-a' ORDER BY collected_at DESC LIMIT 1",
	).Scan(&hw); err != nil {
		t.Fatalf("select latest: %v", err)
	}
	if hw != "a-novo" {
		t.Fatalf("latest do agent-a = %q, want a-novo", hw)
	}
}

func TestCleanOldInventoryKeepingLatestRejectsInvalidDays(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if _, err := db.CleanOldInventoryKeepingLatest(0, 1); err == nil {
		t.Fatal("esperava erro para days=0")
	}
}

func TestCompactIfBloatedVacuumsAfterLargeDelete(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	blob := strings.Repeat("x", 20*1024)
	for i := 0; i < 200; i++ {
		if _, err := db.conn.Exec(
			"INSERT INTO inventory_history (agent_id, collected_at, hardware_json, software_json) VALUES (?, ?, ?, ?)",
			"agent-a", int64(i), blob, blob,
		); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	before := pageCount(t, db)

	if _, err := db.conn.Exec("DELETE FROM inventory_history WHERE collected_at < 190"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	compacted, err := db.CompactIfBloated(0.25)
	if err != nil {
		t.Fatalf("CompactIfBloated: %v", err)
	}
	if !compacted {
		t.Fatal("esperava VACUUM após apagar ~95% das páginas")
	}
	if after := pageCount(t, db); after >= before {
		t.Fatalf("page_count após VACUUM = %d, want < %d", after, before)
	}

	// Segunda chamada sem folga: não deve rodar de novo.
	again, err := db.CompactIfBloated(0.25)
	if err != nil {
		t.Fatalf("CompactIfBloated segunda: %v", err)
	}
	if again {
		t.Fatal("VACUUM não deveria rodar sem folga de páginas livres")
	}
}

func TestShrinkMemoryIsNoOpSafe(t *testing.T) {
	if err := (*DB)(nil).ShrinkMemory(); err != nil {
		t.Fatalf("ShrinkMemory nil = %v", err)
	}
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if err := db.ShrinkMemory(); err != nil {
		t.Fatalf("ShrinkMemory: %v", err)
	}
}

func pageCount(t *testing.T, db *DB) int {
	t.Helper()
	var n int
	if err := db.conn.QueryRow("PRAGMA page_count").Scan(&n); err != nil {
		t.Fatalf("page_count: %v", err)
	}
	return n
}
