package memlimit

import (
	"math"
	"testing"
)

func TestApplyUsesDefaultWhenEnvMissing(t *testing.T) {
	t.Setenv(EnvLimit, "")
	if got := Apply(123); got != 123 {
		t.Fatalf("Apply default = %d, want 123", got)
	}
	if got := Current(); got != int64(123)<<20 {
		t.Fatalf("Current = %d, want %d", got, int64(123)<<20)
	}
}

func TestApplyEnvOverridesDefault(t *testing.T) {
	t.Setenv(EnvLimit, "64")
	if got := Apply(999); got != 64 {
		t.Fatalf("Apply env = %d, want 64", got)
	}
	if got := Current(); got != int64(64)<<20 {
		t.Fatalf("Current = %d, want %d", got, int64(64)<<20)
	}
}

func TestApplyInvalidEnvFallsBackToDefault(t *testing.T) {
	t.Setenv(EnvLimit, "nao-numero")
	if got := Apply(48); got != 48 {
		t.Fatalf("Apply invalid env = %d, want 48", got)
	}
}

func TestApplyZeroDisablesLimit(t *testing.T) {
	t.Setenv(EnvLimit, "0")
	if got := Apply(256); got != 0 {
		t.Fatalf("Apply zero = %d, want 0", got)
	}
	if got := Current(); got != math.MaxInt64 {
		t.Fatalf("Current = %d, want MaxInt64", got)
	}
}

func TestRelaxRespectsInterval(t *testing.T) {
	ResetRelaxState()
	if !Relax() {
		t.Fatal("primeira Relax deveria executar")
	}
	if Relax() {
		t.Fatal("segunda Relax imediata deveria ser ignorada pelo intervalo")
	}
	ResetRelaxState()
}
