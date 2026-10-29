package windowsupdate

import "testing"

func TestPolicyWriteAllowed(t *testing.T) {
	zero := uint32(0)
	one := uint32(1)

	if !PolicyWriteAllowed(nil, false) {
		t.Fatal("valor ausente: pode escrever")
	}
	if !PolicyWriteAllowed(&one, true) {
		t.Fatal("valor com sentinela nossa: pode reescrever")
	}
	if PolicyWriteAllowed(&zero, false) {
		t.Fatal("valor pre-existente sem sentinela: GPO do admin, nao escrever")
	}
}

func TestPlanPolicyRevert(t *testing.T) {
	// Nos escrevemos, chave ja existia: remove valores, preserva a chave.
	plan := PlanPolicyRevert(true, true, false, 3, 0)
	if len(plan.DeleteValues) != 2 || plan.DeleteKey {
		t.Fatalf("plano inesperado: %+v", plan)
	}

	// Criamos a chave e ela ficou vazia: remove a chave tambem.
	plan = PlanPolicyRevert(true, true, true, 0, 0)
	if !plan.DeleteKey {
		t.Fatalf("chave criada por nos e vazia deveria ser removida: %+v", plan)
	}

	// Chave tem outros valores (admin): nunca remove.
	plan = PlanPolicyRevert(true, true, true, 2, 0)
	if plan.DeleteKey {
		t.Fatalf("chave com valores alheios nao pode ser removida: %+v", plan)
	}

	// Nao escrevemos nada: revert nao toca em nada.
	plan = PlanPolicyRevert(true, false, false, 1, 0)
	if len(plan.DeleteValues) != 0 || plan.DeleteKey {
		t.Fatalf("sem sentinela o revert nao age: %+v", plan)
	}
}
