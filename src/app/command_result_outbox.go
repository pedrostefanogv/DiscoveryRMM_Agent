package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"discovery/internal/agentconn"
	"discovery/internal/database"
	"discovery/internal/errutil"
)

type commandResultOutboxPayload struct {
	CommandID    string `json:"commandId"`
	ExitCode     int    `json:"exitCode"`
	Output       string `json:"output,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

func (a *App) enqueueCommandResultOutbox(transport, commandID string, exitCode int, output, errText, sendError string) error {
	if !a.shouldEnqueueCommandResultOutbox() {
		return nil
	}
	if a.db == nil {
		return nil
	}
	agentID := strings.TrimSpace(a.GetDebugConfig().AgentID)
	if agentID == "" {
		return nil
	}
	payload := commandResultOutboxPayload{
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
	idempotencyKey := strings.TrimSpace(transport) + ":" + payload.CommandID
	return a.db.EnqueueCommandResultOutbox(database.CommandResultOutboxEntry{
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

func (a *App) listDueCommandResultOutbox(transport string, now time.Time, limit int) ([]agentconn.CommandResultOutboxItem, error) {
	if !a.shouldDrainCommandResultOutbox() {
		return nil, nil
	}
	if a.db == nil {
		return nil, nil
	}
	agentID := strings.TrimSpace(a.GetDebugConfig().AgentID)
	if agentID == "" {
		return nil, nil
	}
	entries, err := a.db.ListDueCommandResultOutbox(agentID, transport, now, limit)
	if err != nil {
		return nil, err
	}

	out := make([]agentconn.CommandResultOutboxItem, 0, len(entries))
	for _, entry := range entries {
		var payload commandResultOutboxPayload
		if err := json.Unmarshal([]byte(entry.PayloadJSON), &payload); err != nil {
			a.logs.append("[agent][outbox] payload invalido removido id=" + strconv.FormatInt(entry.ID, 10) + " erro=" + err.Error())
			errutil.LogIfErr(a.db.DeleteCommandResultOutbox(entry.ID), "outbox: remover payload invalido")
			continue
		}
		out = append(out, agentconn.CommandResultOutboxItem{
			ID:           entry.ID,
			CommandID:    strings.TrimSpace(payload.CommandID),
			ExitCode:     payload.ExitCode,
			Output:       payload.Output,
			ErrorMessage: payload.ErrorMessage,
			Attempts:     entry.Attempts,
		})
	}
	return out, nil
}

func (a *App) markSentCommandResultOutbox(id int64) error {
	if a.db == nil {
		return nil
	}
	return a.db.MarkSentCommandResultOutbox(id)
}

func (a *App) rescheduleCommandResultOutbox(id int64, attempts int, nextAttemptAt time.Time, lastError string) error {
	if a.db == nil {
		return nil
	}
	return a.db.RescheduleCommandResultOutbox(id, attempts, nextAttemptAt, strings.TrimSpace(lastError))
}
