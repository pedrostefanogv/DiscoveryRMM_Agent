package database

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
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

// CacheGet recupera um valor do cache (retorna nil se não existe ou expirou)
func (db *DB) CacheGet(key string) ([]byte, error) {
	var value string
	var expiresAt sql.NullInt64

	err := db.conn.QueryRow(
		"SELECT value, expires_at FROM cache WHERE key = ?",
		key,
	).Scan(&value, &expiresAt)

	if err == sql.ErrNoRows {
		return nil, nil // Não encontrado
	}
	if err != nil {
		return nil, err
	}

	// Verificar expiração
	if expiresAt.Valid && expiresAt.Int64 < time.Now().Unix() {
		db.conn.Exec("DELETE FROM cache WHERE key = ?", key)
		return nil, nil
	}

	return []byte(value), nil
}

// CacheSet armazena um valor no cache com TTL opcional (0 = sem expiração)
func (db *DB) CacheSet(key string, value []byte, ttl time.Duration) error {
	var expiresAt sql.NullInt64
	if ttl > 0 {
		expiresAt.Valid = true
		expiresAt.Int64 = time.Now().Add(ttl).Unix()
	}

	_, err := db.conn.Exec(
		"INSERT OR REPLACE INTO cache (key, value, expires_at) VALUES (?, ?, ?)",
		key, string(value), expiresAt,
	)
	return err
}

// CacheDelete remove um valor do cache
func (db *DB) CacheDelete(key string) error {
	_, err := db.conn.Exec("DELETE FROM cache WHERE key = ?", key)
	return err
}

// CacheSetJSON armazena um objeto JSON no cache
func (db *DB) CacheSetJSON(key string, obj interface{}, ttl time.Duration) error {
	data, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	return db.CacheSet(key, data, ttl)
}

// CacheGetJSON recupera um objeto JSON do cache
func (db *DB) CacheGetJSON(key string, target interface{}) (bool, error) {
	data, err := db.CacheGet(key)
	if err != nil {
		return false, err
	}
	if data == nil {
		return false, nil // Não encontrado
	}
	return true, json.Unmarshal(data, target)
}

