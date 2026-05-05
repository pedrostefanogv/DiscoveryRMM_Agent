package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	debugsvc "discovery/app/debug"
	"discovery/internal/database"
)

func newOfflineSyncTestApp(t *testing.T, rollout AgentRolloutConfig) *App {
	t.Helper()

	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	a := &App{
		db:          db,
		agentConfig: AgentConfiguration{Rollout: rollout},
	}
	a.debugSvc = debugsvc.NewService(debugsvc.Options{})
	a.debugSvc.ApplyRuntimeConnectionConfig("https", "example.local", "token", "agent-1", "", "")
	return a
}

func TestCommandResultOutboxLoggingOnlySkipsEnqueue(t *testing.T) {
	a := newOfflineSyncTestApp(t, AgentRolloutConfig{CommandResultOfflineMode: OfflineQueueModeLoggingOnly})

	if err := a.enqueueCommandResultOutbox("nats-wss", "cmd-1", 0, "ok", "", "network error"); err != nil {
		t.Fatalf("enqueue command result outbox: %v", err)
	}
	pending, err := a.db.CountPendingCommandResultOutbox("agent-1")
	if err != nil {
		t.Fatalf("count pending command results: %v", err)
	}
	if pending != 0 {
		t.Fatalf("expected 0 pending command results, got %d", pending)
	}
}

func TestCommandResultOutboxEnqueueOnlySkipsDrain(t *testing.T) {
	a := newOfflineSyncTestApp(t, AgentRolloutConfig{CommandResultOfflineMode: OfflineQueueModeEnqueueOnly})

	if err := a.enqueueCommandResultOutbox("nats-wss", "cmd-1", 0, "ok", "", "network error"); err != nil {
		t.Fatalf("enqueue command result outbox: %v", err)
	}
	pending, err := a.db.CountPendingCommandResultOutbox("agent-1")
	if err != nil {
		t.Fatalf("count pending command results: %v", err)
	}
	if pending != 1 {
		t.Fatalf("expected 1 pending command result, got %d", pending)
	}
	due, err := a.listDueCommandResultOutbox("nats-wss", time.Now().Add(time.Second), 10)
	if err != nil {
		t.Fatalf("list due command results: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("expected drain to be disabled, got %d due entries", len(due))
	}
}

func TestP2PTelemetryOutboxLoggingOnlySkipsEnqueue(t *testing.T) {
	a := newOfflineSyncTestApp(t, AgentRolloutConfig{P2PTelemetryOfflineMode: OfflineQueueModeLoggingOnly})
	payload := P2PTelemetryPayload{AgentID: "agent-1", CollectedAtUTC: time.Now().UTC().Format(time.RFC3339)}

	if err := a.enqueueP2PTelemetryOutbox(payload, errors.New("offline")); err != nil {
		t.Fatalf("enqueue p2p telemetry outbox: %v", err)
	}
	pending, err := a.db.CountPendingP2PTelemetryOutbox("agent-1")
	if err != nil {
		t.Fatalf("count pending telemetry: %v", err)
	}
	if pending != 0 {
		t.Fatalf("expected 0 pending telemetry rows, got %d", pending)
	}
}

func TestP2PTelemetryOutboxEnqueueOnlySkipsDrain(t *testing.T) {
	a := newOfflineSyncTestApp(t, AgentRolloutConfig{P2PTelemetryOfflineMode: OfflineQueueModeEnqueueOnly})
	payload := P2PTelemetryPayload{AgentID: "agent-1", CollectedAtUTC: time.Now().UTC().Format(time.RFC3339)}

	if err := a.enqueueP2PTelemetryOutbox(payload, errors.New("offline")); err != nil {
		t.Fatalf("enqueue p2p telemetry outbox: %v", err)
	}
	pending, err := a.db.CountPendingP2PTelemetryOutbox("agent-1")
	if err != nil {
		t.Fatalf("count pending telemetry: %v", err)
	}
	if pending != 1 {
		t.Fatalf("expected 1 pending telemetry row, got %d", pending)
	}
	if err := a.drainP2PTelemetryOutbox(context.Background(), 10); err != nil {
		t.Fatalf("drain p2p telemetry outbox: %v", err)
	}
	pending, err = a.db.CountPendingP2PTelemetryOutbox("agent-1")
	if err != nil {
		t.Fatalf("count pending telemetry after drain: %v", err)
	}
	if pending != 1 {
		t.Fatalf("expected drain to be disabled, got %d pending rows", pending)
	}
}

func TestMarshalP2PTelemetryPayloadRejectsOversize(t *testing.T) {
	payload := P2PTelemetryPayload{
		AgentID:        strings.Repeat("a", p2pTelemetryMaxPayloadBytes+1),
		CollectedAtUTC: time.Now().UTC().Format(time.RFC3339),
	}

	_, err := marshalP2PTelemetryPayload(payload)
	if err == nil {
		t.Fatalf("expected oversize telemetry payload to fail")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("%d", p2pTelemetryMaxPayloadBytes)) {
		t.Fatalf("expected error to mention payload limit, got %v", err)
	}
}
