package database

import (
	"database/sql"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// DB encapsula a conexão SQLite e operações de cache
type DB struct {
	conn *sql.DB

	// Manutenção do WAL: checkpoint TRUNCATE + optimize periódicos.
	maintenanceMu   sync.Mutex
	maintenanceStop chan struct{}
	maintenanceWg   sync.WaitGroup
	closeOnce       sync.Once
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

	// Pragmas via DSN (modernc.org/sqlite): garantem que busy_timeout, WAL e
	// synchronous sejam aplicados a TODAS as conexões do pool, não apenas à
	// primeira. Crítico para o modo serviço (SYSTEM) + UI companion
	// (PLANO_AGENT_SERVICE_SYSTEM.md, Fase 0): dois processos escrevendo no
	// mesmo discovery.db — sem busy_timeout, SQLITE_BUSY vira erro intermitente.
	// journal_size_limit limita o WAL a 4 MB depois de cada checkpoint — sem
	// ele o arquivo cresce indefinidamente em máquina com dois processos
	// (serviço SYSTEM + UI companion) mantendo o mesmo discovery.db aberto.
	dsn := "file:" + dbPath + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)&_pragma=journal_size_limit(4194304)"
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("erro ao abrir database: %w", err)
	}

	// Configurações de performance
	conn.SetMaxOpenConns(1)               // SQLite funciona melhor com single connection
	conn.Exec("PRAGMA cache_size=-64000") // 64MB cache

	db := &DB{conn: conn}
	if err := db.initialize(); err != nil {
		conn.Close()
		return nil, err
	}

	return db, nil
}

// currentSchemaVersion é a versão do schema do discovery.db (M8).
//
// Incrementar QUANDO houver mudança incompatível no schema (ALTER/DROP/tipo).
// Mudanças puramente aditivas (tabela/índice novos via CREATE IF NOT EXISTS)
// NÃO precisam incrementar — o schema idempotente já as aplica preservando
// os dados.
//
// Política de dados (decisão do dono, M8): o discovery.db é CACHE descartável
// (inventário, histórico de execuções, outboxes, memórias). Quando a versão
// diverge da atual, o schema antigo é DESCARTADO e recriado do zero — o agente
// reprocessa e repopula. Sem migrations incrementais para manter.
const currentSchemaVersion = 2

// initialize cria as tabelas necessárias, com versionamento de schema (M8).
func (db *DB) initialize() error {
	// ── Versionamento do schema (M8) ──
	var schemaVersion int
	if err := db.conn.QueryRow("PRAGMA user_version").Scan(&schemaVersion); err != nil {
		return fmt.Errorf("erro ao ler PRAGMA user_version: %w", err)
	}

	if schemaVersion == currentSchemaVersion {
		return db.initializeCurrent()
	}

	// Versão divergente (banco legado v1 sem user_version, ou schema anterior):
	// os dados são cache descartável — descarta o schema antigo e recria na
	// versão atual. O agente reprocessa/repopula o que precisa.
	var tableCount int
	if err := db.conn.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tableCount); err != nil {
		return fmt.Errorf("erro ao inspecionar schema antigo: %w", err)
	}
	if tableCount > 0 {
		rows, err := db.conn.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'")
		if err != nil {
			return fmt.Errorf("erro ao listar tabelas antigas: %w", err)
		}
		var oldTables []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err == nil {
				oldTables = append(oldTables, name)
			}
		}
		rows.Close()
		for _, name := range oldTables {
			// Nomes vêm do sqlite_master (não são input de usuário); escapamos
			// aspas duplas por rigor. O identificador não pode ser parametrizado.
			safe := strings.ReplaceAll(name, `"`, `""`)
			if _, err := db.conn.Exec(`DROP TABLE IF EXISTS "` + safe + `"`); err != nil {
				return fmt.Errorf("erro ao descartar tabela antiga %s: %w", name, err)
			}
		}
	}

	if err := db.initializeCurrent(); err != nil {
		return err
	}
	if _, err := db.conn.Exec(fmt.Sprintf("PRAGMA user_version = %d", currentSchemaVersion)); err != nil {
		return fmt.Errorf("erro ao gravar user_version: %w", err)
	}
	return nil
}

