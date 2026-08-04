package database

import (
	"strings"
	"testing"
	"time"
)

func TestCacheGetJSON_NilDBReturnsError(t *testing.T) {
	var db *DB
	var target map[string]any

	found, err := db.CacheGetJSON("agent_info", &target)
	if err == nil {
		t.Fatalf("expected error when DB is nil")
	}
	if found {
		t.Fatalf("expected found=false when DB is nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "database indisponivel") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInventorySoftwareChanged_IgnoresOrdering(t *testing.T) {
	oldJSON := []byte(`{"software":[{"name":"B","version":"1","publisher":"P","installId":"2","source":"registry"},{"name":"A","version":"1","publisher":"P","installId":"1","source":"registry"}],"collectedAt":"2026-01-01T10:00:00Z"}`)
	newJSON := []byte(`{"software":[{"name":"A","version":"1","publisher":"P","installId":"1","source":"registry"},{"name":"B","version":"1","publisher":"P","installId":"2","source":"registry"}],"collectedAt":"2026-01-02T10:00:00Z"}`)

	if inventorySoftwareChanged(oldJSON, newJSON) {
		t.Fatalf("expected no significant change when order differs only")
	}
}

func TestInventorySoftwareChanged_DetectsVersionChange(t *testing.T) {
	oldJSON := []byte(`{"software":[{"name":"App","version":"1.0","publisher":"P","installId":"1","source":"registry"}]}`)
	newJSON := []byte(`{"software":[{"name":"App","version":"2.0","publisher":"P","installId":"1","source":"registry"}]}`)

	if !inventorySoftwareChanged(oldJSON, newJSON) {
		t.Fatalf("expected significant change when version changes")
	}
}

func TestInventorySoftwareChanged_IgnoresInstallDateChange(t *testing.T) {
	oldJSON := []byte(`{"software":[{"name":"App","version":"1.0","publisher":"P","installId":"1","source":"registry","installDate":"2026-01-01"}]}`)
	newJSON := []byte(`{"software":[{"name":"App","version":"1.0","publisher":"P","installId":"1","source":"registry","installDate":"2026-02-01"}]}`)

	if inventorySoftwareChanged(oldJSON, newJSON) {
		t.Fatalf("expected no significant change when only installDate changes")
	}
}

func TestInventorySoftwareChanged_DetectsInstallSourceChange(t *testing.T) {
	oldJSON := []byte(`{"software":[{"name":"App","version":"1.0","publisher":"P","installId":"1","source":"registry","installSource":"C:/Apps/App"}]}`)
	newJSON := []byte(`{"software":[{"name":"App","version":"1.0","publisher":"P","installId":"1","source":"registry","installSource":"D:/Apps/App"}]}`)

	if !inventorySoftwareChanged(oldJSON, newJSON) {
		t.Fatalf("expected significant change when installSource changes")
	}
}

func TestSavePSADTBootstrapStatus(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	err = db.SavePSADTBootstrapStatus(PSADTBootstrapEntry{
		RequiredVersion:  "4.1.8",
		Installed:        true,
		InstalledVersion: "4.1.8",
		Source:           "powershell_gallery",
		Message:          "ok",
	})
	if err != nil {
		t.Fatalf("save bootstrap status: %v", err)
	}
}

func TestSaveAndListNotificationEvent(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	err = db.SaveNotificationEvent(NotificationEventEntry{
		NotificationID: "notif-1",
		Mode:           "notify_only",
		Severity:       "medium",
		EventType:      "maintenance",
		Title:          "Teste",
		Result:         "approved",
		AgentAction:    "rendered",
		MetadataJSON:   `{"ticket":"ABC-1"}`,
	})
	if err != nil {
		t.Fatalf("save notification event: %v", err)
	}

	events, err := db.ListRecentNotificationEvents(10)
	if err != nil {
		t.Fatalf("list notification events: %v", err)
	}
	if len(events) == 0 {
		t.Fatalf("expected at least one notification event")
	}
	if events[0].NotificationID != "notif-1" {
		t.Fatalf("unexpected notification id: %q", events[0].NotificationID)
	}
}

func TestUpsertAndListAutomationDeferState(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	err = db.UpsertAutomationDeferState(AutomationDeferStateEntry{
		AgentID:        "agent-1",
		TaskID:         "task-1",
		ExecutionID:    "exec-1",
		DeferCount:     2,
		FirstDeferAt:   now.Add(-2 * time.Hour),
		LastDeferAt:    now.Add(-1 * time.Hour),
		NextAttemptAt:  now.Add(30 * time.Minute),
		DeferExhausted: false,
		FinalStatus:    "deferred",
	})
	if err != nil {
		t.Fatalf("upsert defer state: %v", err)
	}

	entry, found, err := db.GetAutomationDeferState("agent-1", "task-1")
	if err != nil {
		t.Fatalf("get defer state: %v", err)
	}
	if !found {
		t.Fatalf("expected defer state to be found")
	}
	if entry.DeferCount != 2 {
		t.Fatalf("expected deferCount=2, got %d", entry.DeferCount)
	}

	states, err := db.ListAutomationDeferStates("agent-1", 10)
	if err != nil {
		t.Fatalf("list defer states: %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("expected one defer state entry, got %d", len(states))
	}
}

func TestEnqueueAction(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	err = db.EnqueueAction(ActionQueueEntry{
		ActionID:    "action-1",
		UserSID:     "S-1-5-21-123",
		UserName:    "DESKTOP\\pedro",
		Command:     "install_package",
		PayloadJSON: `{"action":"install_package","package":"7zip"}`,
		Status:      "queued",
		QueuedAt:    time.Now(),
	})
	if err != nil {
		t.Fatalf("enqueue action: %v", err)
	}

	var (
		actionID string
		userSID  string
		userName string
		command  string
		status   string
	)
	err = db.conn.QueryRow(
		"SELECT action_id, user_sid, user_name, command, status FROM action_queue WHERE action_id = ?",
		"action-1",
	).Scan(&actionID, &userSID, &userName, &command, &status)
	if err != nil {
		t.Fatalf("query queued action: %v", err)
	}

	if actionID != "action-1" {
		t.Fatalf("unexpected action_id: %s", actionID)
	}
	if userSID != "S-1-5-21-123" {
		t.Fatalf("unexpected user_sid: %s", userSID)
	}
	if userName != "DESKTOP\\pedro" {
		t.Fatalf("unexpected user_name: %s", userName)
	}
	if command != "install_package" {
		t.Fatalf("unexpected command: %s", command)
	}
	if status != "queued" {
		t.Fatalf("unexpected status: %s", status)
	}
}

func TestClaimAndCompleteAction(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	err = db.EnqueueAction(ActionQueueEntry{
		ActionID:    "action-2",
		UserSID:     "S-1-5-21-456",
		UserName:    "DESKTOP\\maria",
		Command:     "refresh_policy",
		PayloadJSON: `{"action":"refresh_policy"}`,
		Status:      "queued",
		QueuedAt:    time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("enqueue action: %v", err)
	}

	claimed, found, err := db.ClaimNextQueuedAction(time.Now())
	if err != nil {
		t.Fatalf("claim action: %v", err)
	}
	if !found {
		t.Fatalf("expected queued action to be claimed")
	}
	if claimed.Status != "running" {
		t.Fatalf("expected running status after claim, got %s", claimed.Status)
	}
	if claimed.StartedAt.IsZero() {
		t.Fatalf("expected started_at to be set")
	}

	err = db.CompleteAction(claimed, "completed", nil, "ok", `{"ok":true}`, "", time.Now())
	if err != nil {
		t.Fatalf("complete action: %v", err)
	}

	stored, found, err := db.GetAction("action-2")
	if err != nil {
		t.Fatalf("get action: %v", err)
	}
	if !found {
		t.Fatalf("expected action row after completion")
	}
	if stored.Status != "completed" {
		t.Fatalf("expected completed status, got %s", stored.Status)
	}
	if stored.CompletedAt.IsZero() {
		t.Fatalf("expected completed_at to be set")
	}

	history, err := db.ListActionHistory("action-2", 10)
	if err != nil {
		t.Fatalf("list action history: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("expected one action history row, got %d", len(history))
	}
	if history[0].Status != "completed" {
		t.Fatalf("expected completed history status, got %s", history[0].Status)
	}
	if history[0].Output != "ok" {
		t.Fatalf("unexpected history output: %s", history[0].Output)
	}
}
