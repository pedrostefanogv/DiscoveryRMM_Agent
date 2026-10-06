package logstore

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func openTest(t *testing.T, opts Options) *Store {
	t.Helper()
	s, err := Open(opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func queryInt(t *testing.T, path, query string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	var v int
	if err := db.QueryRow(query).Scan(&v); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return v
}

func queryStr(t *testing.T, path, query string) string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	var v string
	if err := db.QueryRow(query).Scan(&v); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return v
}

// O banco precisa ficar autocontido em repouso: um unico arquivo, sem -wal/-shm
// e com journal DELETE + auto_vacuum incremental.
func TestOpenLeavesSingleFileInRepose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logs.db")
	s := openTest(t, Options{Path: path, Kind: KindLogs, Source: "agent", Process: "discovery-agent"})
	s.AppendLog("INFO", "rev1", "linha de teste")
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "logs.db" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("esperava apenas logs.db em repouso, veio: %v", names)
	}
	if mode := strings.ToLower(queryStr(t, path, "PRAGMA journal_mode")); mode != "delete" {
		t.Fatalf("journal_mode = %q, esperava delete", mode)
	}
	// PRAGMA auto_vacuum: 0=NONE, 1=FULL, 2=INCREMENTAL.
	if av := queryInt(t, path, "PRAGMA auto_vacuum"); av != 2 {
		t.Fatalf("auto_vacuum = %d, esperava 2 (incremental)", av)
	}
	if v := queryInt(t, path, "PRAGMA user_version"); v != schemaVersion {
		t.Fatalf("user_version = %d, esperava %d", v, schemaVersion)
	}
}

func TestAppendReadBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs.db")
	s := openTest(t, Options{Path: path, Kind: KindLogs, Source: "service", Process: "discovery-service"})
	s.AppendLog("INFO", "abc12345", "primeira")
	s.AppendLog("ERROR", "abc12345", "segunda")
	s.AppendLog("", "abc12345", "sem nivel")
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if n := queryInt(t, path, "SELECT COUNT(*) FROM log_entries"); n != 3 {
		t.Fatalf("linhas = %d, esperava 3", n)
	}
	if got := queryStr(t, path, "SELECT source FROM log_entries LIMIT 1"); got != "service" {
		t.Fatalf("source = %q", got)
	}
	if got := queryStr(t, path, "SELECT process FROM log_entries LIMIT 1"); got != "discovery-service" {
		t.Fatalf("process = %q", got)
	}
	if got := queryStr(t, path, "SELECT level FROM log_entries WHERE message = 'segunda'"); got != "ERROR" {
		t.Fatalf("level = %q", got)
	}
	if got := queryStr(t, path, "SELECT code_rev FROM log_entries LIMIT 1"); got != "abc12345" {
		t.Fatalf("code_rev = %q", got)
	}
}

// Retencao por idade: 8 dias cai, agora fica. Usa relogio injetado.
func TestRetentionByAge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs.db")
	now := time.Now().Add(-8 * 24 * time.Hour)
	s := openTest(t, Options{Path: path, Kind: KindLogs, Now: func() time.Time { return now }})
	s.AppendLog("INFO", "rev", "velha")
	now = time.Now()
	s.AppendLog("INFO", "rev", "nova")
	if err := s.Purge(); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if n := queryInt(t, path, "SELECT COUNT(*) FROM log_entries"); n != 1 {
		t.Fatalf("linhas = %d, esperava 1 (só a nova)", n)
	}
	if got := queryStr(t, path, "SELECT message FROM log_entries"); got != "nova" {
		t.Fatalf("sobrou %q", got)
	}
}

// Teto de tamanho: arquivo acima do cap perde linhas antigas E devolve espaco.
func TestSizeCapTrimsAndShrinks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs.db")
	// Cap folgado em relação ao chunk da purga (500 linhas) para que o teste
	// detecte overshoot: 2 MB de teto com ~6 MB de dados de 1 KB por linha.
	const capBytes = int64(2 << 20)
	now := time.Now()

	writer := openTest(t, Options{Path: path, Kind: KindLogs, MaxBytes: capBytes, Now: func() time.Time { return now }})
	msg := strings.Repeat("x", 1024)
	for i := 0; i < 6000; i++ {
		writer.AppendLog("INFO", "rev", msg)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close writer: %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if before.Size() <= capBytes {
		t.Fatalf("premissa do teste inválida: arquivo já abaixo do teto (%d)", before.Size())
	}

	purger := openTest(t, Options{Path: path, Kind: KindLogs, MaxBytes: capBytes, Now: func() time.Time { return now }})
	if err := purger.Purge(); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	after := purger.SizeBytes()
	_ = purger.Close()

	if after >= before.Size() {
		t.Fatalf("teto não reduziu o arquivo: %d -> %d", before.Size(), after)
	}
	if after > capBytes*2 {
		t.Fatalf("arquivo ainda muito acima do teto: %d (cap %d)", after, capBytes)
	}
	if n := queryInt(t, path, "SELECT COUNT(*) FROM log_entries"); n == 0 {
		t.Fatalf("purga por tamanho apagou tudo (cap %d, arquivo %d)", capBytes, after)
	}
}

