//go:build windows

package app

import "testing"

func TestRespawnBudgetStore(t *testing.T) {
	var s respawnBudgetStore
	s.byID = map[string]int{}

	s.reset("sess-1", 2)
	if !s.consume("sess-1") {
		t.Fatalf("1o consume deveria ser true")
	}
	if !s.consume("sess-1") {
		t.Fatalf("2o consume deveria ser true")
	}
	if s.consume("sess-1") {
		t.Fatalf("3o consume deveria ser false (esgotado)")
	}

	// Reset rearma o orcamento.
	s.reset("sess-1", 1)
	if !s.consume("sess-1") {
		t.Fatalf("consume apos reset deveria ser true")
	}

	// Sessao desconhecida nao tem orcamento.
	if s.consume("outra") {
		t.Fatalf("sessao desconhecida nao deveria ter orcamento")
	}
}
