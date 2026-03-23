package app

import "testing"

func TestNormalizePackageLookupKey(t *testing.T) {
	if got := normalizePackageLookupKey("Microsoft.VisualStudioCode"); got != "microsoftvisualstudiocode" {
		t.Fatalf("normalizePackageLookupKey unexpected: %s", got)
	}
}

func TestFindBestArtifactForPackage(t *testing.T) {
	artifacts := []P2PArtifactView{
		{ArtifactName: "random-installer.exe"},
		{ArtifactName: "Microsoft.VisualStudioCode-x64.exe"},
		{ArtifactName: "Microsoft.VisualStudioCode.msi"},
	}
	best := findBestArtifactForPackage(artifacts, "Microsoft.VisualStudioCode")
	if best == "" {
		t.Fatal("expected a best artifact")
	}
}