// Append nunca bloqueia e nada se perde quando a fila está ociosa.
func TestAppendNeverBlocksAndCountsEverything(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs.db")
	s := openTest(t, Options{
		Path:       path,
		Kind:       KindLogs,
		FlushEvery: time.Hour,
		BatchSize:  1 << 30,
		QueueSize:  4,
	})
	start := time.Now()
	const total = 20000
	for i := 0; i < total; i++ {
		s.AppendLog("INFO", "rev", "linha")
	}
	elapsed := time.Since(start)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("append bloqueou demais: %v", elapsed)
	}
	if got := s.Written() + s.Dropped(); got != total {
		t.Fatalf("persistidas+descartadas = %d, esperava %d", got, total)
	}
}

func TestCloseFlushesPending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs.db")
	s := openTest(t, Options{Path: path, Kind: KindLogs, FlushEvery: time.Hour, BatchSize: 1 << 30})
	for i := 0; i < 50; i++ {
		s.AppendLog("INFO", "rev", "pendente")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if n := queryInt(t, path, "SELECT COUNT(*) FROM log_entries"); n != 50 {
		t.Fatalf("linhas = %d, esperava 50 (flush no Close)", n)
	}
}

func TestOpenFailsForDirectoryPath(t *testing.T) {
	dir := t.TempDir()
	if s, err := Open(Options{Path: dir}); err == nil {
		_ = s.Close()
		t.Fatal("esperava erro ao abrir um diretório como arquivo de banco")
	}
}

func TestConcurrentAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs.db")
	s := openTest(t, Options{Path: path, Kind: KindLogs, QueueSize: 1024})
	var wg sync.WaitGroup
	const goroutines, perG = 8, 250
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				s.AppendLog("INFO", "rev", "concorrente")
			}
		}()
	}
	wg.Wait()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Contrato: nenhum append se perde — cada linha foi gravada OU descartada
	// (fila cheia). O banco tem exatamente o que foi confirmado em commit.
	written, dropped := s.Written(), s.Dropped()
	if written+dropped != goroutines*perG {
		t.Fatalf("gravadas+descartadas = %d, esperava %d", written+dropped, goroutines*perG)
	}
	if n := uint64(queryInt(t, path, "SELECT COUNT(*) FROM log_entries")); n != written {
		t.Fatalf("linhas no banco = %d, commits reportados = %d", n, written)
	}
}

func TestChatStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat.db")
	s := openTest(t, Options{Path: path, Kind: KindChat})
	s.AppendChat(ChatEntry{
		Type:       "stream_error",
		SessionID:  "sess-1",
		StatusCode: 500,
		LatencyMs:  42,
		TokensUsed: 7,
		ToolCount:  3,
		UserMsg:    "oi",
		Assistant:  "ola",
		ToolCalls:  []string{"ping"},
	})
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if n := queryInt(t, path, "SELECT COUNT(*) FROM chat_entries"); n != 1 {
		t.Fatalf("linhas = %d", n)
	}
	if got := queryStr(t, path, "SELECT type FROM chat_entries"); got != "stream_error" {
		t.Fatalf("type = %q", got)
	}
	if got := queryStr(t, path, "SELECT user_msg FROM chat_entries"); got != "oi" {
		t.Fatalf("user_msg = %q", got)
	}
	if got := queryInt(t, path, "SELECT status_code FROM chat_entries"); got != 500 {
		t.Fatalf("status_code = %d", got)
	}
	if got := queryInt(t, path, "SELECT tool_count FROM chat_entries"); got != 3 {
		t.Fatalf("tool_count = %d (campo do JSONL não pode ser perdido)", got)
	}
	if got := queryStr(t, path, "SELECT tool_calls FROM chat_entries"); got != `["ping"]` {
		t.Fatalf("tool_calls = %q", got)
	}
}

// Banco criado antes de uma coluna nova precisa recebê-la (upgrade sem perda
// de escrita). CREATE TABLE IF NOT EXISTS não adiciona colunas.
func TestChatSchemaAddsMissingColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat.db")
	// Cria o schema completo e remove a coluna nova: simula um banco gravado
	// pela versão anterior à migração aditiva.
	fresh := openTest(t, Options{Path: path, Kind: KindChat})
	if err := fresh.Close(); err != nil {
		t.Fatalf("Close fresh: %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec("ALTER TABLE chat_entries DROP COLUMN tool_count"); err != nil {
		_ = db.Close()
		t.Fatalf("DROP COLUMN tool_count: %v", err)
	}
	_ = db.Close()

	s := openTest(t, Options{Path: path, Kind: KindChat})
	s.AppendChat(ChatEntry{Type: "multi_round_start", ToolCount: 2})
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := queryInt(t, path, "SELECT tool_count FROM chat_entries"); got != 2 {
		t.Fatalf("tool_count = %d, esperava 2", got)
	}
}

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in, level, msg string
	}{
		{"[ERROR] falhou k=v", "ERROR", "falhou k=v"},
		{"[INFO] ok", "INFO", "ok"},
		{"sem prefixo", "", "sem prefixo"},
		{"[TRACE] detalhe", "TRACE", "detalhe"},
	}
	for _, c := range cases {
		lv, msg := ParseLevel(c.in)
		if lv != c.level || msg != c.msg {
			t.Fatalf("ParseLevel(%q) = (%q,%q), esperava (%q,%q)", c.in, lv, msg, c.level, c.msg)
		}
	}
}

