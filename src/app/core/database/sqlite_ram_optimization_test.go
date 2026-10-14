package database

import (
	"bytes"
	"fmt"
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

func TestPruneInventoryHistoryKeepsNewestPerAgentByCount(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	insert := func(agent string, minute int) {
		// Todas RECENTES de propósito: a poda por idade não removeria nenhuma
		// (foi exatamente o caso real — 52 linhas em 20 h, todas < 7 dias).
		ts := time.Now().Add(-time.Duration(minute) * time.Minute).Unix()
		tag := fmt.Sprintf("%s-%02d", agent, minute)
		if _, err := db.conn.Exec(
			"INSERT INTO inventory_history (agent_id, collected_at, hardware_json, software_json) VALUES (?, ?, ?, ?)",
			agent, ts, tag, tag,
		); err != nil {
			t.Fatalf("insert %s: %v", tag, err)
		}
	}

	// agent-a: 12 linhas (minuto 0 = mais nova); agent-b: 7 linhas.
	for i := 0; i < 12; i++ {
		insert("agent-a", i)
	}
	for i := 0; i < 7; i++ {
		insert("agent-b", i)
	}

	removed, err := db.PruneInventoryHistory(5)
	if err != nil {
		t.Fatalf("PruneInventoryHistory: %v", err)
	}
	if removed != 9 {
		t.Fatalf("removed = %d, want 9 (7 do agent-a + 2 do agent-b)", removed)
	}

	rows, err := db.conn.Query("SELECT agent_id, COUNT(*) FROM inventory_history GROUP BY agent_id ORDER BY agent_id")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	defer rows.Close()
	got := map[string]int{}
	for rows.Next() {
		var agent string
		var n int
		if err := rows.Scan(&agent, &n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[agent] = n
	}
	if got["agent-a"] != 5 || got["agent-b"] != 5 {
		t.Fatalf("linhas por agente = %v, want agent-a=5 agent-b=5", got)
	}

	// A linha mais recente de cada agente precisa sobreviver (base do delta sync).
	for _, agent := range []string{"agent-a", "agent-b"} {
		var hw string
		if err := db.conn.QueryRow(
			"SELECT hardware_json FROM inventory_history WHERE agent_id = ? ORDER BY collected_at DESC, id DESC LIMIT 1", agent,
		).Scan(&hw); err != nil {
			t.Fatalf("latest %s: %v", agent, err)
		}
		if hw != agent+"-00" {
			t.Fatalf("latest de %s = %q, want %s-00", agent, hw, agent)
		}
	}
}

func TestPruneInventoryHistoryClampsKeep(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	for i := 0; i < 3; i++ {
		if _, err := db.conn.Exec(
			"INSERT INTO inventory_history (agent_id, collected_at, hardware_json, software_json) VALUES (?, ?, ?, ?)",
			"a", time.Now().Unix()+int64(i), "hw", "sw",
		); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	if _, err := db.PruneInventoryHistory(0); err != nil {
		t.Fatalf("PruneInventoryHistory(0): %v", err)
	}
	var n int
	if err := db.conn.QueryRow("SELECT COUNT(*) FROM inventory_history").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("keep=0 deveria virar 1, restaram %d", n)
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
