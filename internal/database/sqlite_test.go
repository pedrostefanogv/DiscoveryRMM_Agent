package database

import "testing"

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
