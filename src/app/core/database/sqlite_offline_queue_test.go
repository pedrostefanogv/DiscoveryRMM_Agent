package database

import (
	"testing"
	"time"
)

func TestCommandResultOutboxLifecycle(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	agentID := "agent-1"
	now := time.Now().UTC()

	err = db.EnqueueCommandResultOutbox(CommandResultOutboxEntry{
		AgentID:        agentID,
		Transport:      "nats-wss",
		CommandID:      "cmd-1",
		IdempotencyKey: "idem-1",
		PayloadJSON:    `{"commandId":"cmd-1"}`,
		PayloadHash:    "hash-1",
		NextAttemptAt:  now,
		ExpiresAt:      now.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("enqueue command result outbox: %v", err)
	}

	pending, err := db.CountPendingCommandResultOutbox(agentID)
	if err != nil {
		t.Fatalf("count pending: %v", err)
	}
	if pending != 1 {
		t.Fatalf("expected 1 pending, got %d", pending)
	}

	due, err := db.ListDueCommandResultOutbox(agentID, "", now.Add(time.Second), 10)
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("expected 1 due entry, got %d", len(due))
	}

	nextAttempt := now.Add(10 * time.Minute)
	err = db.RescheduleCommandResultOutbox(due[0].ID, 1, nextAttempt, "network error")
	if err != nil {
		t.Fatalf("reschedule command result outbox: %v", err)
	}

	due, err = db.ListDueCommandResultOutbox(agentID, "", now.Add(time.Second), 10)
	if err != nil {
		t.Fatalf("list due after reschedule: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("expected 0 due entries after reschedule, got %d", len(due))
	}

	due, err = db.ListDueCommandResultOutbox(agentID, "", nextAttempt.Add(time.Second), 10)
	if err != nil {
		t.Fatalf("list due at future time: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("expected 1 due entry at future time, got %d", len(due))
	}

	err = db.MarkSentCommandResultOutbox(due[0].ID)
	if err != nil {
		t.Fatalf("mark sent command result outbox: %v", err)
	}

	pending, err = db.CountPendingCommandResultOutbox(agentID)
	if err != nil {
		t.Fatalf("count pending after mark sent: %v", err)
	}
	if pending != 0 {
		t.Fatalf("expected 0 pending after mark sent, got %d", pending)
	}
}

func TestCommandResultOutboxIdempotencyAndCleanup(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	agentID := "agent-1"
	now := time.Now().UTC()

	first := CommandResultOutboxEntry{
		AgentID:        agentID,
		Transport:      "nats",
		CommandID:      "cmd-1",
		IdempotencyKey: "idem-1",
		PayloadJSON:    `{"commandId":"cmd-1"}`,
		NextAttemptAt:  now,
		ExpiresAt:      now.Add(24 * time.Hour),
	}
	if err := db.EnqueueCommandResultOutbox(first); err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	if err := db.EnqueueCommandResultOutbox(first); err != nil {
		t.Fatalf("enqueue duplicate should not fail: %v", err)
	}

	pending, err := db.CountPendingCommandResultOutbox(agentID)
	if err != nil {
		t.Fatalf("count pending after duplicate: %v", err)
	}
	if pending != 1 {
		t.Fatalf("expected duplicate idempotency key to keep 1 row, got %d", pending)
	}

	err = db.EnqueueCommandResultOutbox(CommandResultOutboxEntry{
		AgentID:        agentID,
		Transport:      "nats",
		CommandID:      "cmd-exp",
		IdempotencyKey: "idem-exp",
		PayloadJSON:    `{"commandId":"cmd-exp"}`,
		NextAttemptAt:  now.Add(-2 * time.Hour),
		ExpiresAt:      now.Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("enqueue expired row: %v", err)
	}

	deleted, err := db.CleanupExpiredCommandResultOutbox(now, 100)
	if err != nil {
		t.Fatalf("cleanup expired: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 deleted expired row, got %d", deleted)
	}
}

func TestCommandResultOutboxListDueFiltersByTransport(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	agentID := "agent-transport"
	now := time.Now().UTC()
	entries := []CommandResultOutboxEntry{
		{
			AgentID:        agentID,
			Transport:      "nats-wss",
			CommandID:      "cmd-nats-wss",
			IdempotencyKey: "nats-wss:cmd-nats-wss",
			PayloadJSON:    `{"commandId":"cmd-nats-wss"}`,
			NextAttemptAt:  now,
			ExpiresAt:      now.Add(24 * time.Hour),
		},
		{
			AgentID:        agentID,
			Transport:      "nats",
			CommandID:      "cmd-nats",
			IdempotencyKey: "nats:cmd-nats",
			PayloadJSON:    `{"commandId":"cmd-nats"}`,
			NextAttemptAt:  now,
			ExpiresAt:      now.Add(24 * time.Hour),
		},
	}
	for _, entry := range entries {
		if err := db.EnqueueCommandResultOutbox(entry); err != nil {
			t.Fatalf("enqueue command result outbox: %v", err)
		}
	}

	natsWSSDue, err := db.ListDueCommandResultOutbox(agentID, "nats-wss", now.Add(time.Second), 10)
	if err != nil {
		t.Fatalf("list nats-wss due: %v", err)
	}
	if len(natsWSSDue) != 1 || natsWSSDue[0].Transport != "nats-wss" {
		t.Fatalf("expected only nats-wss due entry, got %+v", natsWSSDue)
	}

	natsDue, err := db.ListDueCommandResultOutbox(agentID, "nats", now.Add(time.Second), 10)
	if err != nil {
		t.Fatalf("list nats due: %v", err)
	}
	if len(natsDue) != 1 || natsDue[0].Transport != "nats" {
		t.Fatalf("expected only nats due entry, got %+v", natsDue)
	}
}

func TestP2PTelemetryOutboxLifecycleAndCleanup(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	agentID := "agent-2"
	now := time.Now().UTC()

	err = db.EnqueueP2PTelemetryOutbox(P2PTelemetryOutboxEntry{
		AgentID:        agentID,
		IdempotencyKey: "telem-1",
		PayloadJSON:    `{"agentId":"agent-2"}`,
		PayloadHash:    "telem-hash",
		NextAttemptAt:  now,
		ExpiresAt:      now.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("enqueue telemetry outbox: %v", err)
	}

	err = db.EnqueueP2PTelemetryOutbox(P2PTelemetryOutboxEntry{
		AgentID:        agentID,
		IdempotencyKey: "telem-1",
		PayloadJSON:    `{"agentId":"agent-2"}`,
		NextAttemptAt:  now,
		ExpiresAt:      now.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("enqueue duplicate telemetry should not fail: %v", err)
	}

	pending, err := db.CountPendingP2PTelemetryOutbox(agentID)
	if err != nil {
		t.Fatalf("count telemetry pending: %v", err)
	}
	if pending != 1 {
		t.Fatalf("expected 1 pending telemetry row, got %d", pending)
	}

	existsRecent, err := db.ExistsRecentP2PTelemetryOutboxHash(agentID, "telem-hash", now.Add(-5*time.Minute))
	if err != nil {
		t.Fatalf("exists recent telemetry hash: %v", err)
	}
	if !existsRecent {
		t.Fatalf("expected telemetry hash to exist in recent window")
	}

	due, err := db.ListDueP2PTelemetryOutbox(agentID, now.Add(time.Second), 10)
	if err != nil {
		t.Fatalf("list telemetry due: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("expected 1 due telemetry entry, got %d", len(due))
	}

	future := now.Add(5 * time.Minute)
	err = db.RescheduleP2PTelemetryOutbox(due[0].ID, 1, future, "timeout")
	if err != nil {
		t.Fatalf("reschedule telemetry outbox: %v", err)
	}

	due, err = db.ListDueP2PTelemetryOutbox(agentID, now.Add(time.Second), 10)
	if err != nil {
		t.Fatalf("list telemetry due after reschedule: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("expected no due telemetry row after reschedule, got %d", len(due))
	}

	err = db.EnqueueP2PTelemetryOutbox(P2PTelemetryOutboxEntry{
		AgentID:        agentID,
		IdempotencyKey: "telem-exp",
		PayloadJSON:    `{"agentId":"agent-2","expired":true}`,
		NextAttemptAt:  now.Add(-2 * time.Hour),
		ExpiresAt:      now.Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("enqueue expired telemetry row: %v", err)
	}

	deleted, err := db.CleanupExpiredP2PTelemetryOutbox(now, 100)
	if err != nil {
		t.Fatalf("cleanup expired telemetry: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 deleted expired telemetry row, got %d", deleted)
	}

	due, err = db.ListDueP2PTelemetryOutbox(agentID, future.Add(time.Second), 10)
	if err != nil {
		t.Fatalf("list due telemetry future: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("expected one telemetry row after cleanup, got %d", len(due))
	}

	if err := db.MarkSentP2PTelemetryOutbox(due[0].ID); err != nil {
		t.Fatalf("mark sent telemetry: %v", err)
	}
	pending, err = db.CountPendingP2PTelemetryOutbox(agentID)
	if err != nil {
		t.Fatalf("count pending telemetry after mark sent: %v", err)
	}
	if pending != 0 {
		t.Fatalf("expected 0 pending telemetry rows, got %d", pending)
	}
}

func TestConsolidationWindowStateUpsertAndGet(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	entry := ConsolidationWindowStateEntry{
		AgentID:       "agent-3",
		DataType:      "command_result",
		WindowMode:    "batch_1min",
		WindowStartAt: now,
		LastFlushAt:   now,
	}
	if err := db.UpsertConsolidationWindowState(entry); err != nil {
		t.Fatalf("upsert window state: %v", err)
	}

	stored, found, err := db.GetConsolidationWindowState("agent-3", "command_result")
	if err != nil {
		t.Fatalf("get window state: %v", err)
	}
	if !found {
		t.Fatalf("expected window state to be found")
	}
	if stored.WindowMode != "batch_1min" {
		t.Fatalf("unexpected window mode: %s", stored.WindowMode)
	}

	entry.WindowMode = "batch_5min"
	if err := db.UpsertConsolidationWindowState(entry); err != nil {
		t.Fatalf("upsert window state update: %v", err)
	}
	stored, found, err = db.GetConsolidationWindowState("agent-3", "command_result")
	if err != nil {
		t.Fatalf("get window state after update: %v", err)
	}
	if !found {
		t.Fatalf("expected updated window state to be found")
	}
	if stored.WindowMode != "batch_5min" {
		t.Fatalf("expected updated mode batch_5min, got %s", stored.WindowMode)
	}
}
