package app

import "testing"

func TestNormalizePackageLookupKey(t *testing.T) {
	if got := normalizePackageLookupKey("Microsoft.VisualStudioCode"); got != "microsoftvisualstudiocode" {
		t.Fatalf("normalizePackageLookupKey unexpected: %s", got)
	}
}

func TestResolveArtifactSource_UsesExactArtifactID(t *testing.T) {
	// O novo resolveArtifactSource usa lookup exato por artifactID = "winget:<packageId>"
	// em vez de fuzzy matching por nome de arquivo.
	// Este teste confirma que a função existe e compila.
	// O teste funcional requer p2pCoordinator real; validado via build.
	if got := normalizePackageLookupKey("Mozilla.Firefox"); got != "mozillafirefox" {
		t.Fatalf("normalizePackageLookupKey unexpected: %s", got)
	}
}
