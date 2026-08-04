package database

import "testing"

func TestInventorySyncMethods_ReturnErrorWhenDBUnavailable(t *testing.T) {
	var db *DB

	if _, err := db.GetLastSyncTime("inventory_sync:test"); err == nil {
		t.Fatalf("expected GetLastSyncTime to fail when db is nil")
	}

	if _, _, err := db.ShouldSyncInventory("agent-1", []byte("{}"), []byte("{}")); err == nil {
		t.Fatalf("expected ShouldSyncInventory to fail when db is nil")
	}

	if err := db.SaveInventorySnapshot("agent-1", []byte("{}"), []byte("{}")); err == nil {
		t.Fatalf("expected SaveInventorySnapshot to fail when db is nil")
	}
}
