package inventory

import "testing"

type typedNilInventoryDB struct{}

func (d *typedNilInventoryDB) ShouldSyncInventory(agentID string, hardwareBody, softwareBody []byte) (bool, string, error) {
	return false, "", nil
}

func (d *typedNilInventoryDB) SaveInventorySnapshot(agentID string, hardwareBody, softwareBody []byte) error {
	return nil
}

func (d *typedNilInventoryDB) UpdateLastSyncTime(key, status string) error {
	return nil
}

func TestNewService_NormalizesTypedNilDB(t *testing.T) {
	var db *typedNilInventoryDB
	svc := NewService(Options{DB: db})
	if svc.db != nil {
		t.Fatalf("expected nil db after constructor normalization")
	}
}

func TestSetDB_NormalizesTypedNilDB(t *testing.T) {
	svc := NewService(Options{})

	var db *typedNilInventoryDB
	svc.SetDB(db)
	if svc.db != nil {
		t.Fatalf("expected nil db after SetDB normalization")
	}

	realDB := &typedNilInventoryDB{}
	svc.SetDB(realDB)
	if svc.db == nil {
		t.Fatalf("expected non-nil db after SetDB with concrete implementation")
	}
}
