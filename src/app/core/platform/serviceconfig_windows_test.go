//go:build windows

package platform

import "testing"

func TestPreshutdownTimeoutFor(t *testing.T) {
	if got := PreshutdownTimeoutFor(0); got != 0 {
		t.Fatalf("grace 0 deveria desativar preshutdown, got %d", got)
	}
	if got := PreshutdownTimeoutFor(-1); got != 0 {
		t.Fatalf("grace negativo deveria ser 0, got %d", got)
	}
	// grace + margem de 5s.
	if got := PreshutdownTimeoutFor(30); got != 35000 {
		t.Fatalf("grace 30 deveria ser 35000ms, got %d", got)
	}
	// grace maximo (120s) + margem = 125000ms (abaixo do teto de 180s).
	if got := PreshutdownTimeoutFor(120); got != 125000 {
		t.Fatalf("grace 120 deveria ser 125000ms, got %d", got)
	}
	// Acima do teto o valor e clampado a 180s.
	if got := PreshutdownTimeoutFor(200); got != 180000 {
		t.Fatalf("grace 200 deveria clampar a 180000ms, got %d", got)
	}
}

// Regressão (A.4.1): o deadline do SCM tem de cobrir TODO o grace aceito pelo
// serviço (1..120s) com a margem de 5s, senão o SCM encerra o processo no meio
// do grace que o handler prometeu. Valores acima do teto são clampados a 180s.
func TestPreshutdownTimeoutFor_CoversAcceptableGrace(t *testing.T) {
	for grace := 1; grace <= 120; grace++ {
		want := uint32(grace*1000 + 5000)
		if got := PreshutdownTimeoutFor(grace); got != want {
			t.Fatalf("grace %d: PreshutdownTimeoutFor = %dms, esperado %dms (grace + 5s)", grace, got, want)
		}
		if PreshutdownTimeoutFor(grace) <= uint32(grace*1000) {
			t.Fatalf("grace %d: deadline do SCM não pode ser <= grace", grace)
		}
	}
}
