package database

import (
	"fmt"
	"time"
)

// AutoUpdateExecutionRetention é a retenção do ledger local de execuções de
// auto-update da Loja. Mesma ordem de grandeza do histórico de execuções da
// automação (30 dias): é cache local descartável, não auditoria permanente.
const AutoUpdateExecutionRetention = 30 * 24 * time.Hour

// Resultados canônicos do ledger (result).
const (
	AutoUpdateResultSuccess = "success"
	AutoUpdateResultError   = "error"
)

// AutoUpdateExecutionEntry é uma linha do ledger local de execuções de
// auto-update da Loja (F3). O resultado é o que o agente observou ao executar
// o upgrade via inventory.Service.Upgrade (gate da Loja + fila única).
type AutoUpdateExecutionEntry struct {
	ID               int64
	AgentID          string
	InstallationType string
	PackageID        string
	Source           string
	Target           string
	TargetVersion    string
	Result           string
	ErrorMessage     string
	ExecutedAt       time.Time
}

// InsertAutoUpdateExecution grava UMA execução no ledger e aplica a retenção
// (best-effort: falha de retenção não impede a gravação do registro atual).
func (db *DB) InsertAutoUpdateExecution(entry AutoUpdateExecutionEntry) error {
	if db == nil || db.conn == nil {
		return fmt.Errorf("database: conexão indisponível para o ledger de auto-update")
	}
	if entry.ExecutedAt.IsZero() {
		entry.ExecutedAt = time.Now().UTC()
	}
	res, err := db.conn.Exec(
		`INSERT INTO app_auto_update_executions
			(agent_id, installation_type, package_id, source, target, target_version, result, error_message, executed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.AgentID, entry.InstallationType, entry.PackageID,
		entry.Source, entry.Target, entry.TargetVersion,
		entry.Result, entry.ErrorMessage, entry.ExecutedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("database: inserir execução de auto-update: %w", err)
	}
	// LastInsertId preenche o ID da linha (best-effort: driver sem suporte
	// apenas deixa o ID zero, sem mascarar o sucesso da gravação).
	if id, idErr := res.LastInsertId(); idErr == nil {
		entry.ID = id
	}

	// Retenção best-effort (mesmo caminho tolerante dos outboxes).
	cutoff := time.Now().Add(-AutoUpdateExecutionRetention).Unix()
	if _, err := db.conn.Exec(
		`DELETE FROM app_auto_update_executions WHERE executed_at < ?`, cutoff,
	); err != nil {
		return fmt.Errorf("database: retenção do ledger de auto-update: %w", err)
	}
	return nil
}

// ListAutoUpdateExecutions devolve as últimas execuções do ledger do agente
// (mais recente primeiro, limite aplicado). Limit <= 0 → default 50.
func (db *DB) ListAutoUpdateExecutions(agentID string, limit int) ([]AutoUpdateExecutionEntry, error) {
	if db == nil || db.conn == nil {
		return nil, fmt.Errorf("database: conexão indisponível para o ledger de auto-update")
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := db.conn.Query(
		`SELECT id, agent_id, installation_type, package_id, source, target, target_version, result, error_message, executed_at
			FROM app_auto_update_executions
			WHERE agent_id = ?
			ORDER BY executed_at DESC, id DESC
			LIMIT ?`,
		agentID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("database: listar execuções de auto-update: %w", err)
	}
	defer rows.Close()

	out := make([]AutoUpdateExecutionEntry, 0, limit)
	for rows.Next() {
		var e AutoUpdateExecutionEntry
		var source, target, targetVersion, errorMessage any
		var executedAt int64
		if err := rows.Scan(&e.ID, &e.AgentID, &e.InstallationType, &e.PackageID,
			&source, &target, &targetVersion, &e.Result, &errorMessage, &executedAt); err != nil {
			return nil, fmt.Errorf("database: ler execução de auto-update: %w", err)
		}
		if v, ok := source.(string); ok {
			e.Source = v
		}
		if v, ok := target.(string); ok {
			e.Target = v
		}
		if v, ok := targetVersion.(string); ok {
			e.TargetVersion = v
		}
		if v, ok := errorMessage.(string); ok {
			e.ErrorMessage = v
		}
		e.ExecutedAt = time.Unix(executedAt, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}
