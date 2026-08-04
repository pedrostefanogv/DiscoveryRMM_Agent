package database

import (
	"fmt"
	"strings"
	"time"
)

func (db *DB) SavePSADTBootstrapStatus(entry PSADTBootstrapEntry) error {
	installed := 0
	if entry.Installed {
		installed = 1
	}
	createdAt := time.Now()
	if !entry.CreatedAt.IsZero() {
		createdAt = entry.CreatedAt
	}
	_, err := db.conn.Exec(
		`INSERT INTO psadt_bootstrap_history (required_version, installed, installed_version, source, message, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		nullIfEmpty(entry.RequiredVersion),
		installed,
		nullIfEmpty(entry.InstalledVersion),
		nullIfEmpty(entry.Source),
		nullIfEmpty(entry.Message),
		createdAt.Unix(),
	)
	return err
}

func (db *DB) SaveNotificationEvent(entry NotificationEventEntry) error {
	notificationID := strings.TrimSpace(entry.NotificationID)
	if notificationID == "" {
		return fmt.Errorf("notification_id obrigatorio")
	}
	createdAt := time.Now()
	if !entry.CreatedAt.IsZero() {
		createdAt = entry.CreatedAt
	}
	_, err := db.conn.Exec(
		`INSERT INTO notification_history (notification_id, mode, severity, event_type, title, result, agent_action, metadata_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		notificationID,
		nullIfEmpty(entry.Mode),
		nullIfEmpty(entry.Severity),
		nullIfEmpty(entry.EventType),
		nullIfEmpty(entry.Title),
		nullIfEmpty(entry.Result),
		nullIfEmpty(entry.AgentAction),
		nullIfEmpty(entry.MetadataJSON),
		createdAt.Unix(),
	)
	return err
}

func (db *DB) ListRecentNotificationEvents(limit int) ([]NotificationEventEntry, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := db.conn.Query(
		`SELECT id, notification_id, COALESCE(mode, ''), COALESCE(severity, ''), COALESCE(event_type, ''),
			COALESCE(title, ''), COALESCE(result, ''), COALESCE(agent_action, ''), COALESCE(metadata_json, ''), created_at
		 FROM notification_history ORDER BY created_at DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]NotificationEventEntry, 0, limit)
	for rows.Next() {
		var item NotificationEventEntry
		var createdAt int64
		if err := rows.Scan(
			&item.ID,
			&item.NotificationID,
			&item.Mode,
			&item.Severity,
			&item.EventType,
			&item.Title,
			&item.Result,
			&item.AgentAction,
			&item.MetadataJSON,
			&createdAt,
		); err != nil {
			return nil, err
		}
		item.CreatedAt = time.Unix(createdAt, 0)
		out = append(out, item)
	}
	return out, rows.Err()
}
