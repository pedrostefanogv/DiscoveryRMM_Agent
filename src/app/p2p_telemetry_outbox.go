package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"discovery/internal/database"
)

const (
	p2pTelemetryRetryBase       = 30 * time.Second
	p2pTelemetryRetryMax        = 5 * time.Minute
	p2pTelemetryDrainLimit      = 20
	p2pTelemetryDedupWindow     = 5 * time.Minute
	p2pTelemetryMaxPayloadBytes = 1 << 20
)

func (a *App) buildP2PTelemetryPayload() (P2PTelemetryPayload, error) {
	if a.p2pCoord == nil {
		return P2PTelemetryPayload{}, fmt.Errorf("coordinator P2P indisponivel")
	}
	status := a.GetP2PDebugStatus()
	payload := P2PTelemetryPayload{
		AgentID:         strings.TrimSpace(a.GetDebugConfig().AgentID),
		CollectedAtUTC:  time.Now().UTC().Format(time.RFC3339),
		Metrics:         status.Metrics,
		CurrentSeedPlan: status.CurrentSeedPlan,
	}
	if info, err := a.GetAgentInfo(); err == nil {
		payload.SiteID = strings.TrimSpace(info.SiteID)
	}
	return payload, nil
}

func (a *App) enqueueP2PTelemetryOutbox(payload P2PTelemetryPayload, sendErr error) error {
	if !a.shouldEnqueueP2PTelemetryOutbox() {
		return nil
	}
	if a.db == nil {
		return nil
	}
	agentID := strings.TrimSpace(payload.AgentID)
	if agentID == "" {
		agentID = strings.TrimSpace(a.GetDebugConfig().AgentID)
	}
	if agentID == "" {
		return nil
	}
	payloadJSON, err := marshalP2PTelemetryPayload(payload)
	if err != nil {
		return err
	}
	hashBytes := sha256.Sum256(payloadJSON)
	payloadHash := hex.EncodeToString(hashBytes[:])
	alreadyQueued, err := a.db.ExistsRecentP2PTelemetryOutboxHash(agentID, payloadHash, time.Now().Add(-p2pTelemetryDedupWindow))
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
	return a.db.EnqueueP2PTelemetryOutbox(database.P2PTelemetryOutboxEntry{
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

func (a *App) drainP2PTelemetryOutbox(ctx context.Context, limit int) error {
	if !a.shouldDrainP2PTelemetryOutbox() {
		return nil
	}
	if a.db == nil {
		return nil
	}
	agentID := strings.TrimSpace(a.GetDebugConfig().AgentID)
	if agentID == "" {
		return nil
	}
	if limit <= 0 {
		limit = p2pTelemetryDrainLimit
	}
	entries, err := a.db.ListDueP2PTelemetryOutbox(agentID, time.Now(), limit)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		var payload P2PTelemetryPayload
		if err := json.Unmarshal([]byte(entry.PayloadJSON), &payload); err != nil {
			a.logs.append("[p2p][api] payload outbox invalido removido id=" + strconv.FormatInt(entry.ID, 10) + " erro=" + err.Error())
			_ = a.db.DeleteP2PTelemetryOutbox(entry.ID)
			continue
		}
		if err := a.postP2PTelemetryPayload(ctx, payload, entry.IdempotencyKey); err != nil {
			attempt := entry.Attempts + 1
			nextAttemptAt := time.Now().Add(p2pTelemetryRetryBackoff(attempt))
			_ = a.db.RescheduleP2PTelemetryOutbox(entry.ID, attempt, nextAttemptAt, err.Error())
			continue
		}
		_ = a.db.MarkSentP2PTelemetryOutbox(entry.ID)
	}
	return nil
}

func p2pTelemetryRetryBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		attempt = 1
	}
	backoff := p2pTelemetryRetryBase
	for i := 1; i < attempt; i++ {
		backoff *= 2
		if backoff >= p2pTelemetryRetryMax {
			backoff = p2pTelemetryRetryMax
			break
		}
	}
	return backoff
}

func marshalP2PTelemetryPayload(payload P2PTelemetryPayload) ([]byte, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(payloadJSON) > p2pTelemetryMaxPayloadBytes {
		return nil, fmt.Errorf("payload de telemetria excede limite de %d bytes", p2pTelemetryMaxPayloadBytes)
	}
	return payloadJSON, nil
}
