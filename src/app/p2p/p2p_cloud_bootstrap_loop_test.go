package p2p

import (
	"testing"
)

// cloudBootstrapCycle deve re-checkar em 5 min quando o toggle esta desligado
// (barato: so le a config local; nao ha chamada HTTP).
func TestCloudBootstrapCycleDisabledRecheck(t *testing.T) {
	deps := &mockDeps{cfg: DefaultConfig(), tempDir: t.TempDir(), dataDir: t.TempDir()}
	c := NewCoordinator(deps)
	// BootstrapConfig.CloudBootstrapEnabled = false por default
	got := c.cloudBootstrapCycle(p2pCloudBootstrapInterval)
	if got != p2pCloudBootstrapDisabledRecheck {
		t.Fatalf("intervalo com toggle desligado = %v, want %v", got, p2pCloudBootstrapDisabledRecheck)
	}
}

// cloudBootstrapCycle em falha dobra o intervalo ate o teto de 6h.
func TestCloudBootstrapCycleBackoffOnFailure(t *testing.T) {
	deps := &mockDeps{cfg: DefaultConfig(), tempDir: t.TempDir(), dataDir: t.TempDir()}
	deps.cfg.BootstrapConfig.CloudBootstrapEnabled = true
	// debugCfg vazio: RunCloudBootstrap falha deterministica
	// ("configuracao incompleta") sem tocar rede.
	c := NewCoordinator(deps)

	got := c.cloudBootstrapCycle(p2pCloudBootstrapInterval)
	if want := 2 * p2pCloudBootstrapInterval; got != want {
		t.Fatalf("backoff = %v, want %v", got, want)
	}
	got = c.cloudBootstrapCycle(p2pCloudBootstrapBackoffMax)
	if got != p2pCloudBootstrapBackoffMax {
		t.Fatalf("backoff no teto = %v, want %v", got, p2pCloudBootstrapBackoffMax)
	}
}

// jitteredInterval mantem o intervalo dentro de mais/menos 15%.
func TestJitteredIntervalBounds(t *testing.T) {
	base := p2pCloudBootstrapInterval
	for i := 0; i < 200; i++ {
		got := jitteredInterval(base)
		lo := base - base/7
		hi := base + base/7
		if got < lo || got > hi {
			t.Fatalf("jitter fora dos limites: %v (want entre %v e %v)", got, lo, hi)
		}
	}
	if jitteredInterval(0) != 0 {
		t.Fatalf("jitteredInterval(0) != 0")
	}
}
