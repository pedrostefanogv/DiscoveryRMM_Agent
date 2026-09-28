package p2p

import (
	"strings"
	"testing"
)

func TestFetchAllowed_DefaultsToAllow(t *testing.T) {
	t.Cleanup(func() { SetArtifactFetchGate(nil) })
	SetArtifactFetchGate(nil)
	if !fetchAllowed("winget:bravebrave", "winget-bravebrave.exe") {
		t.Fatalf("sem gate configurado deve permitir (fail-safe)")
	}
}

// O gate é quem evita baixar instalador de pacote já instalado/atualizado.
func TestFetchAllowed_CustomGate(t *testing.T) {
	t.Cleanup(func() { SetArtifactFetchGate(nil) })
	SetArtifactFetchGate(func(artifactID, artifactName string) bool {
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(artifactID)), "selfupdate:")
	})
	if fetchAllowed("winget:bravebrave", "winget-bravebrave.exe") {
		t.Fatalf("gate bloqueando deveria impedir o fetch")
	}
	if !fetchAllowed("selfupdate:9bd8a5c3", "selfupdate-9bd8a5c3.exe") {
		t.Fatalf("artifact permitido pelo gate deveria ser baixado")
	}
}

func TestFetchAllowed_GateReceivesNameAndID(t *testing.T) {
	t.Cleanup(func() { SetArtifactFetchGate(nil) })
	var gotID, gotName string
	SetArtifactFetchGate(func(artifactID, artifactName string) bool {
		gotID, gotName = artifactID, artifactName
		return true
	})
	fetchAllowed("winget:osqueryosquery", "winget-osqueryosquery.msi")
	if gotID != "winget:osqueryosquery" || gotName != "winget-osqueryosquery.msi" {
		t.Fatalf("gate recebeu id=%q name=%q", gotID, gotName)
	}
}
