package memory

import (
	"strings"
	"testing"

	"discovery/app/core/database"
)

func TestServiceNilDBGuard(t *testing.T) {
	svc := New(Deps{
		DB: func() *database.DB { return nil },
	})

	if _, err := svc.GetLocalMemories(); err == nil || !strings.Contains(err.Error(), "database") {
		t.Fatalf("GetLocalMemories nil DB error = %v, want database error", err)
	}
	if _, err := svc.AddLocalMemory("hello"); err == nil || !strings.Contains(err.Error(), "database") {
		t.Fatalf("AddLocalMemory nil DB error = %v, want database error", err)
	}
	if err := svc.DeleteLocalMemory(1); err == nil || !strings.Contains(err.Error(), "database") {
		t.Fatalf("DeleteLocalMemory nil DB error = %v, want database error", err)
	}
}

func TestServiceCRUDAndValidation(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatalf("database.Open failed: %v", err)
	}
	defer func() {
		_ = db.Close()
	}()

	svc := New(Deps{
		DB: func() *database.DB { return db },
	})

	initial, err := svc.GetLocalMemories()
	if err != nil {
		t.Fatalf("initial GetLocalMemories failed: %v", err)
	}
	if len(initial) != 0 {
		t.Fatalf("expected 0 initial memories, got %d", len(initial))
	}

	// Empty or whitespace-only content must be rejected.
	for _, blank := range []string{"", "   ", "\t\n"} {
		if _, err := svc.AddLocalMemory(blank); err == nil {
			t.Fatalf("AddLocalMemory(%q) succeeded, expected validation error", blank)
		}
	}

	n1, err := svc.AddLocalMemory("servidor usa proxy corporativo")
	if err != nil {
		t.Fatalf("AddLocalMemory 1 failed: %v", err)
	}
	if n1.ID <= 0 || n1.Content != "servidor usa proxy corporativo" {
		t.Fatalf("unexpected note 1: %+v", n1)
	}

	n2, err := svc.AddLocalMemory("janela de manutencao domingo 02h")
	if err != nil {
		t.Fatalf("AddLocalMemory 2 failed: %v", err)
	}
	if n2.ID == n1.ID {
		t.Fatalf("expected distinct note IDs, both %d", n1.ID)
	}

	all, err := svc.GetLocalMemories()
	if err != nil {
		t.Fatalf("GetLocalMemories failed: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 memories, got %d", len(all))
	}

	if err := svc.DeleteLocalMemory(n1.ID); err != nil {
		t.Fatalf("DeleteLocalMemory(%d) failed: %v", n1.ID, err)
	}

	remaining, err := svc.GetLocalMemories()
	if err != nil {
		t.Fatalf("GetLocalMemories after delete failed: %v", err)
	}
	if len(remaining) != 1 || remaining[0].ID != n2.ID {
		t.Fatalf("expected only note %d remaining, got %+v", n2.ID, remaining)
	}
}