// MemoryNote representa uma anotação/memória local persistida.
type MemoryNote struct {
	ID        int64     `json:"id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ListMemoryNotes retorna todas as notas ordenadas pela mais recente.
func (db *DB) ListMemoryNotes() ([]MemoryNote, error) {
	rows, err := db.conn.Query("SELECT id, content, created_at, updated_at FROM memory_notes ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]MemoryNote, 0)
	for rows.Next() {
		var n MemoryNote
		var createdAt, updatedAt int64
		if err := rows.Scan(&n.ID, &n.Content, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		n.CreatedAt = time.Unix(createdAt, 0)
		n.UpdatedAt = time.Unix(updatedAt, 0)
		out = append(out, n)
	}
	return out, rows.Err()
}

// CreateMemoryNote insere uma nova anotação e retorna o registro criado.
func (db *DB) CreateMemoryNote(content string) (MemoryNote, error) {
	now := time.Now().Unix()
	res, err := db.conn.Exec(
		"INSERT INTO memory_notes (content, created_at, updated_at) VALUES (?, ?, ?)",
		content, now, now,
	)
	if err != nil {
		return MemoryNote{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return MemoryNote{}, err
	}
	return MemoryNote{
		ID:        id,
		Content:   content,
		CreatedAt: time.Unix(now, 0),
		UpdatedAt: time.Unix(now, 0),
	}, nil
}

// DeleteMemoryNote remove uma anotação pelo ID.
func (db *DB) DeleteMemoryNote(id int64) error {
	_, err := db.conn.Exec("DELETE FROM memory_notes WHERE id = ?", id)
	return err
}

// SaveInventorySnapshot salva um snapshot de inventário para histórico
func (db *DB) SaveInventorySnapshot(agentID string, hardwareJSON, softwareJSON []byte) error {
	_, err := db.conn.Exec(
		"INSERT INTO inventory_history (agent_id, collected_at, hardware_json, software_json) VALUES (?, ?, ?, ?)",
		agentID, time.Now().Unix(), string(hardwareJSON), string(softwareJSON),
	)
	return err
}

// GetLatestInventory recupera o último snapshot de inventário para um agente
func (db *DB) GetLatestInventory(agentID string) (hardwareJSON, softwareJSON []byte, collectedAt time.Time, err error) {
	var hw, sw string
	var ts int64

	err = db.conn.QueryRow(
		"SELECT hardware_json, software_json, collected_at FROM inventory_history WHERE agent_id = ? ORDER BY collected_at DESC LIMIT 1",
		agentID,
	).Scan(&hw, &sw, &ts)

	if err == sql.ErrNoRows {
		return nil, nil, time.Time{}, nil
	}
	if err != nil {
		return nil, nil, time.Time{}, err
	}

	return []byte(hw), []byte(sw), time.Unix(ts, 0), nil
}

// CleanOldInventory remove snapshots de inventário mais antigos que X dias
func (db *DB) CleanOldInventory(days int) error {
	cutoff := time.Now().AddDate(0, 0, -days).Unix()
	_, err := db.conn.Exec("DELETE FROM inventory_history WHERE collected_at < ?", cutoff)
	return err
}

// GetLastSyncTime retorna quando foi o último sync bem-sucedido para uma chave
func (db *DB) GetLastSyncTime(key string) (time.Time, error) {
	var ts int64
	err := db.conn.QueryRow(
		"SELECT last_sync_at FROM sync_control WHERE key = ?",
		key,
	).Scan(&ts)

	if err == sql.ErrNoRows {
		return time.Time{}, nil // Nunca sincronizado
	}
	if err != nil {
		return time.Time{}, err
	}

	return time.Unix(ts, 0), nil
}

// UpdateLastSyncTime atualiza o timestamp do último sync bem-sucedido
func (db *DB) UpdateLastSyncTime(key string, metadata string) error {
	_, err := db.conn.Exec(
		"INSERT OR REPLACE INTO sync_control (key, last_sync_at, metadata) VALUES (?, ?, ?)",
		key, time.Now().Unix(), metadata,
	)
	return err
}

// ShouldSyncInventory verifica se deve enviar inventário para API com base em:
// - Foi modificado desde último sync OU
// - Passou mais de 24h desde último sync
func (db *DB) ShouldSyncInventory(agentID string, currentHardware, currentSoftware []byte) (bool, string, error) {
	// 1. Verificar quando foi o último sync
	lastSync, err := db.GetLastSyncTime("inventory_sync:" + agentID)
	if err != nil {
		return false, "", err
	}

	// Se nunca sincronizou, deve sincronizar
	if lastSync.IsZero() {
		return true, "primeiro sync", nil
	}

	// 2. Verificar se passou 24h
	if time.Since(lastSync) >= 24*time.Hour {
		return true, "passou 24h desde ultimo sync", nil
	}

	// 3. Comparar com último snapshot
	lastHW, lastSW, _, err := db.GetLatestInventory(agentID)
	if err != nil {
		return false, "", err
	}

	// Se não há snapshot anterior, deve sincronizar
	if lastHW == nil && lastSW == nil {
		return true, "sem snapshot anterior", nil
	}

	// 4. Detectar mudanças significativas
	hwChanged := string(lastHW) != string(currentHardware)
	swChanged := inventorySoftwareChanged(lastSW, currentSoftware)

	if hwChanged {
		return true, "hardware modificado", nil
	}
	if swChanged {
		return true, "software modificado significativamente", nil
	}

	return false, "sem mudancas significativas", nil
}

// inventorySoftwareChanged compara listas de software e retorna true se houver
// mudanças significativas (ignorando pequenas variações de versão patch)
func inventorySoftwareChanged(oldJSON, newJSON []byte) bool {
	if len(oldJSON) == 0 && len(newJSON) == 0 {
		return false
	}
	if len(oldJSON) == 0 || len(newJSON) == 0 {
		return true
	}

	oldNorm, oldErr := normalizeInventorySoftwareJSON(oldJSON)
	newNorm, newErr := normalizeInventorySoftwareJSON(newJSON)

	// Fallback seguro para formato inesperado.
	if oldErr != nil || newErr != nil {
		return string(oldJSON) != string(newJSON)
	}

	if len(oldNorm) != len(newNorm) {
		return true
	}

	for i := range oldNorm {
		if oldNorm[i] != newNorm[i] {
			return true
		}
	}
	return false
}

func normalizeInventorySoftwareJSON(raw []byte) ([]string, error) {
	var payload struct {
		Software []struct {
			Name      string `json:"name"`
			Version   string `json:"version"`
			Publisher string `json:"publisher"`
			InstallID string `json:"installId"`
			Source    string `json:"source"`
		} `json:"software"`
	}

	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}

	items := make([]string, 0, len(payload.Software))
	for _, s := range payload.Software {
		key := strings.ToLower(strings.TrimSpace(s.Name)) + "|" +
			strings.TrimSpace(s.Version) + "|" +
			strings.ToLower(strings.TrimSpace(s.Publisher)) + "|" +
			strings.TrimSpace(s.InstallID) + "|" +
			strings.ToLower(strings.TrimSpace(s.Source))
		if strings.TrimSpace(key) == "||||" {
			continue
		}
		items = append(items, key)
	}
	sort.Strings(items)
	return items, nil
}

// SaveAutomationPolicy persiste o ultimo snapshot conhecido da policy de automacao.
func (db *DB) SaveAutomationPolicy(agentID, fingerprint string, payload []byte) error {
	_, err := db.conn.Exec(
		"INSERT OR REPLACE INTO automation_policy_state (agent_id, fingerprint, updated_at, payload_json) VALUES (?, ?, ?, ?)",
		agentID, fingerprint, time.Now().Unix(), string(payload),
	)
	return err
}

// GetAutomationPolicy retorna o snapshot persistido da policy de automacao para um agent.
func (db *DB) GetAutomationPolicy(agentID string) (payload []byte, fingerprint string, updatedAt time.Time, found bool, err error) {
	var raw string
	var ts int64
	err = db.conn.QueryRow(
		"SELECT payload_json, fingerprint, updated_at FROM automation_policy_state WHERE agent_id = ?",
		agentID,
	).Scan(&raw, &fingerprint, &ts)

	if err == sql.ErrNoRows {
		return nil, "", time.Time{}, false, nil
	}
	if err != nil {
		return nil, "", time.Time{}, false, err
	}

	return []byte(raw), fingerprint, time.Unix(ts, 0), true, nil
}

func (db *DB) UpsertAutomationExecution(entry AutomationExecutionEntry) error {
	var finishedAt any
	if !entry.FinishedAt.IsZero() {
		finishedAt = entry.FinishedAt.Unix()
	}
	var success any
	if entry.SuccessSet {
		if entry.Success {
			success = 1
		} else {
			success = 0
		}
	}
	var exitCode any
	if entry.ExitCodeSet {
		exitCode = entry.ExitCode
	}
	_, err := db.conn.Exec(
		`INSERT OR REPLACE INTO automation_execution_history (
			execution_id, agent_id, command_id, task_id, task_name, action_type, installation_type,
			source_type, trigger_type, status, correlation_id, started_at, finished_at, success,
			exit_code, error_message, output, package_id, script_id, metadata_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.ExecutionID,
		entry.AgentID,
		nullIfEmpty(entry.CommandID),
		nullIfEmpty(entry.TaskID),
		nullIfEmpty(entry.TaskName),
		nullIfEmpty(entry.ActionType),
		nullIfEmpty(entry.InstallationType),
		nullIfEmpty(entry.SourceType),
		nullIfEmpty(entry.TriggerType),
		entry.Status,
		nullIfEmpty(entry.CorrelationID),
		entry.StartedAt.Unix(),
		finishedAt,
		success,
		exitCode,
		nullIfEmpty(entry.ErrorMessage),
		nullIfEmpty(entry.Output),
		nullIfEmpty(entry.PackageID),
		nullIfEmpty(entry.ScriptID),
		nullIfEmpty(entry.MetadataJSON),
	)
	return err
}

