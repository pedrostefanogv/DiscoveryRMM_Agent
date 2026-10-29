package thirdparty

import "testing"

// Fixture REAL capturada em 2026-10-29 (choco 2.7.4, pt-BR).
func TestParseChocoOutdatedRealFixture(t *testing.T) {
	updates := ParseChocoOutdated(loadTestdata(t, "choco_outdated_ptbr.txt"))

	if len(updates) != 7 {
		t.Fatalf("esperava 7 pacotes reais; got %d", len(updates))
	}
	byID := map[string]AppUpdate{}
	for _, update := range updates {
		byID[update.ID] = update
	}
	choco, ok := byID["chocolatey"]
	if !ok || choco.InstalledVersion != "2.7.4" || choco.AvailableVersion != "2.7.5" {
		t.Fatalf("chocolatey inesperado: %+v", choco)
	}
	if buildtools, ok := byID["visualstudio2022buildtools"]; !ok || buildtools.AvailableVersion != "117.14.42" {
		t.Fatalf("buildtools inesperado: %+v", buildtools)
	}
	for _, update := range updates {
		if update.Source != SourceChoco || update.UpdateKey() != "choco:"+update.ID {
			t.Fatalf("chave/origem inesperada: %+v", update)
		}
	}
}
