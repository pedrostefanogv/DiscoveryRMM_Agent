package inventory

import (
	"testing"
	"time"
)

func TestSaveSnapshotIfMissingSkipsWhenPreviousSnapshotExists(t *testing.T) {
	db := &mockSyncDB{}
	logs := []string{}

	saveSnapshotIfMissing(db, "agent-1", []byte("hw"), []byte("sw"), func(s string) { logs = append(logs, s) })

	if db.saved {
		t.Fatal("não deveria regravar snapshot idêntico (economia de ~840 KB por sync)")
	}
	if db.latestCall != 1 {
		t.Fatalf("latestCall = %d, want 1", db.latestCall)
	}
	if len(logs) != 0 {
		t.Fatalf("não deveria logar aviso, got %v", logs)
	}
}

func TestSaveSnapshotIfMissingWritesWhenNoPreviousSnapshot(t *testing.T) {
	db := &mockSyncDB{latestMissing: true}

	saveSnapshotIfMissing(db, "agent-1", []byte("hw"), []byte("sw"), func(string) {})

	if !db.saved {
		t.Fatal("sem snapshot anterior o snapshot deveria ser gravado")
	}
	if string(db.savedHardware) != "hw" || string(db.savedSoftware) != "sw" {
		t.Fatalf("payload salvo = %q/%q", db.savedHardware, db.savedSoftware)
	}
}

func TestSaveSnapshotIfMissingWritesWhenReadFails(t *testing.T) {
	db := &failingLatestDB{}
	logs := []string{}

	saveSnapshotIfMissing(db, "agent-1", []byte("hw"), []byte("sw"), func(s string) { logs = append(logs, s) })

	if !db.saved {
		t.Fatal("leitura com erro deveria cair no regravar")
	}
	if len(logs) == 0 {
		t.Fatal("esperava log de aviso quando o snapshot anterior não pôde ser lido")
	}
}

func TestSaveSnapshotIfMissingIgnoresNilDB(t *testing.T) {
	saveSnapshotIfMissing(nil, "agent-1", []byte("hw"), []byte("sw"), func(string) {})
}

// failingLatestDB devolve erro em GetLatestInventory.
type failingLatestDB struct {
	mockSyncDB
	err error
}

func (d *failingLatestDB) GetLatestInventory(agentID string) ([]byte, []byte, time.Time, error) {
	return nil, nil, time.Time{}, errLatestUnavailable
}

var errLatestUnavailable = errTestf("snapshot indisponivel")

type errTestf string

func (e errTestf) Error() string { return string(e) }