func (db *DB) ListRecentAutomationExecutions(agentID string, limit int) ([]AutomationExecutionEntry, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := db.conn.Query(
		`SELECT execution_id, agent_id, COALESCE(command_id, ''), COALESCE(task_id, ''), COALESCE(task_name, ''),
			COALESCE(action_type, ''), COALESCE(installation_type, ''), COALESCE(source_type, ''), COALESCE(trigger_type, ''),
			status, COALESCE(correlation_id, ''), started_at, finished_at, success, exit_code,
			COALESCE(error_message, ''), COALESCE(output, ''), COALESCE(package_id, ''), COALESCE(script_id, ''), COALESCE(metadata_json, '')
		 FROM automation_execution_history WHERE agent_id = ? ORDER BY started_at DESC LIMIT ?`,
		agentID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := make([]AutomationExecutionEntry, 0, limit)
	for rows.Next() {
		var entry AutomationExecutionEntry
		var startedAt int64
		var finishedAt sql.NullInt64
		var success sql.NullInt64
		var exitCode sql.NullInt64
		if err := rows.Scan(
			&entry.ExecutionID, &entry.AgentID, &entry.CommandID, &entry.TaskID, &entry.TaskName,
			&entry.ActionType, &entry.InstallationType, &entry.SourceType, &entry.TriggerType,
			&entry.Status, &entry.CorrelationID, &startedAt, &finishedAt, &success, &exitCode,
			&entry.ErrorMessage, &entry.Output, &entry.PackageID, &entry.ScriptID, &entry.MetadataJSON,
		); err != nil {
			return nil, err
		}
		entry.StartedAt = time.Unix(startedAt, 0)
		if finishedAt.Valid {
			entry.FinishedAt = time.Unix(finishedAt.Int64, 0)
		}
		if success.Valid {
			entry.SuccessSet = true
			entry.Success = success.Int64 == 1
		}
		if exitCode.Valid {
			entry.ExitCodeSet = true
			entry.ExitCode = int(exitCode.Int64)
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (db *DB) EnqueueAutomationCallback(entry AutomationCallbackEntry) error {
	now := time.Now().Unix()
	next := entry.NextAttemptAt
	if next.IsZero() {
		next = time.Now()
	}
	_, err := db.conn.Exec(
		`INSERT INTO automation_callback_queue (agent_id, execution_id, command_id, callback_type, correlation_id, payload_json, attempts, next_attempt_at, last_error, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.AgentID,
		entry.ExecutionID,
		entry.CommandID,
		entry.CallbackType,
		nullIfEmpty(entry.CorrelationID),
		entry.PayloadJSON,
		entry.Attempts,
		next.Unix(),
		nullIfEmpty(entry.LastError),
		now,
		now,
	)
	return err
}

func (db *DB) ListDueAutomationCallbacks(agentID string, now time.Time, limit int) ([]AutomationCallbackEntry, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := db.conn.Query(
		`SELECT id, agent_id, execution_id, command_id, callback_type, COALESCE(correlation_id, ''), payload_json, attempts, next_attempt_at, COALESCE(last_error, ''), created_at, updated_at
		 FROM automation_callback_queue WHERE agent_id = ? AND next_attempt_at <= ? ORDER BY next_attempt_at ASC LIMIT ?`,
		agentID, now.Unix(), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := make([]AutomationCallbackEntry, 0, limit)
	for rows.Next() {
		var entry AutomationCallbackEntry
		var nextAttemptAt int64
		var createdAt int64
		var updatedAt int64
		if err := rows.Scan(&entry.ID, &entry.AgentID, &entry.ExecutionID, &entry.CommandID, &entry.CallbackType, &entry.CorrelationID, &entry.PayloadJSON, &entry.Attempts, &nextAttemptAt, &entry.LastError, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		entry.NextAttemptAt = time.Unix(nextAttemptAt, 0)
		entry.CreatedAt = time.Unix(createdAt, 0)
		entry.UpdatedAt = time.Unix(updatedAt, 0)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (db *DB) DeleteAutomationCallback(id int64) error {
	_, err := db.conn.Exec("DELETE FROM automation_callback_queue WHERE id = ?", id)
	return err
}

func (db *DB) RescheduleAutomationCallback(id int64, attempts int, nextAttemptAt time.Time, lastError string) error {
	_, err := db.conn.Exec(
		"UPDATE automation_callback_queue SET attempts = ?, next_attempt_at = ?, last_error = ?, updated_at = ? WHERE id = ?",
		attempts, nextAttemptAt.Unix(), nullIfEmpty(lastError), time.Now().Unix(), id,
	)
	return err
}

func (db *DB) CountPendingAutomationCallbacks(agentID string) (int, error) {
	var count int
	err := db.conn.QueryRow("SELECT COUNT(1) FROM automation_callback_queue WHERE agent_id = ?", agentID).Scan(&count)
	return count, err
}

func (db *DB) SetAutomationMarker(agentID, markerKey, markerValue string) error {
	_, err := db.conn.Exec(
		"INSERT OR REPLACE INTO automation_marker_state (agent_id, marker_key, marker_value, updated_at) VALUES (?, ?, ?, ?)",
		agentID, markerKey, markerValue, time.Now().Unix(),
	)
	return err
}

func (db *DB) GetAutomationMarker(agentID, markerKey string) (string, bool, error) {
	var value string
	err := db.conn.QueryRow(
		"SELECT marker_value FROM automation_marker_state WHERE agent_id = ? AND marker_key = ?",
		agentID, markerKey,
	).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

func nullIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
