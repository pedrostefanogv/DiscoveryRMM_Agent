package database

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func (db *DB) EnqueueAction(entry ActionQueueEntry) error {
	if strings.TrimSpace(entry.ActionID) == "" {
		return fmt.Errorf("action_id obrigatorio")
	}
	if strings.TrimSpace(entry.Command) == "" {
		return fmt.Errorf("command obrigatorio")
	}

	queuedAt := entry.QueuedAt
	if queuedAt.IsZero() {
		queuedAt = time.Now()
	}

	status := strings.TrimSpace(entry.Status)
	if status == "" {
		status = "queued"
	}

	_, err := db.conn.Exec(
		`INSERT OR REPLACE INTO action_queue (action_id, user_sid, user_name, command, payload_json, status, queued_at, started_at, completed_at, result_json, error_message)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(entry.ActionID),
		strings.TrimSpace(entry.UserSID),
		strings.TrimSpace(entry.UserName),
		strings.TrimSpace(entry.Command),
		nullIfEmpty(entry.PayloadJSON),
		status,
		queuedAt.Unix(),
		nullUnix(entry.StartedAt),
		nullUnix(entry.CompletedAt),
		nullIfEmpty(entry.ResultJSON),
		nullIfEmpty(entry.ErrorMessage),
	)
	return err
}

func (db *DB) ClaimNextQueuedAction(now time.Time) (ActionQueueEntry, bool, error) {
	if now.IsZero() {
		now = time.Now()
	}

	tx, err := db.conn.Begin()
	if err != nil {
		return ActionQueueEntry{}, false, err
	}
	defer tx.Rollback()

	var entry ActionQueueEntry
	var queuedAt int64
	var startedAt sql.NullInt64
	var completedAt sql.NullInt64
	err = tx.QueryRow(
		`SELECT action_id, user_sid, user_name, command, COALESCE(payload_json, ''), status, queued_at, started_at, completed_at, COALESCE(result_json, ''), COALESCE(error_message, '')
		 FROM action_queue WHERE status = 'queued' ORDER BY queued_at ASC, action_id ASC LIMIT 1`,
	).Scan(
		&entry.ActionID,
		&entry.UserSID,
		&entry.UserName,
		&entry.Command,
		&entry.PayloadJSON,
		&entry.Status,
		&queuedAt,
		&startedAt,
		&completedAt,
		&entry.ResultJSON,
		&entry.ErrorMessage,
	)
	if err == sql.ErrNoRows {
		return ActionQueueEntry{}, false, nil
	}
	if err != nil {
		return ActionQueueEntry{}, false, err
	}

	result, err := tx.Exec(
		"UPDATE action_queue SET status = ?, started_at = ?, error_message = NULL WHERE action_id = ? AND status = 'queued'",
		"running", now.Unix(), entry.ActionID,
	)
	if err != nil {
		return ActionQueueEntry{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return ActionQueueEntry{}, false, err
	}
	if affected == 0 {
		return ActionQueueEntry{}, false, nil
	}

	if err := tx.Commit(); err != nil {
		return ActionQueueEntry{}, false, err
	}

	entry.Status = "running"
	entry.QueuedAt = time.Unix(queuedAt, 0)
	entry.StartedAt = now
	entry.CompletedAt = timeFromNullUnix(completedAt)
	if startedAt.Valid && startedAt.Int64 > 0 {
		entry.StartedAt = time.Unix(startedAt.Int64, 0)
	}

	return entry, true, nil
}

func (db *DB) CompleteAction(entry ActionQueueEntry, finalStatus string, exitCode *int, output, resultJSON, errorMessage string, completedAt time.Time) error {
	if strings.TrimSpace(entry.ActionID) == "" {
		return fmt.Errorf("action_id obrigatorio")
	}
	finalStatus = strings.TrimSpace(finalStatus)
	if finalStatus == "" {
		finalStatus = "completed"
	}
	if completedAt.IsZero() {
		completedAt = time.Now()
	}

	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var exitCodeValue any
	if exitCode != nil {
		exitCodeValue = *exitCode
	}

	_, err = tx.Exec(
		`UPDATE action_queue SET status = ?, completed_at = ?, result_json = ?, error_message = ? WHERE action_id = ?`,
		finalStatus,
		completedAt.Unix(),
		nullIfEmpty(resultJSON),
		nullIfEmpty(errorMessage),
		strings.TrimSpace(entry.ActionID),
	)
	if err != nil {
		return err
	}

	_, err = tx.Exec(
		`INSERT INTO action_history (action_id, user_sid, user_name, command, status, exit_code, output, error_message, completed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(entry.ActionID),
		strings.TrimSpace(entry.UserSID),
		strings.TrimSpace(entry.UserName),
		strings.TrimSpace(entry.Command),
		finalStatus,
		exitCodeValue,
		nullIfEmpty(output),
		nullIfEmpty(errorMessage),
		completedAt.Unix(),
	)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (db *DB) GetAction(actionID string) (ActionQueueEntry, bool, error) {
	var entry ActionQueueEntry
	var queuedAt int64
	var startedAt sql.NullInt64
	var completedAt sql.NullInt64
	err := db.conn.QueryRow(
		`SELECT action_id, user_sid, user_name, command, COALESCE(payload_json, ''), status, queued_at, started_at, completed_at, COALESCE(result_json, ''), COALESCE(error_message, '')
		 FROM action_queue WHERE action_id = ?`,
		strings.TrimSpace(actionID),
	).Scan(
		&entry.ActionID,
		&entry.UserSID,
		&entry.UserName,
		&entry.Command,
		&entry.PayloadJSON,
		&entry.Status,
		&queuedAt,
		&startedAt,
		&completedAt,
		&entry.ResultJSON,
		&entry.ErrorMessage,
	)
	if err == sql.ErrNoRows {
		return ActionQueueEntry{}, false, nil
	}
	if err != nil {
		return ActionQueueEntry{}, false, err
	}
	entry.QueuedAt = time.Unix(queuedAt, 0)
	entry.StartedAt = timeFromNullUnix(startedAt)
	entry.CompletedAt = timeFromNullUnix(completedAt)
	return entry, true, nil
}

func (db *DB) ListActionHistory(actionID string, limit int) ([]ActionHistoryEntry, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := db.conn.Query(
		`SELECT id, action_id, user_sid, user_name, command, status, exit_code, COALESCE(output, ''), COALESCE(error_message, ''), completed_at
		 FROM action_history WHERE (? = '' OR action_id = ?) ORDER BY completed_at DESC LIMIT ?`,
		strings.TrimSpace(actionID), strings.TrimSpace(actionID), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ActionHistoryEntry, 0, limit)
	for rows.Next() {
		var entry ActionHistoryEntry
		var exitCode sql.NullInt64
		var completedAt int64
		if err := rows.Scan(&entry.ID, &entry.ActionID, &entry.UserSID, &entry.UserName, &entry.Command, &entry.Status, &exitCode, &entry.Output, &entry.ErrorMessage, &completedAt); err != nil {
			return nil, err
		}
		if exitCode.Valid {
			entry.ExitCodeSet = true
			entry.ExitCode = int(exitCode.Int64)
		}
		entry.CompletedAt = time.Unix(completedAt, 0)
		out = append(out, entry)
	}
	return out, rows.Err()
}
