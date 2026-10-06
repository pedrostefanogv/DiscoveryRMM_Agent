// Package logstore mantém os logs do agente e do chat em bancos SQLite
// dedicados dentro de %ProgramData%\Discovery\logs.
//
// Decisões de projeto (RELATORIO_VIABILIDADE_LOG_SQLITE.md §5):
//   - journal_mode=DELETE: em repouso existe APENAS o arquivo .db (sem
//     -wal/-shm), de modo que copiar o arquivo para debug é suficiente.
//   - auto_vacuum=INCREMENTAL definido na criação: a purga devolve páginas
//     ao SO, senão o arquivo ficaria no pico histórico para sempre.
//   - um writer por processo com commits em lote: logging nunca bloqueia o
//     chamador (canal com buffer + descarte com contador quando enche).
//   - retenção por idade (7 dias) E por teto de tamanho (128 MB), aplicada
//     no startup e por ticker diário.
//
// O pacote nunca é fatal: falha de abertura é devolvida ao chamador (que cai
// para arquivo texto) e falhas de escrita em runtime são contabilizadas em
// LastError/Dropped sem derrubar o processo.
package logstore

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

// Kind identifica o schema do banco.
type Kind string

const (
	// KindLogs é o banco logs.db (log_entries): logs do agent e do serviço.
	KindLogs Kind = "logs"
	// KindChat é o banco chat.db (chat_entries): interações de chat.
	KindChat Kind = "chat"
)

// Defaults de retenção, teto de tamanho e batching.
const (
	DefaultRetention   = 7 * 24 * time.Hour
	DefaultMaxBytes    = int64(128 << 20) // 128 MB
	DefaultFlushEvery  = 250 * time.Millisecond
	DefaultBatchSize   = 200
	DefaultQueueSize   = 4096
	DefaultMaintenance = 24 * time.Hour
	maxMessageBytes    = 64 << 10
	schemaVersion      = 1
	metaLastPurge      = "last_purge_ms"
)

// Options configura a abertura de um Store.
type Options struct {
	// Path é o caminho do arquivo .db.
	Path string
	// Kind seleciona o schema (KindLogs por padrão).
	Kind Kind
	// Source e Process são gravados em cada linha de log (diagnóstico).
	Source  string
	Process string
	// Retention é a idade máxima das linhas (default 7 dias).
	Retention time.Duration
	// MaxBytes é o teto do arquivo (default 128 MB).
	MaxBytes int64
	// FlushEvery é o intervalo máximo entre commits (default 250 ms).
	FlushEvery time.Duration
	// BatchSize força o commit ao acumular N registros (default 200).
	BatchSize int
	// QueueSize é a capacidade do canal de escrita (default 4096).
	QueueSize int
	// MaintenanceInterval é o período da purga periódica. Valores <= 0 usam o
	// default de 24 h (a purga no startup também é responsabilidade do
	// chamador, além desta periódica).
	MaintenanceInterval time.Duration
	// LegacyDir, quando informado, faz a manutenção remover arquivos texto
	// antigos (installer.log, fallback e _legacy/) além das linhas do banco.
	LegacyDir string
	// OnError recebe avisos não fatais (nunca deve bloquear).
	OnError func(string)
	// Now permite injetar o relógio em testes (default time.Now).
	Now func() time.Time
}

type record struct {
	chat    bool
	tsMs    int64
	tsISO   string
	level   string
	rev     string
	message string
	session string
	entry   ChatEntry
}

// ChatEntry espelha os campos persistidos das interações de chat.
// O pacote ai é responsável por redigir segredos ANTES de montar a entrada.
type ChatEntry struct {
	Timestamp    time.Time
	CodeRev      string
	Type         string
	Endpoint     string
	Method       string
	SessionID    string
	StatusCode   int
	LatencyMs    int
	TokensUsed   int
	MessageLen   int
	ResponseLen  int
	Round        int
	ToolCount    int
	StreamDone   bool
	HasTokens    bool
	HasToolCalls bool
	Error        string
	UserMsg      string
	Assistant    string
	ToolCalls    []string
	ToolArgs     []string
	ToolResults  []string
}

// Store é um banco de logs com writer assíncrono.
type Store struct {
	opts  Options
	db    *sql.DB
	table string

	ch        chan record
	done      chan struct{}
	maintStop chan struct{}
	wg        sync.WaitGroup
	maintWg   sync.WaitGroup

	sendMu   sync.RWMutex
	closed   bool
	once     sync.Once
	flushReq chan chan struct{}

	dropped  uint64
	written  uint64
	enqueued atomic.Int64
	lastErr  atomic.Value // string
}

