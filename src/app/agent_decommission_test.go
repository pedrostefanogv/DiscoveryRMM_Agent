package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupAgentDecommissionPathsRemovesDirectories(t *testing.T) {
	root := t.TempDir()
	tempDir := filepath.Join(root, "Discovery")
	p2pDir := filepath.Join(tempDir, "P2P_Temp")

	if err := os.MkdirAll(p2pDir, 0o755); err != nil {
		t.Fatalf("mkdir p2p dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "session.log"), []byte("log"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(p2pDir, "artifact.bin"), []byte("artifact"), 0o600); err != nil {
		t.Fatalf("write p2p file: %v", err)
	}

	if err := cleanupAgentDecommissionPaths([]string{p2pDir, tempDir}); err != nil {
		t.Fatalf("cleanupAgentDecommissionPaths returned error: %v", err)
	}

	if _, err := os.Stat(tempDir); !os.IsNotExist(err) {
		t.Fatalf("expected temp dir to be removed, stat err=%v", err)
	}
}

func TestCleanupAgentDecommissionPathsAcceptsDuplicatesAndMissing(t *testing.T) {
	root := t.TempDir()
	tempDir := filepath.Join(root, "Discovery")

	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		t.Fatalf("mkdir temp dir: %v", err)
	}
	missingDir := filepath.Join(root, "does-not-exist")

	if err := cleanupAgentDecommissionPaths([]string{"", tempDir, tempDir, missingDir}); err != nil {
		t.Fatalf("cleanupAgentDecommissionPaths returned error: %v", err)
	}

	if _, err := os.Stat(tempDir); !os.IsNotExist(err) {
		t.Fatalf("expected temp dir to be removed, stat err=%v", err)
	}
}
