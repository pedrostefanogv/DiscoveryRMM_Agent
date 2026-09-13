package database

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// TestM8_LegacyDatabaseMigratedAndRecreated simula um banco legado (v1, sem
// user_version) com tabela antiga: ao abrir, o schema legado é descartado
// (dados são cache descartável — decisão do dono), o schema atual é criado e
// user_version é gravado.
func TestM8_LegacyDatabaseMigratedAndRecreated(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "discovery.db")

	// Simula banco legado: tabela antiga + user_version 1.
	legacy, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open legado: %v", err)
	}
	if _, err := legacy.Exec("CREATE TABLE IF NOT EXISTS legacy_stuff (id INTEGER PRIMARY KEY, junk TEXT)"); err != nil {
		t.Fatalf("criar tabela legado: %v", err)
	}
	if _, err := legacy.Exec("INSERT INTO legacy_stuff (junk) VALUES ('velho')"); err != nil {
		t.Fatalf("inserir legado: %v", err)
	}
	if _, err := legacy.Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatalf("setar user_version legado: %v", err)
	}
	legacy.Close()

	// Abre com o Open atual: deve migrar (drop legacy + schema v2).
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	var version int
	if err := db.conn.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if version != currentSchemaVersion {
		t.Fatalf("user_version = %d, want %d", version, currentSchemaVersion)
	}

	var legacyCount int
	if err := db.conn.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = 'legacy_stuff'").Scan(&legacyCount); err != nil {
		t.Fatalf("inspecionar legacy_stuff: %v", err)
	}
	if legacyCount != 0 {
		t.Fatalf("tabela legado sobreviveu à migração (M8: drop-and-recreate)")
	}

	// Schema atual funcional: cache opera.
	if err := db.CacheSetJSON("m8-key", map[string]string{"v": "1"}, time.Hour); err != nil {
		t.Fatalf("CacheSetJSON: %v", err)
	}
	var out map[string]string
	found, err := db.CacheGetJSON("m8-key", &out)
	if err != nil || !found || out["v"] != "1" {
		t.Fatalf("cache pós-migração: found=%v out=%v err=%v", found, out, err)
	}
}

// TestM8_CurrentVersionPreservesData valida que reabrir um banco já na versão
// atual NÃO recria o schema (dados de cache sobrevivem entre restarts).
func TestM8_CurrentVersionPreservesData(t *testing.T) {
	dir := t.TempDir()

	db1, err := Open(dir)
	if err != nil {
		t.Fatalf("Open 1: %v", err)
	}
	if err := db1.CacheSetJSON("keep-key", map[string]string{"v": "ok"}, time.Hour); err != nil {
		t.Fatalf("CacheSetJSON: %v", err)
	}
	if err := db1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db2, err := Open(dir)
	if err != nil {
		t.Fatalf("Open 2: %v", err)
	}
	defer db2.Close()

	var version int
	if err := db2.conn.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if version != currentSchemaVersion {
		t.Fatalf("user_version = %d, want %d", version, currentSchemaVersion)
	}

	var out map[string]string
	found, err := db2.CacheGetJSON("keep-key", &out)
	if err != nil || !found || out["v"] != "ok" {
		t.Fatalf("cache deve sobreviver ao restart na mesma versão: found=%v out=%v err=%v", found, out, err)
	}
}
