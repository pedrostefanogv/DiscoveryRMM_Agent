package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestP2PTokenValidation(t *testing.T) {
	srv := &p2pTransferServer{secret: []byte("01234567890123456789012345678901")}
	token, err := srv.issueTokenLocked("artifact.bin", "peer-a", time.Now().Add(2*time.Minute))
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if err := srv.verifyToken("artifact.bin", "peer-a", token, time.Now()); err != nil {
		t.Fatalf("verify token should succeed: %v", err)
	}
	if err := srv.verifyToken("other.bin", "peer-a", token, time.Now()); err == nil {
		t.Fatal("verify token should fail for different artifact")
	}
	if err := srv.verifyToken("artifact.bin", "peer-b", token, time.Now()); err == nil {
		t.Fatal("verify token should fail for different peer")
	}
}

func TestSanitizeArtifactName(t *testing.T) {
	if got := sanitizeArtifactName("../danger.txt"); got != "" {
		t.Fatalf("expected empty for traversal, got %q", got)
	}
	if got := sanitizeArtifactName("folder/file.txt"); got != "" {
		t.Fatalf("expected empty for path separator, got %q", got)
	}
	if got := sanitizeArtifactName("artifact.bin"); got != "artifact.bin" {
		t.Fatalf("expected artifact.bin, got %q", got)
	}
}

func TestComputeFileSHA256(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	hash, err := computeFileSHA256(path)
	if err != nil {
		t.Fatalf("compute hash: %v", err)
	}
	if hash != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("unexpected sha256: %s", hash)
	}
}

func TestVerifyReplicationControl(t *testing.T) {
	srv := &p2pTransferServer{sharedSecret: []byte("shared-secret")}
	access := P2PArtifactAccess{
		ArtifactName:   "artifact.bin",
		ChecksumSHA256: "abc123",
	}
	ts := time.Now().UTC().Unix()
	timestampValue := strconv.FormatInt(ts, 10)
	signature := signReplicationControl(srv.sharedSecret, "agent-source", access, timestampValue)
	req, err := http.NewRequest(http.MethodPost, "http://peer/p2p/replicate", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set(p2pControlHeaderSourceAgent, "agent-source")
	req.Header.Set(p2pControlHeaderTimestamp, timestampValue)
	req.Header.Set(p2pControlHeaderSignature, signature)
	if err := srv.verifyReplicationControl(req, access); err != nil {
		t.Fatalf("expected control verification success, got %v", err)
	}
	req.Header.Set(p2pControlHeaderSignature, "bad-signature")
	if err := srv.verifyReplicationControl(req, access); err == nil {
		t.Fatal("expected control verification failure for invalid signature")
	}
}
