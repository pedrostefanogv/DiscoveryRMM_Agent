package inventory

import (
	"context"
	"strings"
	"testing"

	"discovery/app/debug"
	"discovery/internal/models"
)

type mockSyncDB struct {
	saved          bool
	savedAgentID   string
	savedHardware  []byte
	savedSoftware  []byte
	shouldSyncCall int
}

func (m *mockSyncDB) ShouldSyncInventory(agentID string, hardwareBody, softwareBody []byte) (bool, string, error) {
	m.shouldSyncCall++
	return true, "ok", nil
}

func (m *mockSyncDB) SaveInventorySnapshot(agentID string, hardwareBody, softwareBody []byte) error {
	m.saved = true
	m.savedAgentID = agentID
	m.savedHardware = hardwareBody
	m.savedSoftware = softwareBody
	return nil
}

func (m *mockSyncDB) UpdateLastSyncTime(key, status string) error {
	return nil
}

func TestSyncInventoryOnStartup_SavesLocalSnapshotWhenMissingCredentials(t *testing.T) {
	db := &mockSyncDB{}
	svc := &Service{
		db:      db,
		logf:    func(string) {},
		version: "dev",
		debugConfig: func() debug.Config {
			return debug.Config{}
		},
	}

	report := models.InventoryReport{
		CollectedAt: "2026-04-07T21:00:00Z",
		Hardware: models.HardwareInfo{
			Hostname: "pc-teste",
		},
		OS: models.OperatingSystem{
			Name: "Windows",
		},
	}

	svc.SyncInventoryOnStartup(context.Background(), report)

	if !db.saved {
		t.Fatalf("expected local snapshot to be saved")
	}
	if !strings.HasPrefix(db.savedAgentID, "local:pc-teste") {
		t.Fatalf("expected local snapshot agent id, got %q", db.savedAgentID)
	}
	if len(db.savedHardware) == 0 {
		t.Fatalf("expected hardware payload to be saved")
	}
	if len(db.savedSoftware) == 0 {
		t.Fatalf("expected software payload to be saved")
	}
	if db.shouldSyncCall != 0 {
		t.Fatalf("expected ShouldSyncInventory not to be called without remote credentials")
	}
}