// Open abre (criando se necessário) o banco e inicia o writer.
func Open(opts Options) (*Store, error) {
	if strings.TrimSpace(opts.Path) == "" {
		return nil, fmt.Errorf("logstore: caminho vazio")
	}
	opts = withDefaults(opts)

	if err := os.MkdirAll(filepath.Dir(opts.Path), 0o755); err != nil {
		return nil, fmt.Errorf("logstore: criar diretório: %w", err)
	}

	dsn := "file:" + opts.Path + "?_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("logstore: abrir %s: %w", opts.Path, err)
	}
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)

	if err := applyPragmas(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	table, err := createSchema(db, opts.Kind)
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	s := &Store{
		opts:      opts,
		db:        db,
		table:     table,
		ch:        make(chan record, opts.QueueSize),
		done:      make(chan struct{}),
		maintStop: make(chan struct{}),
		flushReq:  make(chan chan struct{}),
	}
	s.wg.Add(1)
	go s.runWriter()
	if opts.MaintenanceInterval > 0 {
		s.maintWg.Add(1)
		go s.runMaintenance()
	}
	return s, nil
}

func withDefaults(opts Options) Options {
	if opts.Kind == "" {
		opts.Kind = KindLogs
	}
	if opts.Retention <= 0 {
		opts.Retention = DefaultRetention
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxBytes
	}
	if opts.FlushEvery <= 0 {
		opts.FlushEvery = DefaultFlushEvery
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = DefaultBatchSize
	}
	if opts.QueueSize <= 0 {
		opts.QueueSize = DefaultQueueSize
	}
	if opts.MaintenanceInterval == 0 {
		opts.MaintenanceInterval = DefaultMaintenance
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return opts
}

func applyPragmas(db *sql.DB) error {
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		return fmt.Errorf("logstore: busy_timeout: %w", err)
	}
	// journal_mode=DELETE mantém o banco autocontido em repouso (sem -wal/-shm).
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode=DELETE").Scan(&mode); err != nil {
		return fmt.Errorf("logstore: journal_mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA synchronous=NORMAL"); err != nil {
		return fmt.Errorf("logstore: synchronous: %w", err)
	}
	// auto_vacuum precisa ser definido ANTES da criação das tabelas para valer.
	if _, err := db.Exec("PRAGMA auto_vacuum=INCREMENTAL"); err != nil {
		return fmt.Errorf("logstore: auto_vacuum: %w", err)
	}
	var av int
	if err := db.QueryRow("PRAGMA auto_vacuum").Scan(&av); err != nil {
		return fmt.Errorf("logstore: ler auto_vacuum: %w", err)
	}
	// PRAGMA auto_vacuum: 0=NONE, 1=FULL, 2=INCREMENTAL.
	if av != 2 {
		// Banco de versão anterior (ou pragma não fixado): aplica a
		// configuração uma vez; o VACUUM reescreve o arquivo no novo modo.
		_, _ = db.Exec("VACUUM")
	}
	return nil
}

func createSchema(db *sql.DB, kind Kind) (string, error) {
	ddl := schemaLogs
	table := "log_entries"
	if kind == KindChat {
		ddl = schemaChat
		table = "chat_entries"
	}
	if _, err := db.Exec(ddl); err != nil {
		return "", fmt.Errorf("logstore: criar schema: %w", err)
	}
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT)"); err != nil {
		return "", fmt.Errorf("logstore: criar meta: %w", err)
	}
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err == nil && v == 0 {
		_, _ = db.Exec("PRAGMA user_version = " + strconv.Itoa(schemaVersion))
	}
	// Migração aditiva: bancos criados por uma versão anterior podem não ter
	// colunas novas (CREATE TABLE IF NOT EXISTS não as adiciona). Sem isto o
	// INSERT falharia em todos os registros após um upgrade.
	if kind == KindChat {
		if err := ensureColumn(db, table, "tool_count", "INTEGER"); err != nil {
			return "", fmt.Errorf("logstore: migrar %s: %w", table, err)
		}
	}
	return table, nil
}

// ensureColumn adiciona uma coluna ausente (ALTER TABLE ADD COLUMN). Sempre
// fecha as rows antes do ALTER: o pool tem uma única conexão (SetMaxOpenConns(1)).
func ensureColumn(db *sql.DB, table, column, typ string) error {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, ctype string
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			_ = rows.Close()
			return err
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	if found {
		return nil
	}
	_, err = db.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + typ)
	return err
}

