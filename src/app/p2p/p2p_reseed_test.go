package p2p

import (
	"path/filepath"
	"strings"
	"testing"
)

// Regressão do loop de re-seed: um artifact recebido via download P2P ficava sem
// o sidecar .meta, então ListArtifacts derivava "name:<arquivo>" enquanto os
// peers anunciavam "winget:<pkg>". Sem o casamento por nome, o re-seed
// considerava o arquivo ausente para sempre e re-baixava indefinidamente.
func TestArtifactPresentLocallyMatchesDivergentIDByName(t *testing.T) {
	dir := t.TempDir()
	name := "winget-foxitfoxitreader.exe"
	if err := writeTestFile(filepath.Join(dir, name), 4096); err != nil {
		t.Fatal(err)
	}

	c := &Coordinator{
		deps:        &mockDeps{tempDir: dir},
		sha256Cache: make(map[string]artifactSHA256CacheEntry),
	}

	if !c.artifactPresentLocally("winget:foxitfoxitreader", name) {
		t.Fatal("esperava o artifact presente por nome mesmo com ID divergente")
	}
	if !c.artifactPresentLocally("name:"+name, name) {
		t.Fatal("esperava o artifact presente pelo ID derivado do nome")
	}
	if c.artifactPresentLocally("winget:inexistente", "outro.exe") {
		t.Fatal("não esperava artifact presente para ID/nome inexistentes")
	}
}

// recordArtifactIdentity deve fazer ListArtifacts devolver o ID lógico anunciado
// pela rede, em vez do ID derivado do nome do arquivo.
func TestRecordArtifactIdentityPersistsMeta(t *testing.T) {
	dir := t.TempDir()
	name := "winget-foxitfoxitreader.exe"
	if err := writeTestFile(filepath.Join(dir, name), 2048); err != nil {
		t.Fatal(err)
	}

	c := &Coordinator{
		deps:        &mockDeps{tempDir: dir},
		sha256Cache: make(map[string]artifactSHA256CacheEntry),
	}
	c.recordArtifactIdentity(name, "winget:foxitfoxitreader")

	arts, err := c.ListArtifacts()
	if err != nil {
		t.Fatal(err)
	}
	if len(arts) != 1 {
		t.Fatalf("esperava 1 artifact, obteve %d", len(arts))
	}
	if !strings.EqualFold(arts[0].ArtifactID, "winget:foxitfoxitreader") {
		t.Fatalf("esperava artifactID=winget:foxitfoxitreader, obteve %q", arts[0].ArtifactID)
	}
}

// ListArtifacts não deve anunciar montagens parciais nem sidecars de metadata:
// o handler de download já os rejeita, então anuncia-los só gerava tentativas
// de download fadadas a falhar (e re-seed sobre arquivos incompletos).
func TestListArtifactsIgnoresPartialAndMeta(t *testing.T) {
	dir := t.TempDir()
	target := "winget-googlechromeexe.exe"
	if err := writeTestFile(filepath.Join(dir, target), 2048); err != nil {
		t.Fatal(err)
	}
	if err := writeTestFile(filepath.Join(dir, target+".partial"), 1024); err != nil {
		t.Fatal(err)
	}
	if err := writeTestFile(filepath.Join(dir, target+".meta"), 16); err != nil {
		t.Fatal(err)
	}
	if err := writeTestFile(filepath.Join(dir, target+".meta.json"), 16); err != nil {
		t.Fatal(err)
	}

	c := &Coordinator{
		deps:        &mockDeps{tempDir: dir},
		sha256Cache: make(map[string]artifactSHA256CacheEntry),
	}
	arts, err := c.ListArtifacts()
	if err != nil {
		t.Fatal(err)
	}
	if len(arts) != 1 || !strings.EqualFold(arts[0].ArtifactName, target) {
		t.Fatalf("esperava apenas %q, obteve %+v", target, arts)
	}
}
