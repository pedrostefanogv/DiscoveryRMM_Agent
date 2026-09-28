package p2p

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newRetentionCoordinator(dir string) *Coordinator {
	return &Coordinator{
		deps:            &mockDeps{tempDir: dir},
		sha256Cache:     map[string]artifactSHA256CacheEntry{},
		manifestHealth:  map[string]manifestHealthEntry{},
		servingSessions: map[string]*servingSession{},
	}
}

// O Chrome (520 MB) fica no P2P_Temp mesmo com o pacote instalado: a retenção
// por estado deve removê-lo, mantendo o artifact de pacote ainda pendente.
func TestPruneFinalStateArtifacts_RemovesOnlyFinalState(t *testing.T) {
	dir := t.TempDir()
	brave := "winget-bravebrave.exe"
	chrome := "winget-googlechromeexe.exe"
	for _, name := range []string{brave, chrome} {
		if err := writeTestFile(filepath.Join(dir, name), 2048); err != nil {
			t.Fatal(err)
		}
	}
	if err := saveArtifactMeta(dir, brave, "winget:bravebrave"); err != nil {
		t.Fatal(err)
	}
	if err := saveArtifactMeta(dir, chrome, "winget:googlechromeexe"); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { SetArtifactRetentionChecker(nil) })
	SetArtifactRetentionChecker(func(artifactID, artifactName string) bool {
		// brave instalado (remover); chrome com update pendente (manter).
		return strings.EqualFold(strings.TrimSpace(artifactID), "winget:bravebrave")
	})

	c := newRetentionCoordinator(dir)
	removed, err := c.PruneFinalStateArtifacts(time.Now())
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if removed != 1 {
		t.Fatalf("esperava 1 artifact removido, got %d", removed)
	}
	if _, err := os.Stat(filepath.Join(dir, brave)); !os.IsNotExist(err) {
		t.Fatalf("brave deveria ter sido removido do P2P_Temp")
	}
	if _, err := os.Stat(filepath.Join(dir, chrome)); err != nil {
		t.Fatalf("chrome (update pendente) deveria permanecer: %v", err)
	}
}

func TestPruneFinalStateArtifacts_NoHookDoesNothing(t *testing.T) {
	dir := t.TempDir()
	name := "winget-bravebrave.exe"
	if err := writeTestFile(filepath.Join(dir, name), 1024); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { SetArtifactRetentionChecker(nil) })
	SetArtifactRetentionChecker(nil)

	c := newRetentionCoordinator(dir)
	removed, err := c.PruneFinalStateArtifacts(time.Now())
	if err != nil || removed != 0 {
		t.Fatalf("sem hook nada deve ser removido (removed=%d err=%v)", removed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		t.Fatalf("artifact não deveria ser removido: %v", err)
	}
}

// Não remove artifact com transferência em andamento (peer baixando agora).
func TestPruneFinalStateArtifacts_SkipsBeingServed(t *testing.T) {
	dir := t.TempDir()
	name := "winget-bravebrave.exe"
	if err := writeTestFile(filepath.Join(dir, name), 1024); err != nil {
		t.Fatal(err)
	}
	if err := saveArtifactMeta(dir, name, "winget:bravebrave"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { SetArtifactRetentionChecker(nil) })
	SetArtifactRetentionChecker(func(string, string) bool { return true })

	c := newRetentionCoordinator(dir)
	c.servingSessions[name+"|peer-1"] = &servingSession{}

	removed, err := c.PruneFinalStateArtifacts(time.Now())
	if err != nil || removed != 0 {
		t.Fatalf("artifact em uso não deve ser removido (removed=%d err=%v)", removed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		t.Fatalf("artifact em uso foi removido: %v", err)
	}
}
