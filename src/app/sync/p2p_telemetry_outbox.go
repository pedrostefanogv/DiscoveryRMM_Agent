package sync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"discovery/app/core/database"
	"discovery/app/p2p"
	"discovery/app/p2pmeta"
)

// P2PTelemetryOutbox encapsula o outbox de telemetria P2P (persistência local
// para retry quando offline). Depende de SyncDeps para config, db, log e envio.
type P2PTelemetryOutbox struct {
	deps    SyncDeps
	rollout *Rollout
}

// NewP2PTelemetryOutbox cria um P2PTelemetryOutbox com as dependências injetadas.
func NewP2PTelemetryOutbox(deps SyncDeps, rollout *Rollout) *P2PTelemetryOutbox {
	return &P2PTelemetryOutbox{deps: deps, rollout: rollout}
}

// Enqueue persiste um payload de telemetria no outbox, se o modo offline
// permitir.
func (o *P2PTelemetryOutbox) Enqueue(payload p2pmeta.TelemetryPayload, sendErr error) error {
	if !o.rollout.ShouldEnqueueP2PTelemetryOutbox() {
		return nil
	}
	db := o.deps.DB()
	if db == nil {
		return nil
	}
	agentID := strings.TrimSpace(payload.AgentID)
	if agentID == "" {
		agentID = strings.TrimSpace(o.deps.GetDebugConfig().AgentID)
	}
	if agentID == "" {
		return nil
	}
	payloadJSON, err := p2p.MarshalTelemetryPayload(payload)
	if err != nil {
		return err
	}
	hashBytes := sha256.Sum256(payloadJSON)
	payloadHash := hex.EncodeToString(hashBytes[:])
	alreadyQueued, err := db.ExistsRecentP2PTelemetryOutboxHash(agentID, payloadHash, time.Now().Add(-p2p.TelemetryDedupWindow))
	if err != nil {
		return err
	}
	if alreadyQueued {
		return nil
	}
	lastError := ""
	if sendErr != nil {
		lastError = sendErr.Error()
	}
	idempotencyKey := payloadHash + ":" + strconv.FormatInt(time.Now().Unix(), 10)
	return db.EnqueueP2PTelemetryOutbox(database.P2PTelemetryOutboxEntry{
		AgentID:        agentID,
		IdempotencyKey: idempotencyKey,
		PayloadJSON:    string(payloadJSON),
		PayloadHash:    payloadHash,
		Attempts:       0,
		NextAttemptAt:  time.Now(),
		LastError:      strings.TrimSpace(lastError),
		ExpiresAt:      time.Now().Add(14 * 24 * time.Hour),
	})
}

// Drain envia os itens do outbox prontos para envio.
func (o *P2PTelemetryOutbox) Drain(ctx context.Context, limit int) error {
	if !o.rollout.ShouldDrainP2PTelemetryOutbox() {
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
	if limit <= 0 {
		limit = p2p.TelemetryDrainLimit
	}
	entries, err := db.ListDueP2PTelemetryOutbox(agentID, time.Now(), limit)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		var payload p2pmeta.TelemetryPayload
		if err := json.Unmarshal([]byte(entry.PayloadJSON), &payload); err != nil {
			o.deps.Log("[p2p][api] payload outbox inválido removido id=" + strconv.FormatInt(entry.ID, 10) + " erro=" + err.Error())
			_ = db.DeleteP2PTelemetryOutbox(entry.ID)
			continue
		}
		if err := o.deps.PostP2PTelemetryPayload(ctx, payload, entry.IdempotencyKey); err != nil {
			attempt := entry.Attempts + 1
			nextAttemptAt := time.Now().Add(p2p.TelemetryRetryBackoff(attempt))
			_ = db.RescheduleP2PTelemetryOutbox(entry.ID, attempt, nextAttemptAt, err.Error())
			continue
		}
		_ = db.MarkSentP2PTelemetryOutbox(entry.ID)
	}
	return nil
}
