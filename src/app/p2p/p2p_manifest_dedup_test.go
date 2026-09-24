package p2p

import (
	"context"
	"path/filepath"
	"testing"
)

// Regressão: o handler /artifact/manifest reconstruía o manifest lendo o
// arquivo inteiro sem dedup (generateManifestEager era código morto), então N
// peers pedindo o mesmo manifest disparavam N leituras completas. O
// ensureServingManifest deduplica e reusa o cache.
func TestEnsureServingManifestCachesAndReuses(t *testing.T) {
	dir := t.TempDir()
	name := "artifact.bin"
	path := filepath.Join(dir, name)
	if err := writeTestFile(path, 4096); err != nil {
		t.Fatal(err)
	}

	c := &Coordinator{
		deps:           &mockDeps{tempDir: dir},
		transferServer: &TransferServer{tempDir: dir},
	}

	m1, err := c.ensureServingManifest(context.Background(), path, name, defaultChunkSizeBytes, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m1.TotalChunks != 1 || m1.TotalSize != 4096 {
		t.Fatalf("manifest inesperado: chunks=%d size=%d", m1.TotalChunks, m1.TotalSize)
	}

	// Segunda chamada deve vir do cache (mesmo SHA256, sem erro).
	m2, err := c.ensureServingManifest(context.Background(), path, name, defaultChunkSizeBytes, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m1.SHA256 != m2.SHA256 {
		t.Fatalf("manifest divergente entre chamadas: %s != %s", m1.SHA256, m2.SHA256)
	}
}
