package database

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// GetWindowsUpdateCache le o ultimo scan persistido do agente. A assinatura
// casa com inventory.WindowsUpdateCache.
func (db *DB) GetWindowsUpdateCache(agentID string) (payload []byte, hash string, scannedAt time.Time, found bool, err error) {
	if db == nil || db.conn == nil {
		return nil, "", time.Time{}, false, nil
	}
	var (
		payloadJSON string
		hashValue   string
		scannedUnix int64
	)
	queryErr := db.conn.QueryRow(
		"SELECT payload_json, payload_hash, scanned_at FROM windows_update_cache WHERE agent_id = ?",
		agentID,
	).Scan(&payloadJSON, &hashValue, &scannedUnix)
	if errors.Is(queryErr, sql.ErrNoRows) {
		return nil, "", time.Time{}, false, nil
	}
	if queryErr != nil {
		return nil, "", time.Time{}, false, fmt.Errorf("ler cache de windows update: %w", queryErr)
	}
	return []byte(payloadJSON), hashValue, time.Unix(scannedUnix, 0).UTC(), true, nil
}

// SaveWindowsUpdateCache grava o ultimo scan (idempotente por agent_id).
func (db *DB) SaveWindowsUpdateCache(agentID, hash string, payload []byte, scannedAt time.Time) error {
	if db == nil || db.conn == nil {
		return nil
	}
	now := time.Now().Unix()
	_, err := db.conn.Exec(
		`INSERT INTO windows_update_cache (agent_id, payload_json, payload_hash, scanned_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(agent_id) DO UPDATE SET
		   payload_json = excluded.payload_json,
		   payload_hash = excluded.payload_hash,
		   scanned_at = excluded.scanned_at,
		   updated_at = excluded.updated_at`,
		agentID, string(payload), hash, scannedAt.UTC().Unix(), now,
	)
	if err != nil {
		return fmt.Errorf("gravar cache de windows update: %w", err)
	}
	return nil
}

// GetWindowsUpdateRebootState le o estado persistido da sonda de reinicio.
// A assinatura casa com inventory.WindowsUpdateCache.
func (db *DB) GetWindowsUpdateRebootState(agentID string) (reasons []string, checkedAt time.Time, uploadedAt time.Time, found bool, err error) {
	if db == nil || db.conn == nil {
		return nil, time.Time{}, time.Time{}, false, nil
	}
	var (
		reasonsJSON  string
		checkedUnix  int64
		uploadedUnix sql.NullInt64
	)
	queryErr := db.conn.QueryRow(
		"SELECT reasons_json, checked_at, uploaded_at FROM windows_update_reboot_state WHERE agent_id = ?",
		agentID,
	).Scan(&reasonsJSON, &checkedUnix, &uploadedUnix)
	if errors.Is(queryErr, sql.ErrNoRows) {
		return nil, time.Time{}, time.Time{}, false, nil
	}
	if queryErr != nil {
		return nil, time.Time{}, time.Time{}, false, fmt.Errorf("ler estado de reinicio: %w", queryErr)
	}
	var parsed []string
	if reasonsJSON != "" {
		_ = json.Unmarshal([]byte(reasonsJSON), &parsed)
	}
	resultChecked := time.Unix(checkedUnix, 0).UTC()
	var resultUploaded time.Time
	if uploadedUnix.Valid {
		resultUploaded = time.Unix(uploadedUnix.Int64, 0).UTC()
	}
	return parsed, resultChecked, resultUploaded, true, nil
}

// SaveWindowsUpdateRebootState grava a sonda de reinicio. uploaded informa se
// o estado ja foi reportado ao servidor.
func (db *DB) SaveWindowsUpdateRebootState(agentID string, reasons []string, checkedAt time.Time, uploaded bool) error {
	if db == nil || db.conn == nil {
		return nil
	}
	reasonsJSON, err := json.Marshal(reasons)
	if err != nil {
		return err
	}
	var uploadedAt any
	if uploaded {
		uploadedAt = checkedAt.UTC().Unix()
	}
	_, err = db.conn.Exec(
		`INSERT INTO windows_update_reboot_state (agent_id, reasons_json, checked_at, uploaded_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(agent_id) DO UPDATE SET
		   reasons_json = excluded.reasons_json,
		   checked_at = excluded.checked_at,
		   uploaded_at = COALESCE(excluded.uploaded_at, windows_update_reboot_state.uploaded_at)`,
		agentID, string(reasonsJSON), checkedAt.UTC().Unix(), uploadedAt,
	)
	if err != nil {
		return fmt.Errorf("gravar estado de reinicio: %w", err)
	}
	return nil
}
