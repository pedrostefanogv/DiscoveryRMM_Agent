package p2p

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

func TestLoadOrCreateLibp2PIdentity_RoundTrip(t *testing.T) {
	origHarden := hardenIdentityFileACL
	hardenIdentityFileACL = func(string) error { return nil }
	t.Cleanup(func() { hardenIdentityFileACL = origHarden })

	tmpDir := t.TempDir()

	priv1, err := loadOrCreateLibp2PIdentity(tmpDir)
	if err != nil {
		t.Fatalf("first loadOrCreateLibp2PIdentity failed: %v", err)
	}
	id1, err := peer.IDFromPrivateKey(priv1)
	if err != nil {
		t.Fatalf("IDFromPrivateKey(priv1) failed: %v", err)
	}

	priv2, err := loadOrCreateLibp2PIdentity(tmpDir)
	if err != nil {
		t.Fatalf("second loadOrCreateLibp2PIdentity failed: %v", err)
	}
	id2, err := peer.IDFromPrivateKey(priv2)
	if err != nil {
		t.Fatalf("IDFromPrivateKey(priv2) failed: %v", err)
	}

	if id1 != id2 {
		t.Fatalf("expected persistent peer.ID across restarts, got %s then %s", id1, id2)
	}
}

func TestLoadOrCreateLibp2PIdentity_CorruptKeyRegenerates(t *testing.T) {
	origHarden := hardenIdentityFileACL
	hardenIdentityFileACL = func(string) error { return nil }
	t.Cleanup(func() { hardenIdentityFileACL = origHarden })

	tmpDir := t.TempDir()
	keyDir := filepath.Join(tmpDir, p2pIdentityDirName)
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	keyPath := filepath.Join(keyDir, p2pIdentityFileName)
	if err := os.WriteFile(keyPath, []byte("not-a-valid-protobuf-key"), 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	priv1, err := loadOrCreateLibp2PIdentity(tmpDir)
	if err != nil {
		t.Fatalf("loadOrCreateLibp2PIdentity after corruption failed: %v", err)
	}
	id1, err := peer.IDFromPrivateKey(priv1)
	if err != nil {
		t.Fatalf("IDFromPrivateKey(priv1) failed: %v", err)
	}

	priv2, err := loadOrCreateLibp2PIdentity(tmpDir)
	if err != nil {
		t.Fatalf("subsequent loadOrCreateLibp2PIdentity failed: %v", err)
	}
	id2, err := peer.IDFromPrivateKey(priv2)
	if err != nil {
		t.Fatalf("IDFromPrivateKey(priv2) failed: %v", err)
	}

	if id1 != id2 {
		t.Fatalf("expected regenerated key to persist, got %s then %s", id1, id2)
	}
}
