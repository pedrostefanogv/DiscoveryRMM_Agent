package database

import (
	"database/sql"
	"fmt"
	"strings"
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

// ─────────────────────────────────────────────────────────────────────────────
// Buffer DURÁVEL de amostras de sensores pendentes de entrega.
//
// POR QUE []byte/JSON E NÃO automation.SensorResult: app/core/automation JÁ importa
// app/core/database (service.go, service_state.go e vários testes). Importar
// automation aqui criaria CICLO DE IMPORTAÇÃO e o pacote não compilaria. Além de
// ser obrigatório, é o desenho certo: database é a camada de persistência genérica
// (já grava payload []byte em SaveSensorConfig e payload_json em
// automation_callback_queue) e não deve conhecer os DTOs do domínio. A
// (des)serialização de SensorResult fica do lado do automation, que é quem conhece
// o tipo. As assinaturas trafegam []byte JSON opaco.
//
// O lote é gravado como UMA LINHA POR AMOSTRA (não um blob por lote): assim cada
// ciclo de coleta é um INSERT em lote barato, e a remoção pós-entrega apaga só as
// linhas enviadas. A ordem FIFO vem do id AUTOINCREMENT.
// ─────────────────────────────────────────────────────────────────────────────

// SensorPendingSample é uma linha do buffer durável: o payload JSON de UM
// SensorResult mais os metadados que a poda e a remoção por lote precisam.
type SensorPendingSample struct {
	// ID é a chave da linha, usada para apagar exatamente o que foi entregue.
	ID int64
	// SensorID é desnormalizado do payload para diagnóstico e poda por sensor.
	SensorID string
	// Payload é o SensorResult serializado em JSON (opaco para este pacote).
	Payload []byte
	// CreatedAt é o instante da coleta, base da poda por idade.
	CreatedAt time.Time
}

// SavePendingSensorSamples grava amostras no buffer durável.
//
// A gravação é UMA transação: ou o lote inteiro entra, ou nada entra — um lote
// pela metade confundiria a remoção pós-entrega (que apaga por id). agentID vazio
// ou lista vazia são no-op (nada a persistir), não erro.
func (db *DB) SavePendingSensorSamples(agentID string, samples []SensorPendingSample) error {
	agentID = strings.TrimSpace(agentID)
	if db == nil || db.conn == nil || agentID == "" || len(samples) == 0 {
		return nil
	}
	tx, err := db.conn.Begin()
	if err != nil {
		return fmt.Errorf("iniciar transacao do buffer de sensores: %w", err)
	}
	// Rollback é no-op depois do Commit; garante limpeza em qualquer saída de erro.
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(
		"INSERT INTO sensor_pending_samples (agent_id, sensor_id, payload_json, created_at) VALUES (?, ?, ?, ?)",
	)
	if err != nil {
		return fmt.Errorf("preparar insert do buffer de sensores: %w", err)
	}
	defer stmt.Close()

	for _, sample := range samples {
		createdAt := sample.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now()
		}
		if _, err := stmt.Exec(agentID, sample.SensorID, string(sample.Payload), createdAt.Unix()); err != nil {
			return fmt.Errorf("gravar amostra pendente (sensor=%q): %w", sample.SensorID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("confirmar transacao do buffer de sensores: %w", err)
	}
	return nil
}

// ListPendingSensorSamples devolve até limit amostras do agente em ordem FIFO
// (mais antigas primeiro). limit <= 0 significa "sem teto".
func (db *DB) ListPendingSensorSamples(agentID string, limit int) ([]SensorPendingSample, error) {
	agentID = strings.TrimSpace(agentID)
	if db == nil || db.conn == nil || agentID == "" {
		return nil, nil
	}
	query := "SELECT id, sensor_id, payload_json, created_at FROM sensor_pending_samples WHERE agent_id = ? ORDER BY id ASC"
	args := []any{agentID}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listar amostras pendentes: %w", err)
	}
	defer rows.Close()

	var out []SensorPendingSample
	for rows.Next() {
		var (
			sample SensorPendingSample
			raw    string
			ts     int64
		)
		if err := rows.Scan(&sample.ID, &sample.SensorID, &raw, &ts); err != nil {
			return nil, fmt.Errorf("ler amostra pendente: %w", err)
		}
		sample.Payload = []byte(raw)
		sample.CreatedAt = time.Unix(ts, 0)
		out = append(out, sample)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("percorrer amostras pendentes: %w", err)
	}
	return out, nil
}

// DeletePendingSensorSamples apaga as linhas entregues por id. Lista vazia é
// no-op. A operação é idempotente: id já removido não é erro (a remoção acontece
// depois de um envio que pode ter sido concorrente com a poda).
func (db *DB) DeletePendingSensorSamples(ids []int64) error {
	if db == nil || db.conn == nil || len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	query := "DELETE FROM sensor_pending_samples WHERE id IN (" + strings.Join(placeholders, ",") + ")"
	if _, err := db.conn.Exec(query, args...); err != nil {
		return fmt.Errorf("apagar amostras pendentes entregues: %w", err)
	}
	return nil
}

// PrunePendingSensorSamples aplica a retenção do buffer: remove o que for mais
// ANTIGO que cutoff e, depois, o excedente acima de keepLatest (mantendo as
// keepLatest mais recentes). Devolve quantas linhas foram removidas.
//
// A ordem importa: a poda por idade roda primeiro porque é a que reflete a
// política de retenção (7 dias, espelhando o servidor); a poda por tamanho é o
// teto de disco (20000) e só age no excedente.
func (db *DB) PrunePendingSensorSamples(agentID string, cutoff time.Time, keepLatest int) (int64, error) {
	agentID = strings.TrimSpace(agentID)
	if db == nil || db.conn == nil || agentID == "" {
		return 0, nil
	}
	var removed int64

	if !cutoff.IsZero() {
		res, err := db.conn.Exec(
			"DELETE FROM sensor_pending_samples WHERE agent_id = ? AND created_at < ?",
			agentID, cutoff.Unix(),
		)
		if err != nil {
			return removed, fmt.Errorf("podar amostras pendentes por idade: %w", err)
		}
		if n, err := res.RowsAffected(); err == nil {
			removed += n
		}
	}

	if keepLatest > 0 {
		// NOT IN com subconsulta: preserva as keepLatest de maior id (mais
		// recentes) e apaga o resto do agente.
		res, err := db.conn.Exec(
			"DELETE FROM sensor_pending_samples "+
				"WHERE agent_id = ? AND id NOT IN ("+
				"SELECT id FROM sensor_pending_samples WHERE agent_id = ? ORDER BY id DESC LIMIT ?)",
			agentID, agentID, keepLatest,
		)
		if err != nil {
			return removed, fmt.Errorf("podar amostras pendentes por tamanho: %w", err)
		}
		if n, err := res.RowsAffected(); err == nil {
			removed += n
		}
	}
	return removed, nil
}

// CountPendingSensorSamples devolve quantas amostras estão persistidas aguardando
// entrega (observabilidade do painel de debug).
func (db *DB) CountPendingSensorSamples(agentID string) (int64, error) {
	agentID = strings.TrimSpace(agentID)
	if db == nil || db.conn == nil || agentID == "" {
		return 0, nil
	}
	var total int64
	if err := db.conn.QueryRow(
		"SELECT COUNT(*) FROM sensor_pending_samples WHERE agent_id = ?", agentID,
	).Scan(&total); err != nil {
		return 0, fmt.Errorf("contar amostras pendentes: %w", err)
	}
	return total, nil
}
