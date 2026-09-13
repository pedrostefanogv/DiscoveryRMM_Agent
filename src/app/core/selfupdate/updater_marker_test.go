package selfupdate

import (
	"testing"
)

// TestSameBuildInstalled_Contract valida o contrato do dono para o update:
// versão diferente → update; versão igual + commit diferente → update;
// ambos iguais → skip; commits incomparáveis com versão igual → skip
// (proteção anti-loop).
func TestSameBuildInstalled_Contract(t *testing.T) {
	cases := []struct {
		name          string
		srvVer        string
		srvCommit     string
		effVer        string
		effCommit     string
		wantSameBuild bool
	}{
		{name: "ambos iguais -> skip", srvVer: "1.2.1", srvCommit: "a6ddadd4", effVer: "1.2.1", effCommit: "a6ddadd4", wantSameBuild: true},
		{name: "versao diferente -> update", srvVer: "1.2.2", srvCommit: "a6ddadd4", effVer: "1.2.1", effCommit: "a6ddadd4", wantSameBuild: false},
		{name: "versao igual + commit diferente -> update (rebuild)", srvVer: "1.2.1", srvCommit: "b7eebbf5", effVer: "1.2.1", effCommit: "a6ddadd4", wantSameBuild: false},
		{name: "buildinfo 0.0.0 + marker 1.2.1 -> skip (anti-loop)", srvVer: "1.2.1", srvCommit: "a6ddadd4", effVer: "1.2.1", effCommit: "unknown", wantSameBuild: true},
		{name: "servidor sem commit + versao igual -> skip", srvVer: "1.2.1", srvCommit: "", effVer: "1.2.1", effCommit: "a6ddadd4", wantSameBuild: true},
		{name: "eff commit unknown + versao igual -> skip (anti-loop)", srvVer: "1.2.1", srvCommit: "a6ddadd4", effVer: "1.2.1", effCommit: "", wantSameBuild: true},
	}
	for _, c := range cases {
		if got := sameBuildInstalled(c.srvVer, c.srvCommit, c.effVer, c.effCommit); got != c.wantSameBuild {
			t.Fatalf("%s: sameBuildInstalled = %v, want %v", c.name, got, c.wantSameBuild)
		}
	}
}

// TestEffectiveCurrent_MarkerOverridesZeroBuildinfo valida que o buildinfo
// "0.0.0"/"unknown" é substituído pelo marker do último install confirmado.
func TestEffectiveCurrent_MarkerOverridesZeroBuildinfo(t *testing.T) {
	u := &Updater{TempDir: t.TempDir()}

	// Sem marker: retorna o buildinfo como veio.
	v, c := u.effectiveCurrent("0.0.0", "unknown")
	if v != "0.0.0" || c != "unknown" {
		t.Fatalf("sem marker: eff = %s/%s, want 0.0.0/unknown", v, c)
	}

	// Com marker: o marker vira o efetivo.
	u.saveInstalledMarker("1.2.1", "a6ddadd4")
	v, c = u.effectiveCurrent("0.0.0", "unknown")
	if v != "1.2.1" || c != "a6ddadd4" {
		t.Fatalf("com marker: eff = %s/%s, want 1.2.1/a6ddadd4", v, c)
	}

	// Buildinfo confiável (diferente de 0.0.0) tem precedência sobre o marker.
	v, c = u.effectiveCurrent("1.2.2", "b7eebbf5")
	if v != "1.2.2" || c != "b7eebbf5" {
		t.Fatalf("buildinfo confiavel: eff = %s/%s, want 1.2.2/b7eebbf5", v, c)
	}
}