const schemaLogs = `
CREATE TABLE IF NOT EXISTS log_entries (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	ts_ms      INTEGER NOT NULL,
	ts_iso     TEXT    NOT NULL,
	source     TEXT    NOT NULL,
	level      TEXT,
	process    TEXT,
	session_id TEXT,
	code_rev   TEXT,
	message    TEXT    NOT NULL,
	details    TEXT
);
CREATE INDEX IF NOT EXISTS idx_log_entries_ts ON log_entries(ts_ms);
CREATE INDEX IF NOT EXISTS idx_log_entries_src_ts ON log_entries(source, ts_ms);
`

const schemaChat = `
CREATE TABLE IF NOT EXISTS chat_entries (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	ts_ms          INTEGER NOT NULL,
	ts_iso         TEXT    NOT NULL,
	code_rev       TEXT,
	type           TEXT,
	endpoint       TEXT,
	method         TEXT,
	session_id     TEXT,
	status_code    INTEGER,
	latency_ms     INTEGER,
	tokens_used    INTEGER,
	message_len    INTEGER,
	response_len   INTEGER,
	round          INTEGER,
	tool_count     INTEGER,
	stream_done    INTEGER,
	has_tokens     INTEGER,
	has_tool_calls INTEGER,
	error          TEXT,
	user_msg       TEXT,
	assistant      TEXT,
	tool_calls     TEXT,
	tool_args      TEXT,
	tool_results   TEXT
);
CREATE INDEX IF NOT EXISTS idx_chat_entries_ts ON chat_entries(ts_ms);
CREATE INDEX IF NOT EXISTS idx_chat_entries_session ON chat_entries(session_id, ts_ms);
`

// Path devolve o caminho do arquivo do banco.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.opts.Path
}

// Kind devolve o tipo do banco.
func (s *Store) Kind() Kind {
	if s == nil {
		return ""
	}
	return s.opts.Kind
}

// Dropped devolve quantas linhas foram descartadas por fila cheia.
func (s *Store) Dropped() uint64 {
	if s == nil {
		return 0
	}
	return atomic.LoadUint64(&s.dropped)
}

// Written devolve quantas linhas foram confirmadas em commit.
func (s *Store) Written() uint64 {
	if s == nil {
		return 0
	}
	return atomic.LoadUint64(&s.written)
}

// LastError devolve a última falha não fatal (vazio se não houve).
func (s *Store) LastError() string {
	if s == nil {
		return ""
	}
	if v, ok := s.lastErr.Load().(string); ok {
		return v
	}
	return ""
}

// Count devolve quantas linhas existem na tabela do banco (diagnóstico).
func (s *Store) Count() (int64, error) {
	if s == nil {
		return 0, nil
	}
	var n int64
	if err := s.db.QueryRow("SELECT COUNT(*) FROM " + s.table).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// SizeBytes devolve o tamanho atual do arquivo principal.
func (s *Store) SizeBytes() int64 {
	if s == nil {
		return 0
	}
	return s.fileSize()
}

// AppendLog enfileira uma linha de log. Nunca bloqueia e nunca é fatal.
func (s *Store) AppendLog(level, codeRev, message string) {
	if s == nil {
		return
	}
	message = clampMessage(message)
	if message == "" && level == "" {
		return
	}
	now := s.now()
	s.enqueue(record{
		tsMs:    now.UnixMilli(),
		tsISO:   now.UTC().Format(time.RFC3339Nano),
		level:   level,
		rev:     codeRev,
		message: message,
	})
}

// AppendChat enfileira uma interação de chat já redigida pelo chamador.
func (s *Store) AppendChat(e ChatEntry) {
	if s == nil {
		return
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = s.now()
	}
	e.UserMsg = clampMessage(e.UserMsg)
	e.Assistant = clampMessage(e.Assistant)
	e.Error = clampMessage(e.Error)
	s.enqueue(record{
		chat:  true,
		tsMs:  e.Timestamp.UnixMilli(),
		tsISO: e.Timestamp.UTC().Format(time.RFC3339Nano),
		rev:   e.CodeRev,
		entry: e,
	})
}

func (s *Store) now() time.Time {
	if s.opts.Now != nil {
		return s.opts.Now()
	}
	return time.Now()
}

func clampMessage(msg string) string {
	if len(msg) <= maxMessageBytes {
		return msg
	}
	return msg[:maxMessageBytes] + "... (truncado)"
}

func (s *Store) enqueue(r record) {
	s.sendMu.RLock()
	defer s.sendMu.RUnlock()
	if s.closed {
		return
	}
	select {
	case s.ch <- r:
		s.enqueued.Add(1)
	default:
		atomic.AddUint64(&s.dropped, 1)
	}
}

func (s *Store) runWriter() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.opts.FlushEvery)
	defer ticker.Stop()
	// Capacidade inicial limitada: BatchSize pode ser configurado alto e
	// make([]record, 0, BatchSize) alocaria centenas de MB sem necessidade.
	capHint := s.opts.BatchSize
	if capHint > 1024 {
		capHint = 1024
	}
	batch := make([]record, 0, capHint)
	flush := func() {
		if len(batch) > 0 {
			s.flush(batch)
			batch = batch[:0]
		}
	}
	// drain move para o lote tudo que já está no canal, sem esperar o ticker:
	// sem isto um Flush poderia confirmar apenas o lote corrente.
	drain := func() {
		for {
			select {
			case r := <-s.ch:
				batch = append(batch, r)
			default:
				return
			}
		}
	}
	for {
		select {
		case r := <-s.ch:
			batch = append(batch, r)
			if len(batch) >= s.opts.BatchSize {
				flush()
			}
		case ack := <-s.flushReq:
			drain()
			flush()
			close(ack)
		case <-ticker.C:
			flush()
		case <-s.done:
			for {
				select {
				case r := <-s.ch:
					batch = append(batch, r)
				case ack := <-s.flushReq:
					drain()
					flush()
					close(ack)
				default:
					flush()
					return
				}
			}
		}
	}
}

