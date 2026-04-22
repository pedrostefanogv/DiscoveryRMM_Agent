package database

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// DB encapsula a conexão SQLite e operações de cache
type DB struct {
	conn *sql.DB
}

type AutomationExecutionEntry struct {
	ExecutionID      string
	AgentID          string
	CommandID        string
	TaskID           string
	TaskName         string
	ActionType       string
	InstallationType string
	SourceType       string
	TriggerType      string
	Status           string
	CorrelationID    string
	StartedAt        time.Time
	FinishedAt       time.Time
	Success          bool
	SuccessSet       bool
	ExitCode         int
	ExitCodeSet      bool
	ErrorMessage     string
	Output           string
	PackageID        string
	ScriptID         string
	MetadataJSON     string
}

type AutomationCallbackEntry struct {
	ID            int64
	AgentID       string
	ExecutionID   string
	CommandID     string
	CallbackType  string
	CorrelationID string
	PayloadJSON   string
	Attempts      int
	NextAttemptAt time.Time
	LastError     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type PSADTBootstrapEntry struct {
	ID               int64
	RequiredVersion  string
	Installed        bool
	InstalledVersion string
	Source           string
	Message          string
	CreatedAt        time.Time
}

type NotificationEventEntry struct {
	ID             int64
	NotificationID string
	Mode           string
	Severity       string
	EventType      string
	Title          string
	Result         string
	AgentAction    string
	MetadataJSON   string
	CreatedAt      time.Time
}

type AutomationDeferStateEntry struct {
	AgentID        string
	TaskID         string
	ExecutionID    string
	DeferCount     int
	FirstDeferAt   time.Time
	LastDeferAt    time.Time
	DeadlineAt     time.Time
	NextAttemptAt  time.Time
	DeferExhausted bool
	FinalStatus    string
	UpdatedAt      time.Time
}

type CommandResultOutboxEntry struct {
	ID             int64
	AgentID        string
	Transport      string
	CommandID      string
	IdempotencyKey string
	PayloadJSON    string
	PayloadHash    string
	Attempts       int
	NextAttemptAt  time.Time
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ExpiresAt      time.Time
}

type P2PTelemetryOutboxEntry struct {
	ID             int64
	AgentID        string
	IdempotencyKey string
	PayloadJSON    string
	PayloadHash    string
	Attempts       int
	NextAttemptAt  time.Time
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ExpiresAt      time.Time
}

type ConsolidationWindowStateEntry struct {
	AgentID       string
	DataType      string
	WindowMode    string
	WindowStartAt time.Time
	LastFlushAt   time.Time
	UpdatedAt     time.Time
}

type ActionQueueEntry struct {
	ActionID     string
	UserSID      string
	UserName     string
	Command      string
	PayloadJSON  string
	Status       string
	QueuedAt     time.Time
	StartedAt    time.Time
	CompletedAt  time.Time
	ResultJSON   string
	ErrorMessage string
}

type ActionHistoryEntry struct {
	ID           int64
	ActionID     string
	UserSID      string
	UserName     string
	Command      string
	Status       string
	ExitCode     int
	ExitCodeSet  bool
	Output       string
	ErrorMessage string
	CompletedAt  time.Time
}

// Open abre/cria o banco de dados SQLite no diretório especificado
func Open(dataDir string) (*DB, error) {
	dbPath := filepath.Join(dataDir, "discovery.db")

	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("erro ao abrir database: %w", err)
	}

	// Configurações de performance
	conn.SetMaxOpenConns(1) // SQLite funciona melhor com single connection
	conn.Exec("PRAGMA journal_mode=WAL")
	conn.Exec("PRAGMA synchronous=NORMAL")
	conn.Exec("PRAGMA cache_size=-64000") // 64MB cache

	db := &DB{conn: conn}
	if err := db.initialize(); err != nil {
		conn.Close()
		return nil, err
	}

	return db, nil
}

