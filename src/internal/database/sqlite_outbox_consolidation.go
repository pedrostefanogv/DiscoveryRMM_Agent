package database

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func (db *DB) EnqueueCommandResultOutbox(entry CommandResultOutboxEntry) error {
	if strings.TrimSpace(entry.AgentID) == "" {
		return fmt.Errorf("agent_id obrigatorio")
	}
	if strings.TrimSpace(entry.CommandID) == "" {
		return fmt.Errorf("command_id obrigatorio")
	}
	if strings.TrimSpace(entry.IdempotencyKey) == "" {
		return fmt.Errorf("idempotency_key obrigatorio")
	}
	now := time.Now()
	next := entry.NextAttemptAt
	if next.IsZero() {
		next = now
	}
	expiresAt := entry.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = now.Add(14 * 24 * time.Hour)
	}
	_, err := db.conn.Exec(
		`INSERT OR IGNORE INTO command_result_outbox (agent_id, transport, command_id, idempotency_key, payload_json, payload_hash, attempts, next_attempt_at, last_error, created_at, updated_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(entry.AgentID),
		nullIfEmpty(strings.TrimSpace(entry.Transport)),
		strings.TrimSpace(entry.CommandID),
		strings.TrimSpace(entry.IdempotencyKey),
		entry.PayloadJSON,
		nullIfEmpty(strings.TrimSpace(entry.PayloadHash)),
		entry.Attempts,
		next.Unix(),
		nullIfEmpty(entry.LastError),
		now.Unix(),
		now.Unix(),
		expiresAt.Unix(),
	)
	return err
}

func (db *DB) ListDueCommandResultOutbox(agentID, transport string, now time.Time, limit int) ([]CommandResultOutboxEntry, error) {
	if limit <= 0 {
		limit = 10
	}
	transportFilter := strings.ToLower(strings.TrimSpace(transport))
	rows, err := db.conn.Query(
		`SELECT id, agent_id, COALESCE(transport, ''), command_id, idempotency_key, payload_json, COALESCE(payload_hash, ''), attempts, next_attempt_at, COALESCE(last_error, ''), created_at, updated_at, expires_at
		 FROM command_result_outbox WHERE agent_id = ? AND next_attempt_at <= ? AND expires_at > ?
		 AND (? = '' OR LOWER(COALESCE(transport, '')) = ?)
		 ORDER BY next_attempt_at ASC LIMIT ?`,
		strings.TrimSpace(agentID), now.Unix(), now.Unix(), transportFilter, transportFilter, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]CommandResultOutboxEntry, 0, limit)
	for rows.Next() {
		var entry CommandResultOutboxEntry
		var nextAttemptAt int64
		var createdAt int64
		var updatedAt int64
		var expiresAt int64
		if err := rows.Scan(&entry.ID, &entry.AgentID, &entry.Transport, &entry.CommandID, &entry.IdempotencyKey, &entry.PayloadJSON, &entry.PayloadHash, &entry.Attempts, &nextAttemptAt, &entry.LastError, &createdAt, &updatedAt, &expiresAt); err != nil {
			return nil, err
		}
		entry.NextAttemptAt = time.Unix(nextAttemptAt, 0)
		entry.CreatedAt = time.Unix(createdAt, 0)
		entry.UpdatedAt = time.Unix(updatedAt, 0)
		entry.ExpiresAt = time.Unix(expiresAt, 0)
		out = append(out, entry)
	}
	return out, rows.Err()
}

func (db *DB) RescheduleCommandResultOutbox(id int64, attempts int, nextAttemptAt time.Time, lastError string) error {
	_, err := db.conn.Exec(
		"UPDATE command_result_outbox SET attempts = ?, next_attempt_at = ?, last_error = ?, updated_at = ? WHERE id = ?",
		attempts, nextAttemptAt.Unix(), nullIfEmpty(lastError), time.Now().Unix(), id,
	)
	return err
}

func (db *DB) MarkSentCommandResultOutbox(id int64) error {
	return db.DeleteCommandResultOutbox(id)
}

func (db *DB) DeleteCommandResultOutbox(id int64) error {
	_, err := db.conn.Exec("DELETE FROM command_result_outbox WHERE id = ?", id)
	return err
}

func (db *DB) CountPendingCommandResultOutbox(agentID string) (int, error) {
	var count int
	err := db.conn.QueryRow("SELECT COUNT(1) FROM command_result_outbox WHERE agent_id = ?", strings.TrimSpace(agentID)).Scan(&count)
	return count, err
}

func (db *DB) CleanupExpiredCommandResultOutbox(now time.Time, limit int) (int64, error) {
	if limit <= 0 {
		limit = 100
	}
	result, err := db.conn.Exec(
		`DELETE FROM command_result_outbox WHERE id IN (
			SELECT id FROM command_result_outbox WHERE expires_at <= ? ORDER BY expires_at ASC LIMIT ?
		)`,
		now.Unix(), limit,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (db *DB) EnqueueP2PTelemetryOutbox(entry P2PTelemetryOutboxEntry) error {
	if strings.TrimSpace(entry.AgentID) == "" {
		return fmt.Errorf("agent_id obrigatorio")
	}
	if strings.TrimSpace(entry.IdempotencyKey) == "" {
		return fmt.Errorf("idempotency_key obrigatorio")
	}
	now := time.Now()
	next := entry.NextAttemptAt
	if next.IsZero() {
		next = now
	}
	expiresAt := entry.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = now.Add(14 * 24 * time.Hour)
	}
	_, err := db.conn.Exec(
		`INSERT OR IGNORE INTO p2p_telemetry_outbox (agent_id, idempotency_key, payload_json, payload_hash, attempts, next_attempt_at, last_error, created_at, updated_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(entry.AgentID),
		strings.TrimSpace(entry.IdempotencyKey),
		entry.PayloadJSON,
		nullIfEmpty(strings.TrimSpace(entry.PayloadHash)),
		entry.Attempts,
		next.Unix(),
		nullIfEmpty(entry.LastError),
		now.Unix(),
		now.Unix(),
		expiresAt.Unix(),
	)
	return err
}

