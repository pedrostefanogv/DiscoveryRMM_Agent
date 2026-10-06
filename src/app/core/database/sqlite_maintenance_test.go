package database

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func pragmaInt(t *testing.T, db *DB, query string) int {
	t.Helper()
	var v int
	if err := db.conn.QueryRow(query).Scan(&v); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return v
}

// journal_size_limit no DSN limita o WAL, e o checkpoint TRUNCATE devolve o
// espaco em repouso (sem leitores concorrentes).
func TestJournalSizeLimitAndCheckpointTruncatesWAL(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	if got := pragmaInt(t, db, "PRAGMA journal_size_limit"); got != 4194304 {
		t.Fatalf("journal_size_limit = %d, esperava 4194304", got)
	}

	walPath := filepath.Join(dir, "discovery.db-wal")
	payload := strings.Repeat("x", 2048)
	for i := 0; i < 500; i++ {
		if _, err := db.conn.Exec("INSERT INTO cache(key, value, expires_at) VALUES(?, ?, NULL)",
			fmt.Sprintf("k%d", i), payload); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	if before := fileSize(walPath); before == 0 {
		t.Fatalf("premissa do teste: WAL deveria ter dados antes do checkpoint")
	}

	res, err := db.Checkpoint()
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if res.Busy {
		t.Fatalf("checkpoint inesperadamente busy: %+v", res)
	}
	if got := fileSize(walPath); got != 0 {
		t.Fatalf("WAL = %d bytes após TRUNCATE, esperava 0", got)
	}
}

func TestOptimizeIsSafe(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Optimize(); err != nil {
		t.Fatalf("Optimize: %v", err)
	}
}

// Close precisa deixar WAL truncado e ser idempotente (chamado mais de uma vez
// no shutdown do agente).
func TestCloseTruncatesWALAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := db.conn.Exec("INSERT INTO cache(key, value, expires_at) VALUES('k', 'v', NULL)"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close idempotente: %v", err)
	}
	if got := fileSize(filepath.Join(dir, "discovery.db-wal")); got != 0 {
		t.Fatalf("WAL residual = %d bytes, esperava 0", got)
	}
}

// StartMaintenance é idempotente e Close encerra o ticker sem vazar goroutine.
func TestMaintenanceStartIsIdempotentAndStopsOnClose(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	db.StartMaintenance(5 * time.Millisecond)
	db.StartMaintenance(time.Hour) // deve ser ignorado
	time.Sleep(30 * time.Millisecond)
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// O ticker deve ter sido encerrado e desregistrado (sem goroutine vazando).
	db.maintenanceMu.Lock()
	stopped := db.maintenanceStop == nil
	db.maintenanceMu.Unlock()
	if !stopped {
		t.Fatal("manutenção não foi encerrada no Close")
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close idempotente: %v", err)
	}
}