// initialize cria as tabelas necessárias
func (db *DB) initialize() error {
	schema := `
		CREATE TABLE IF NOT EXISTS cache (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			expires_at INTEGER
		);

		CREATE INDEX IF NOT EXISTS idx_cache_expires ON cache(expires_at);

		CREATE TABLE IF NOT EXISTS inventory_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			agent_id TEXT NOT NULL,
			collected_at INTEGER NOT NULL,
			hardware_json TEXT,
			software_json TEXT
		);

		CREATE INDEX IF NOT EXISTS idx_inventory_agent ON inventory_history(agent_id, collected_at DESC);

		CREATE TABLE IF NOT EXISTS sync_control (
			key TEXT PRIMARY KEY,
			last_sync_at INTEGER NOT NULL,
			metadata TEXT
		);

		CREATE TABLE IF NOT EXISTS automation_policy_state (
			agent_id TEXT PRIMARY KEY,
			fingerprint TEXT NOT NULL,
			updated_at INTEGER NOT NULL,
			payload_json TEXT NOT NULL
		);

		CREATE TABLE IF NOT EXISTS automation_execution_history (
			execution_id TEXT PRIMARY KEY,
			agent_id TEXT NOT NULL,
			command_id TEXT,
			task_id TEXT,
			task_name TEXT,
			action_type TEXT,
			installation_type TEXT,
			source_type TEXT,
			trigger_type TEXT,
			status TEXT NOT NULL,
			correlation_id TEXT,
			started_at INTEGER NOT NULL,
			finished_at INTEGER,
			success INTEGER,
			exit_code INTEGER,
			error_message TEXT,
			output TEXT,
			package_id TEXT,
			script_id TEXT,
			metadata_json TEXT
		);

		CREATE INDEX IF NOT EXISTS idx_automation_execution_agent_started ON automation_execution_history(agent_id, started_at DESC);

		CREATE TABLE IF NOT EXISTS automation_callback_queue (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			agent_id TEXT NOT NULL,
			execution_id TEXT NOT NULL,
			command_id TEXT NOT NULL,
			callback_type TEXT NOT NULL,
			correlation_id TEXT,
			payload_json TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			next_attempt_at INTEGER NOT NULL,
			last_error TEXT,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_automation_callback_due ON automation_callback_queue(agent_id, next_attempt_at);

		CREATE TABLE IF NOT EXISTS automation_marker_state (
			agent_id TEXT NOT NULL,
			marker_key TEXT NOT NULL,
			marker_value TEXT NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (agent_id, marker_key)
		);

		CREATE TABLE IF NOT EXISTS memory_notes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			content TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);

		CREATE TABLE IF NOT EXISTS action_queue (
			action_id TEXT PRIMARY KEY,
			user_sid TEXT NOT NULL,
			user_name TEXT NOT NULL,
			command TEXT NOT NULL,
			payload_json TEXT,
			status TEXT NOT NULL DEFAULT 'queued',
			queued_at INTEGER NOT NULL,
			started_at INTEGER,
			completed_at INTEGER,
			result_json TEXT,
			error_message TEXT
		);

		CREATE INDEX IF NOT EXISTS idx_action_queue_user_status ON action_queue(user_sid, status, queued_at DESC);
		CREATE INDEX IF NOT EXISTS idx_action_queue_status ON action_queue(status, queued_at DESC);

		CREATE TABLE IF NOT EXISTS action_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			action_id TEXT NOT NULL,
			user_sid TEXT NOT NULL,
			user_name TEXT NOT NULL,
			command TEXT NOT NULL,
			status TEXT NOT NULL,
			exit_code INTEGER,
			output TEXT,
			error_message TEXT,
			completed_at INTEGER NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_action_history_user ON action_history(user_sid, completed_at DESC);
		CREATE INDEX IF NOT EXISTS idx_action_history_action ON action_history(action_id);

		CREATE TABLE IF NOT EXISTS security_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp INTEGER NOT NULL,
			event_type TEXT NOT NULL,
			severity TEXT NOT NULL,
			source_peer TEXT,
			description TEXT,
			details_json TEXT,
			remediation_taken TEXT
		);

		CREATE INDEX IF NOT EXISTS idx_security_events_timestamp ON security_events(timestamp DESC);
		CREATE INDEX IF NOT EXISTS idx_security_events_type ON security_events(event_type, timestamp DESC);

		CREATE TABLE IF NOT EXISTS psadt_bootstrap_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			required_version TEXT,
			installed INTEGER NOT NULL,
			installed_version TEXT,
			source TEXT,
			message TEXT,
			created_at INTEGER NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_psadt_bootstrap_created ON psadt_bootstrap_history(created_at DESC);

		CREATE TABLE IF NOT EXISTS notification_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			notification_id TEXT NOT NULL,
			mode TEXT,
			severity TEXT,
			event_type TEXT,
			title TEXT,
			result TEXT,
			agent_action TEXT,
			metadata_json TEXT,
			created_at INTEGER NOT NULL
		);

		CREATE TABLE IF NOT EXISTS automation_defer_state (
			agent_id TEXT NOT NULL,
			task_id TEXT NOT NULL,
			execution_id TEXT,
			defer_count INTEGER NOT NULL DEFAULT 0,
			first_defer_at INTEGER,
			last_defer_at INTEGER,
			deadline_at INTEGER,
			next_attempt_at INTEGER,
			defer_exhausted INTEGER NOT NULL DEFAULT 0,
			final_status TEXT,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (agent_id, task_id)
		);

		CREATE TABLE IF NOT EXISTS command_result_outbox (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			agent_id TEXT NOT NULL,
			transport TEXT,
			command_id TEXT NOT NULL,
			idempotency_key TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			payload_hash TEXT,
			attempts INTEGER NOT NULL DEFAULT 0,
			next_attempt_at INTEGER NOT NULL,
			last_error TEXT,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL
		);

		CREATE TABLE IF NOT EXISTS p2p_telemetry_outbox (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			agent_id TEXT NOT NULL,
			idempotency_key TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			payload_hash TEXT,
			attempts INTEGER NOT NULL DEFAULT 0,
			next_attempt_at INTEGER NOT NULL,
			last_error TEXT,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL
		);

		CREATE TABLE IF NOT EXISTS consolidation_window_state (
			agent_id TEXT NOT NULL,
			data_type TEXT NOT NULL,
			window_mode TEXT NOT NULL,
			window_start_at INTEGER,
			last_flush_at INTEGER,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (agent_id, data_type)
		);

		CREATE INDEX IF NOT EXISTS idx_notification_history_notification ON notification_history(notification_id, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_notification_history_created ON notification_history(created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_automation_defer_due ON automation_defer_state(agent_id, next_attempt_at);
		CREATE INDEX IF NOT EXISTS idx_command_result_outbox_due ON command_result_outbox(agent_id, next_attempt_at);
		CREATE INDEX IF NOT EXISTS idx_command_result_outbox_transport_due ON command_result_outbox(agent_id, transport, next_attempt_at);
		CREATE INDEX IF NOT EXISTS idx_command_result_outbox_exp ON command_result_outbox(expires_at);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_command_result_outbox_idempotency ON command_result_outbox(agent_id, idempotency_key);
		CREATE INDEX IF NOT EXISTS idx_p2p_telemetry_outbox_due ON p2p_telemetry_outbox(agent_id, next_attempt_at);
		CREATE INDEX IF NOT EXISTS idx_p2p_telemetry_outbox_exp ON p2p_telemetry_outbox(expires_at);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_p2p_telemetry_outbox_idempotency ON p2p_telemetry_outbox(agent_id, idempotency_key);
		CREATE INDEX IF NOT EXISTS idx_consolidation_window_updated ON consolidation_window_state(agent_id, updated_at DESC);
	`

	_, err := db.conn.Exec(schema)
	if err != nil {
		return fmt.Errorf("erro ao criar schema: %w", err)
	}

	// Limpar cache expirado no startup
	db.conn.Exec("DELETE FROM cache WHERE expires_at IS NOT NULL AND expires_at < ?", time.Now().Unix())

	return nil
}

// Close fecha a conexão com o database
func (db *DB) Close() error {
	if db.conn != nil {
		return db.conn.Close()
	}
	return nil
}
