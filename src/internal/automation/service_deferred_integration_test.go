package automation

import (
	"testing"
	"time"

	"discovery/internal/database"
)

func TestDeferredStatePersistenceAndReload(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	cfg := func() RuntimeConfig {
		return RuntimeConfig{AgentID: "agent-integration"}
	}

	svc := NewService(cfg, nil)
	svc.SetDB(db)

	task := AutomationTask{TaskID: "task-1", ActionType: ActionInstallPackage}
	next := svc.recordAndGetNextDefer("agent-integration", "exec-1", task, deferState{}, resolvePSADTWelcomeOptions(task))
	if next.IsZero() {
		t.Fatalf("expected next attempt to be scheduled")
	}

	reloaded := NewService(cfg, nil)
	reloaded.SetDB(db)
	reloaded.loadDeferStateForAgent("agent-integration")

	reloaded.mu.RLock()
	state, ok := reloaded.deferByTask["task-1"]
	reloaded.mu.RUnlock()
	if !ok {
		t.Fatalf("expected deferred state to be loaded from db")
	}
	if state.Count != 1 {
		t.Fatalf("expected defer count=1, got %d", state.Count)
	}
	if state.NextAttempt.IsZero() {
		t.Fatalf("expected next attempt restored")
	}
}

func TestDeferredStateMarksCompletedOnClear(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	svc := NewService(func() RuntimeConfig { return RuntimeConfig{AgentID: "agent-integration"} }, nil)
	svc.SetDB(db)

	task := AutomationTask{TaskID: "task-2", ActionType: ActionInstallPackage}
	next := svc.recordAndGetNextDefer("agent-integration", "exec-2", task, deferState{}, resolvePSADTWelcomeOptions(task))
	if next.IsZero() {
		t.Fatalf("expected deferred execution to be scheduled")
	}

	svc.clearDeferState("agent-integration", "task-2", "Completed")

	entry, found, err := db.GetAutomationDeferState("agent-integration", "task-2")
	if err != nil {
		t.Fatalf("get defer state: %v", err)
	}
	if !found {
		t.Fatalf("expected defer state row to remain for audit")
	}
	if entry.FinalStatus != "Completed" {
		t.Fatalf("expected final status to be persisted, got %q", entry.FinalStatus)
	}
	if entry.DeferCount < 1 {
		t.Fatalf("expected defer count >= 1")
	}
	if time.Since(entry.UpdatedAt) > time.Minute {
		t.Fatalf("expected updatedAt to be recent")
	}
}

func TestDeferredStateDeadlineExhaustion(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	svc := NewService(func() RuntimeConfig { return RuntimeConfig{AgentID: "agent-integration"} }, nil)
	svc.SetDB(db)

	task := AutomationTask{
		TaskID:         "task-deadline",
		ActionType:     ActionInstallPackage,
		CommandPayload: `{"psadtWelcome":{"allowDefer":true,"deferTimes":3,"deferDeadline":"2000-01-01T00:00:00Z"}}`,
	}
	next := svc.recordAndGetNextDefer("agent-integration", "exec-deadline", task, deferState{}, resolvePSADTWelcomeOptions(task))
	if !next.IsZero() {
		t.Fatalf("expected no schedule when defer deadline is expired")
	}

	svc.mu.RLock()
	state, ok := svc.deferByTask[task.TaskID]
	svc.mu.RUnlock()
	if !ok {
		t.Fatalf("expected deferred state to exist")
	}
	if !state.Exhausted {
		t.Fatalf("expected exhausted state after expired deadline")
	}
}