func (s *Store) flush(batch []record) {
	tx, err := s.db.Begin()
	if err != nil {
		s.errorf("begin: %v", err)
		return
	}
	insert := "INSERT INTO " + s.table + ` (
		ts_ms, ts_iso, source, level, process, session_id, code_rev, message, details
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
	if s.opts.Kind == KindChat {
		insert = "INSERT INTO chat_entries (" +
			"ts_ms, ts_iso, code_rev, type, endpoint, method, session_id, status_code, latency_ms, tokens_used," +
			" message_len, response_len, round, tool_count, stream_done, has_tokens, has_tool_calls, error, user_msg," +
			" assistant, tool_calls, tool_args, tool_results" +
			") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"
	}
	stmt, err := tx.Prepare(insert)
	if err != nil {
		_ = tx.Rollback()
		s.errorf("prepare: %v", err)
		return
	}
	defer stmt.Close()

	failed := 0
	for _, r := range batch {
		var err error
		if s.opts.Kind == KindChat {
			_, err = stmt.Exec(
				r.tsMs, r.tsISO, r.rev, r.entry.Type, r.entry.Endpoint, r.entry.Method, r.entry.SessionID,
				r.entry.StatusCode, r.entry.LatencyMs, r.entry.TokensUsed, r.entry.MessageLen, r.entry.ResponseLen,
				r.entry.Round, r.entry.ToolCount, boolInt(r.entry.StreamDone), boolInt(r.entry.HasTokens), boolInt(r.entry.HasToolCalls),
				r.entry.Error, r.entry.UserMsg, r.entry.Assistant,
				jsonList(r.entry.ToolCalls), jsonList(r.entry.ToolArgs), jsonList(r.entry.ToolResults),
			)
		} else {
			_, err = stmt.Exec(
				r.tsMs, r.tsISO, s.opts.Source, r.level, s.opts.Process, r.session, r.rev, r.message, "",
			)
		}
		if err != nil {
			failed++
			s.errorf("insert: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		s.errorf("commit: %v", err)
		return
	}
	if written := len(batch) - failed; written > 0 {
		atomic.AddUint64(&s.written, uint64(written))
	}
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func jsonList(items []string) string {
	if len(items) == 0 {
		return ""
	}
	data, err := json.Marshal(items)
	if err != nil {
		return ""
	}
	return string(data)
}

// Flush garante que tudo que foi enfileirado ATÉ a chamada está em commit.
// Usa o contador de progresso porque um único ack pode confirmar apenas o lote
// corrente, deixando registros ainda no canal. Nunca é fatal e tem prazo
// limitado (evita travar a purga/shutdown por causa de um erro de escrita).
func (s *Store) Flush() {
	if s == nil {
		return
	}
	target := s.enqueued.Load()
	deadline := time.Now().Add(3 * time.Second)
	for atomic.LoadUint64(&s.written) < uint64(target) {
		if time.Now().After(deadline) {
			return
		}
		ack := make(chan struct{})
		select {
		case s.flushReq <- ack:
			select {
			case <-ack:
			case <-s.done:
				return
			}
		case <-s.done:
			return
		}
	}
}

// Purge remove linhas antigas e, se necessário, as mais antigas até respeitar
// o teto de tamanho. Depois devolve páginas ao SO (auto_vacuum incremental).
func (s *Store) Purge() error {
	if s == nil {
		return nil
	}
	// Barreira: garante que o lote pendente foi gravado antes de apagar/medir.
	s.Flush()
	cutoff := s.now().Add(-s.opts.Retention).UnixMilli()
	if _, err := s.db.Exec("DELETE FROM "+s.table+" WHERE ts_ms < ?", cutoff); err != nil {
		return s.wrap("purge por idade", err)
	}
	for i := 0; i < 200; i++ {
		if s.fileSize() <= s.opts.MaxBytes {
			break
		}
		// Libera páginas JÁ livres antes de apagar mais linhas: o arquivo só
		// encolhe depois do vacuum, então sem isto o teto seria ultrapassado e
		// o loop apagaria tudo antes de devolver espaço.
		_, _ = s.db.Exec("PRAGMA incremental_vacuum")
		if s.fileSize() <= s.opts.MaxBytes {
			break
		}
		res, err := s.db.Exec(
			"DELETE FROM " + s.table + " WHERE id IN (SELECT id FROM " + s.table + " ORDER BY ts_ms ASC LIMIT 500)")
		if err != nil {
			return s.wrap("purge por tamanho", err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			break
		}
	}
	_, _ = s.db.Exec("PRAGMA incremental_vacuum")
	if s.opts.LegacyDir != "" {
		PurgeStaleFiles(s.opts.LegacyDir, s.opts.Retention)
	}
	s.setMeta(metaLastPurge, strconv.FormatInt(s.now().UnixMilli(), 10))
	return nil
}

// PurgeIfDue executa a purga apenas se a última foi há mais de 1 hora
// (evita que múltiplos processos purguem ao mesmo tempo).
func (s *Store) PurgeIfDue() error {
	if s == nil {
		return nil
	}
	last := s.getMetaInt(metaLastPurge)
	if last > 0 && s.now().UnixMilli()-last < int64(time.Hour/time.Millisecond) {
		return nil
	}
	return s.Purge()
}

func (s *Store) runMaintenance() {
	defer s.maintWg.Done()
	ticker := time.NewTicker(s.opts.MaintenanceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := s.PurgeIfDue(); err != nil {
				s.errorf("manutenção: %v", err)
			}
		case <-s.maintStop:
			return
		}
	}
}

func (s *Store) setMeta(key, value string) {
	_, _ = s.db.Exec("INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value)
}

func (s *Store) getMetaInt(key string) int64 {
	var raw string
	if err := s.db.QueryRow("SELECT value FROM meta WHERE key = ?", key).Scan(&raw); err != nil {
		return 0
	}
	v, _ := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return v
}

func (s *Store) fileSize() int64 {
	info, err := os.Stat(s.opts.Path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func (s *Store) errorf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	s.lastErr.Store(msg)
	if s.opts.OnError != nil {
		func() {
			defer func() { _ = recover() }()
			s.opts.OnError(msg)
		}()
	}
}

func (s *Store) wrap(what string, err error) error {
	wrapped := fmt.Errorf("logstore: %s: %w", what, err)
	s.errorf("%s: %v", what, err)
	return wrapped
}

// Close limpa a fila, fecha o banco e para a manutenção. Idempotente.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		s.sendMu.Lock()
		s.closed = true
		s.sendMu.Unlock()
		close(s.done)
		s.wg.Wait()
		close(s.maintStop)
		s.maintWg.Wait()
		if s.db != nil {
			_ = s.db.Close()
		}
	})
	return nil
}

// ParseLevel separa o prefixo "[NÍVEL] " de uma linha formatada pelo logger.
// Linhas sem prefixo retornam nível vazio e o texto original.
func ParseLevel(line string) (level, message string) {
	for _, lv := range []string{"TRACE", "DEBUG", "INFO", "WARN", "ERROR"} {
		prefix := "[" + lv + "] "
		if strings.HasPrefix(line, prefix) {
			return lv, strings.TrimPrefix(line, prefix)
		}
	}
	return "", line
}