func (db *DB) ListDueP2PTelemetryOutbox(agentID string, now time.Time, limit int) ([]P2PTelemetryOutboxEntry, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := db.conn.Query(
		`SELECT id, agent_id, idempotency_key, payload_json, COALESCE(payload_hash, ''), attempts, next_attempt_at, COALESCE(last_error, ''), created_at, updated_at, expires_at
		 FROM p2p_telemetry_outbox WHERE agent_id = ? AND next_attempt_at <= ? AND expires_at > ?
		 ORDER BY next_attempt_at ASC LIMIT ?`,
		strings.TrimSpace(agentID), now.Unix(), now.Unix(), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]P2PTelemetryOutboxEntry, 0, limit)
	for rows.Next() {
		var entry P2PTelemetryOutboxEntry
		var nextAttemptAt int64
		var createdAt int64
		var updatedAt int64
		var expiresAt int64
		if err := rows.Scan(&entry.ID, &entry.AgentID, &entry.IdempotencyKey, &entry.PayloadJSON, &entry.PayloadHash, &entry.Attempts, &nextAttemptAt, &entry.LastError, &createdAt, &updatedAt, &expiresAt); err != nil {
			return nil, err
		}
		entry.NextAttemptAt = time.Unix(nextAttemptAt, 0)
		entry.CreatedAt = time.Unix(createdAt, 0)
		entry.UpdatedAt = time.Unix(updatedAt, 0)
		entry.ExpiresAt = time.Unix(expiresAt, 0)
		out = append(out, entry)
	}
	return out, rows.Err()
}

func (db *DB) RescheduleP2PTelemetryOutbox(id int64, attempts int, nextAttemptAt time.Time, lastError string) error {
	_, err := db.conn.Exec(
		"UPDATE p2p_telemetry_outbox SET attempts = ?, next_attempt_at = ?, last_error = ?, updated_at = ? WHERE id = ?",
		attempts, nextAttemptAt.Unix(), nullIfEmpty(lastError), time.Now().Unix(), id,
	)
	return err
}

func (db *DB) MarkSentP2PTelemetryOutbox(id int64) error {
	return db.DeleteP2PTelemetryOutbox(id)
}

func (db *DB) DeleteP2PTelemetryOutbox(id int64) error {
	_, err := db.conn.Exec("DELETE FROM p2p_telemetry_outbox WHERE id = ?", id)
	return err
}

func (db *DB) CountPendingP2PTelemetryOutbox(agentID string) (int, error) {
	var count int
	err := db.conn.QueryRow("SELECT COUNT(1) FROM p2p_telemetry_outbox WHERE agent_id = ?", strings.TrimSpace(agentID)).Scan(&count)
	return count, err
}

