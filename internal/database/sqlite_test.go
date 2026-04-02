package database

import (
	"testing"
)

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
