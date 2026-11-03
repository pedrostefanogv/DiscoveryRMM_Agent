package database

import (
	"database/sql"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Persistência local da policy de SENSORES.
//
// Espelho de SaveAutomationPolicy/GetAutomationPolicy (sqlite_automation.go),
// em tabela PRÓPRIA porque o recurso tem fingerprint e ciclo de sync
// independentes da automação. O banco é cache descartável: perder este snapshot
// só faz o agente rodar o primeiro sync antes de coletar.
// ─────────────────────────────────────────────────────────────────────────────

// SaveSensorConfig grava o snapshot local da policy de sensores do agente.
func (db *DB) SaveSensorConfig(agentID, fingerprint string, payload []byte) error {
	_, err := db.conn.Exec(
		"INSERT OR REPLACE INTO sensor_config_state (agent_id, fingerprint, updated_at, payload_json) VALUES (?, ?, ?, ?)",
		agentID, fingerprint, time.Now().Unix(), string(payload),
	)
	return err
}

// GetSensorConfig devolve o snapshot local da policy de sensores do agente.
func (db *DB) GetSensorConfig(agentID string) (payload []byte, fingerprint string, updatedAt time.Time, found bool, err error) {
	var raw string
	var ts int64
	err = db.conn.QueryRow(
		"SELECT payload_json, fingerprint, updated_at FROM sensor_config_state WHERE agent_id = ?",
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