// initializeCurrent cria o schema da versão atual (idempotente) e limpa o
// cache expirado. Caller já validou a versão do schema.
func (db *DB) initializeCurrent() error {
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

		-- Watchdog de execuções presas: consulta por (agent_id, status) com corte em
		-- started_at. Sem este índice o sweep é full scan numa tabela com retenção
		-- de 30 dias.
		CREATE INDEX IF NOT EXISTS idx_automation_execution_agent_status_started ON automation_execution_history(agent_id, status, started_at);

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

// DefaultMaintenanceInterval é o período do checkpoint/optimize automáticos.
// Com journal_size_limit(4 MB) o WAL já fica limitado; o timer garante o
// truncamento também em execução longa, sem depender do shutdown.
const DefaultMaintenanceInterval = 30 * time.Minute

// CheckpointResult resume o resultado de PRAGMA wal_checkpoint(TRUNCATE).
type CheckpointResult struct {
	// Busy indica que leitores/escritores impediram o checkpoint completo.
	Busy bool
	// LogFrames é o total de frames no WAL no momento da chamada.
	LogFrames int
	// Checkpointed é quantos frames foram movidos para o .db.
	Checkpointed int
}

// Checkpoint move o WAL para o banco e tenta truncá-lo (TRUNCATE).
//
// Devolve erro apenas quando o PRAGMA falha; um checkpoint bloqueado por outro
// processo volta com Busy=true (esperado com serviço e UI ativos ao mesmo tempo).
func (db *DB) Checkpoint() (CheckpointResult, error) {
	var res CheckpointResult
	if db == nil || db.conn == nil {
		return res, nil
	}
	var busy, logFrames, checkpointed int
	if err := db.conn.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		return res, fmt.Errorf("wal_checkpoint: %w", err)
	}
	return CheckpointResult{Busy: busy != 0, LogFrames: logFrames, Checkpointed: checkpointed}, nil
}

// Optimize roda PRAGMA optimize (estatísticas para o planejador de queries).
// É barato e idempotente; falha nunca é fatal.
func (db *DB) Optimize() error {
	if db == nil || db.conn == nil {
		return nil
	}
	if _, err := db.conn.Exec("PRAGMA optimize"); err != nil {
		return fmt.Errorf("optimize: %w", err)
	}
	return nil
}

// StartMaintenance inicia checkpoint/optimize periódicos. Idempotente: uma
// segunda chamada é ignorada. Close encerra o ticker e aguarda a goroutine.
func (db *DB) StartMaintenance(interval time.Duration) {
	if db == nil || db.conn == nil || interval <= 0 {
		return
	}
	db.maintenanceMu.Lock()
	defer db.maintenanceMu.Unlock()
	if db.maintenanceStop != nil {
		return
	}
	stop := make(chan struct{})
	db.maintenanceStop = stop
	db.maintenanceWg.Add(1)
	go func() {
		defer db.maintenanceWg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if res, err := db.Checkpoint(); err != nil {
					log.Printf("[database] wal_checkpoint periódico: %v", err)
				} else if res.Busy {
					log.Printf("[database] wal_checkpoint periódico incompleto (leitor ativo): %d/%d páginas",
						res.Checkpointed, res.LogFrames)
				}
				if err := db.Optimize(); err != nil {
					log.Printf("[database] optimize periódico: %v", err)
				}
			case <-stop:
				return
			}
		}
	}()
}

// stopMaintenance encerra o ticker (se ativo) e aguarda a goroutine sair.
func (db *DB) stopMaintenance() {
	db.maintenanceMu.Lock()
	stop := db.maintenanceStop
	db.maintenanceStop = nil
	db.maintenanceMu.Unlock()
	if stop != nil {
		close(stop)
		db.maintenanceWg.Wait()
	}
}

// Close encerra a manutenção, faz checkpoint/optimize best-effort e fecha a
// conexão. Idempotente.
func (db *DB) Close() error {
	if db == nil {
		return nil
	}
	var err error
	db.closeOnce.Do(func() {
		db.stopMaintenance()
		// Best-effort: no encerramento o WAL é movido para o .db, reduzindo o
		// -wal residual quando o agente para de forma limpa.
		if res, cerr := db.Checkpoint(); cerr != nil {
			log.Printf("[database] wal_checkpoint no shutdown: %v", cerr)
		} else if res.Busy {
			log.Printf("[database] wal_checkpoint no shutdown incompleto (outro processo ativo): %d/%d páginas",
				res.Checkpointed, res.LogFrames)
		}
		if oerr := db.Optimize(); oerr != nil {
			log.Printf("[database] optimize no shutdown: %v", oerr)
		}
		if db.conn != nil {
			err = db.conn.Close()
		}
	})
	return err
}