// Dois Stores no MESMO arquivo simulam serviço (SYSTEM) + UI (usuário)
// escrevendo em logs.db ao mesmo tempo. Valida busy_timeout + journal DELETE.
func TestTwoStoresSameFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logs.db")
	a := openTest(t, Options{Path: path, Kind: KindLogs, Source: "agent", Process: "discovery-agent"})
	b := openTest(t, Options{Path: path, Kind: KindLogs, Source: "service", Process: "discovery-service"})

	var wg sync.WaitGroup
	writer := func(s *Store, tag string, n int) {
		defer wg.Done()
		for i := 0; i < n; i++ {
			s.AppendLog("INFO", "rev", tag)
		}
	}
	wg.Add(2)
	go writer(a, "ui", 400)
	go writer(b, "svc", 400)
	wg.Wait()

	if err := a.Close(); err != nil {
		t.Fatalf("Close a: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close b: %v", err)
	}
	want := a.Written() + b.Written()
	if want != 800 {
		t.Fatalf("commits = %d, esperava 800", want)
	}
	if got := uint64(queryInt(t, path, "SELECT COUNT(*) FROM log_entries")); got != want {
		t.Fatalf("linhas = %d, commits = %d", got, want)
	}
	if n := queryInt(t, path, "SELECT COUNT(DISTINCT source) FROM log_entries"); n != 2 {
		t.Fatalf("fontes distintas = %d, esperava 2 (agent e service)", n)
	}
	// Mesmo com dois escritores, em repouso resta apenas o .db.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "logs.db" {
			t.Fatalf("arquivo inesperado em repouso: %s", e.Name())
		}
	}
}

// A purga periódica (ticker) precisa rodar sem reiniciar o agente — é o que
// limita o crescimento em máquina ligada por semanas.
func TestMaintenancePurgesPeriodically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs.db")
	s := openTest(t, Options{
		Path:                path,
		Kind:                KindLogs,
		Retention:           10 * time.Millisecond,
		FlushEvery:          10 * time.Millisecond,
		MaintenanceInterval: 40 * time.Millisecond,
	})
	s.AppendLog("INFO", "rev", "efemera")

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if n, err := s.Count(); err == nil && n == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	n, _ := s.Count()
	t.Fatalf("purga periódica não removeu a linha (linhas=%d)", n)
}

func TestPurgeStaleFiles(t *testing.T) {
	dir := t.TempDir()
	past := time.Now().Add(-8 * 24 * time.Hour)

	installer := filepath.Join(dir, "installer.log")
	if err := os.WriteFile(installer, []byte("log"), 0o600); err != nil {
		t.Fatal(err)
	}
	dbFile := filepath.Join(dir, "logs.db")
	if err := os.WriteFile(dbFile, []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacyDir := filepath.Join(dir, LegacyDirName)
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacyFile := filepath.Join(legacyDir, "agent.log")
	if err := os.WriteFile(legacyFile, []byte("antigo"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{installer, dbFile, legacyFile} {
		if err := os.Chtimes(p, past, past); err != nil {
			t.Fatal(err)
		}
	}

	removed := PurgeStaleFiles(dir, 7*24*time.Hour)
	if removed != 2 {
		t.Fatalf("removidos = %d, esperava 2", removed)
	}
	if _, err := os.Stat(installer); !os.IsNotExist(err) {
		t.Fatalf("installer.log deveria ter sido removido")
	}
	if _, err := os.Stat(legacyFile); !os.IsNotExist(err) {
		t.Fatalf("_legacy/agent.log deveria ter sido removido")
	}
	if _, err := os.Stat(dbFile); err != nil {
		t.Fatalf("logs.db NUNCA deve ser removido pela retenção: %v", err)
	}
}

func TestArchiveLegacyLogs(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"agent.log", "chat_logs-20260101-000000.jsonl"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "logs.db"), []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}

	moved := ArchiveLegacyLogs(dir)
	if len(moved) != 2 {
		t.Fatalf("movidos = %v, esperava 2", moved)
	}
	for _, name := range moved {
		if _, err := os.Stat(filepath.Join(dir, LegacyDirName, name)); err != nil {
			t.Fatalf("arquivo %s não foi para _legacy: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "logs.db")); err != nil {
		t.Fatalf("logs.db não deveria ser arquivado: %v", err)
	}
}
