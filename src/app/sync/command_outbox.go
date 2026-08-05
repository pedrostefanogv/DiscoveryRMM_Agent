package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"discovery/app/core/agentconn"
	"discovery/app/core/database"
	"discovery/app/core/errutil"
	"discovery/app/syncmeta"
)

// CommandOutbox encapsula o outbox de resultados de comando (persistência
// local para retry quando offline). Depende de SyncDeps para config, db e log.
type CommandOutbox struct {
	deps    SyncDeps
	rollout *Rollout
}

// NewCommandOutbox cria um CommandOutbox com as dependências injetadas.
func NewCommandOutbox(deps SyncDeps, rollout *Rollout) *CommandOutbox {
	return &CommandOutbox{deps: deps, rollout: rollout}
}

// Enqueue persiste um resultado de comando no outbox, se o modo offline
// permitir.
func (o *CommandOutbox) Enqueue(transport, dispatchID, commandID string, exitCode int, output, errText, sendError string) error {
	if !o.rollout.ShouldEnqueueCommandResultOutbox() {
		return nil
	}
	db := o.deps.DB()
	if db == nil {
		return nil
	}
	agentID := strings.TrimSpace(o.deps.GetDebugConfig().AgentID)
	if agentID == "" {
		return nil
	}
	payload := syncmeta.CommandResultOutboxPayload{
		DispatchID:   strings.TrimSpace(dispatchID),
		CommandID:    strings.TrimSpace(commandID),
		ExitCode:     exitCode,
		Output:       output,
		ErrorMessage: errText,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	payloadHash := sha256.Sum256(payloadJSON)
	idempotencySuffix := payload.CommandID
	if strings.TrimSpace(payload.DispatchID) != "" {
		idempotencySuffix = payload.DispatchID
	}
	idempotencyKey := strings.TrimSpace(transport) + ":" + idempotencySuffix
	return db.EnqueueCommandResultOutbox(database.CommandResultOutboxEntry{
		AgentID:        agentID,
		Transport:      strings.TrimSpace(transport),
		CommandID:      payload.CommandID,
		IdempotencyKey: idempotencyKey,
		PayloadJSON:    string(payloadJSON),
		PayloadHash:    hex.EncodeToString(payloadHash[:]),
		Attempts:       0,
		NextAttemptAt:  time.Now(),
		LastError:      strings.TrimSpace(sendError),
		ExpiresAt:      time.Now().Add(14 * 24 * time.Hour),
	})
}

// ListDue retorna os itens do outbox prontos para envio.
func (o *CommandOutbox) ListDue(transport string, now time.Time, limit int) ([]agentconn.CommandResultOutboxItem, error) {
	if !o.rollout.ShouldDrainCommandResultOutbox() {
		return nil, nil
	}
	db := o.deps.DB()
	if db == nil {
		return nil, nil
	}
	agentID := strings.TrimSpace(o.deps.GetDebugConfig().AgentID)
	if agentID == "" {
		return nil, nil
	}
	entries, err := db.ListDueCommandResultOutbox(agentID, transport, now, limit)
	if err != nil {
		return nil, err
	}

	out := make([]agentconn.CommandResultOutboxItem, 0, len(entries))
	for _, entry := range entries {
		var payload syncmeta.CommandResultOutboxPayload
		if err := json.Unmarshal([]byte(entry.PayloadJSON), &payload); err != nil {
			o.deps.Log("[agent][outbox] payload inválido removido id=" + strconv.FormatInt(entry.ID, 10) + " erro=" + err.Error())
			errutil.LogIfErr(db.DeleteCommandResultOutbox(entry.ID), "outbox: remover payload inválido")
			continue
		}
		out = append(out, agentconn.CommandResultOutboxItem{
			ID:           entry.ID,
			DispatchID:   strings.TrimSpace(payload.DispatchID),
			CommandID:    strings.TrimSpace(payload.CommandID),
			ExitCode:     payload.ExitCode,
			Output:       payload.Output,
			ErrorMessage: payload.ErrorMessage,
			Attempts:     entry.Attempts,
		})
	}
	return out, nil
}

// MarkSent marca um item do outbox como enviado.
func (o *CommandOutbox) MarkSent(id int64) error {
	db := o.deps.DB()
	if db == nil {
		return nil
	}
	return db.MarkSentCommandResultOutbox(id)
}

// Reschedule reagenda um item do outbox para nova tentativa.
func (o *CommandOutbox) Reschedule(id int64, attempts int, nextAttemptAt time.Time, lastError string) error {
	db := o.deps.DB()
	if db == nil {
		return nil
	}
	return db.RescheduleCommandResultOutbox(id, attempts, nextAttemptAt, strings.TrimSpace(lastError))
}
