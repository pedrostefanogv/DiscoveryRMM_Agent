package database

import (
	"database/sql"
	"time"
)

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
