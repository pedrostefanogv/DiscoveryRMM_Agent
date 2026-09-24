package p2p

import (
	"path/filepath"
	"testing"
)

// Regressão de R2: os caches sha256Cache/manifestHealth e o mapa fetchStates
// cresciam indefinidamente — arquivos removidos pela limpeza de TTL deixavam
// entradas órfãs, e artifactIDs fora de circulação ficavam para sempre.
func TestPruneStaleCachesRemovesGhostEntries(t *testing.T) {
	dir := t.TempDir()
	name := "winget-foxitfoxitreader.exe"
	if err := writeTestFile(filepath.Join(dir, name), 1024); err != nil {
		t.Fatal(err)
	}
	realPath := filepath.Join(dir, name)
	ghostPath := filepath.Join(dir, "ghost.exe")

	c := &Coordinator{
		deps:           &mockDeps{tempDir: dir},
		sha256Cache:    map[string]artifactSHA256CacheEntry{realPath: {}, ghostPath: {}},
		manifestHealth: map[string]manifestHealthEntry{realPath: {}, ghostPath: {}},
		fetchStates:    newFetchStateMap(),
		peerArtifacts:  map[string]p2pPeerArtifactState{},
	}
	c.fetchStates.states["winget:fantasma"] = &ArtifactFetchState{Status: "available"}
	c.fetchStates.states["name:"+name] = &ArtifactFetchState{Status: "available"}
	c.fetchStates.states["winget:pendente"] = &ArtifactFetchState{Status: "missing"}

	c.pruneStaleCaches()

	if _, ok := c.sha256Cache[ghostPath]; ok {
		t.Fatal("esperava sha256Cache do arquivo fantasma removido")
	}
	if _, ok := c.sha256Cache[realPath]; !ok {
		t.Fatal("não esperava remover sha256Cache do arquivo real")
	}
	if _, ok := c.manifestHealth[ghostPath]; ok {
		t.Fatal("esperava manifestHealth do arquivo fantasma removido")
	}
	if _, ok := c.manifestHealth[realPath]; !ok {
		t.Fatal("não esperava remover manifestHealth do arquivo real")
	}
	if _, ok := c.fetchStates.states["winget:fantasma"]; ok {
		t.Fatal("esperava fetchState available fantasma podado")
	}
	if _, ok := c.fetchStates.states["name:"+name]; !ok {
		t.Fatal("não esperava podar fetchState do artifact presente localmente")
	}
	if _, ok := c.fetchStates.states["winget:pendente"]; !ok {
		t.Fatal("não esperava podar fetchState missing (precisa do backoff)")
	}
}