func (db *DB) ExistsRecentP2PTelemetryOutboxHash(agentID, payloadHash string, since time.Time) (bool, error) {
	if strings.TrimSpace(agentID) == "" || strings.TrimSpace(payloadHash) == "" {
		return false, nil
	}
	var exists int
	err := db.conn.QueryRow(
		`SELECT 1 FROM p2p_telemetry_outbox
		 WHERE agent_id = ? AND payload_hash = ? AND created_at >= ?
		 ORDER BY created_at DESC LIMIT 1`,
		strings.TrimSpace(agentID), strings.TrimSpace(payloadHash), since.Unix(),
	).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return exists == 1, nil
}

func (db *DB) CleanupExpiredP2PTelemetryOutbox(now time.Time, limit int) (int64, error) {
	if limit <= 0 {
		limit = 100
	}
	result, err := db.conn.Exec(
		`DELETE FROM p2p_telemetry_outbox WHERE id IN (
			SELECT id FROM p2p_telemetry_outbox WHERE expires_at <= ? ORDER BY expires_at ASC LIMIT ?
		)`,
		now.Unix(), limit,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (db *DB) UpsertConsolidationWindowState(entry ConsolidationWindowStateEntry) error {
	agentID := strings.TrimSpace(entry.AgentID)
	dataType := strings.TrimSpace(entry.DataType)
	windowMode := strings.TrimSpace(entry.WindowMode)
	if agentID == "" || dataType == "" || windowMode == "" {
		return fmt.Errorf("agent_id, data_type e window_mode obrigatorios")
	}
	updatedAt := entry.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}
	_, err := db.conn.Exec(
		`INSERT INTO consolidation_window_state (agent_id, data_type, window_mode, window_start_at, last_flush_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(agent_id, data_type) DO UPDATE SET
			window_mode=excluded.window_mode,
			window_start_at=excluded.window_start_at,
			last_flush_at=excluded.last_flush_at,
			updated_at=excluded.updated_at`,
		agentID,
		dataType,
		windowMode,
		nullUnix(entry.WindowStartAt),
		nullUnix(entry.LastFlushAt),
		updatedAt.Unix(),
	)
	return err
}

func (db *DB) GetConsolidationWindowState(agentID, dataType string) (ConsolidationWindowStateEntry, bool, error) {
	var entry ConsolidationWindowStateEntry
	var windowStartAt sql.NullInt64
	var lastFlushAt sql.NullInt64
	var updatedAt int64
	err := db.conn.QueryRow(
		`SELECT agent_id, data_type, window_mode, window_start_at, last_flush_at, updated_at
		 FROM consolidation_window_state WHERE agent_id = ? AND data_type = ?`,
		strings.TrimSpace(agentID), strings.TrimSpace(dataType),
	).Scan(
		&entry.AgentID,
		&entry.DataType,
		&entry.WindowMode,
		&windowStartAt,
		&lastFlushAt,
		&updatedAt,
	)
	if err == sql.ErrNoRows {
		return ConsolidationWindowStateEntry{}, false, nil
	}
	if err != nil {
		return ConsolidationWindowStateEntry{}, false, err
	}
	entry.WindowStartAt = timeFromNullUnix(windowStartAt)
	entry.LastFlushAt = timeFromNullUnix(lastFlushAt)
	entry.UpdatedAt = time.Unix(updatedAt, 0)
	return entry, true, nil
}

func (db *DB) UpsertAutomationDeferState(entry AutomationDeferStateEntry) error {
	agentID := strings.TrimSpace(entry.AgentID)
	taskID := strings.TrimSpace(entry.TaskID)
	if agentID == "" || taskID == "" {
		return fmt.Errorf("agent_id e task_id obrigatorios")
	}
	updatedAt := time.Now()
	if !entry.UpdatedAt.IsZero() {
		updatedAt = entry.UpdatedAt
	}
	deferExhausted := 0
	if entry.DeferExhausted {
		deferExhausted = 1
	}

	_, err := db.conn.Exec(
		`INSERT INTO automation_defer_state (agent_id, task_id, execution_id, defer_count, first_defer_at, last_defer_at, deadline_at, next_attempt_at, defer_exhausted, final_status, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(agent_id, task_id) DO UPDATE SET
			execution_id=excluded.execution_id,
			defer_count=excluded.defer_count,
			first_defer_at=excluded.first_defer_at,
			last_defer_at=excluded.last_defer_at,
			deadline_at=excluded.deadline_at,
			next_attempt_at=excluded.next_attempt_at,
			defer_exhausted=excluded.defer_exhausted,
			final_status=excluded.final_status,
			updated_at=excluded.updated_at`,
		agentID,
		taskID,
		nullIfEmpty(entry.ExecutionID),
		entry.DeferCount,
		nullUnix(entry.FirstDeferAt),
		nullUnix(entry.LastDeferAt),
		nullUnix(entry.DeadlineAt),
		nullUnix(entry.NextAttemptAt),
		deferExhausted,
		nullIfEmpty(entry.FinalStatus),
		updatedAt.Unix(),
	)
	return err
}

func (db *DB) GetAutomationDeferState(agentID, taskID string) (AutomationDeferStateEntry, bool, error) {
	var entry AutomationDeferStateEntry
	var executionID sql.NullString
	var firstDeferAt sql.NullInt64
	var lastDeferAt sql.NullInt64
	var deadlineAt sql.NullInt64
	var nextAttemptAt sql.NullInt64
	var finalStatus sql.NullString
	var deferExhausted int
	var updatedAt int64

	err := db.conn.QueryRow(
		`SELECT agent_id, task_id, execution_id, defer_count, first_defer_at, last_defer_at, deadline_at, next_attempt_at, defer_exhausted, final_status, updated_at
		 FROM automation_defer_state WHERE agent_id = ? AND task_id = ?`,
		strings.TrimSpace(agentID), strings.TrimSpace(taskID),
	).Scan(
		&entry.AgentID,
		&entry.TaskID,
		&executionID,
		&entry.DeferCount,
		&firstDeferAt,
		&lastDeferAt,
		&deadlineAt,
		&nextAttemptAt,
		&deferExhausted,
		&finalStatus,
		&updatedAt,
	)
	if err == sql.ErrNoRows {
		return AutomationDeferStateEntry{}, false, nil
	}
	if err != nil {
		return AutomationDeferStateEntry{}, false, err
	}
	entry.ExecutionID = strings.TrimSpace(executionID.String)
	entry.FinalStatus = strings.TrimSpace(finalStatus.String)
	entry.DeferExhausted = deferExhausted == 1
	entry.FirstDeferAt = timeFromNullUnix(firstDeferAt)
	entry.LastDeferAt = timeFromNullUnix(lastDeferAt)
	entry.DeadlineAt = timeFromNullUnix(deadlineAt)
	entry.NextAttemptAt = timeFromNullUnix(nextAttemptAt)
	entry.UpdatedAt = time.Unix(updatedAt, 0)
	return entry, true, nil
}

func (db *DB) ListAutomationDeferStates(agentID string, limit int) ([]AutomationDeferStateEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := db.conn.Query(
		`SELECT agent_id, task_id, COALESCE(execution_id, ''), defer_count, first_defer_at, last_defer_at, deadline_at, next_attempt_at, defer_exhausted, COALESCE(final_status, ''), updated_at
		 FROM automation_defer_state WHERE agent_id = ? ORDER BY updated_at DESC LIMIT ?`,
		strings.TrimSpace(agentID), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]AutomationDeferStateEntry, 0, limit)
	for rows.Next() {
		var entry AutomationDeferStateEntry
		var firstDeferAt sql.NullInt64
		var lastDeferAt sql.NullInt64
		var deadlineAt sql.NullInt64
		var nextAttemptAt sql.NullInt64
		var deferExhausted int
		var updatedAt int64
		if err := rows.Scan(&entry.AgentID, &entry.TaskID, &entry.ExecutionID, &entry.DeferCount, &firstDeferAt, &lastDeferAt, &deadlineAt, &nextAttemptAt, &deferExhausted, &entry.FinalStatus, &updatedAt); err != nil {
			return nil, err
		}
		entry.FirstDeferAt = timeFromNullUnix(firstDeferAt)
		entry.LastDeferAt = timeFromNullUnix(lastDeferAt)
		entry.DeadlineAt = timeFromNullUnix(deadlineAt)
		entry.NextAttemptAt = timeFromNullUnix(nextAttemptAt)
		entry.DeferExhausted = deferExhausted == 1
		entry.UpdatedAt = time.Unix(updatedAt, 0)
		out = append(out, entry)
	}
	return out, rows.Err()
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
