package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDownloadFromCacheOrPublic_P2PFallbackToLegacyNameID valida o fallback
// duplo de artifactID: primeiro "selfupdate:<sha256>" (canônico, via sidecar
// .meta) e depois "name:selfupdate-<sha256>.exe" (derivado do nome, peers
// antigos sem sidecar).
func TestDownloadFromCacheOrPublic_P2PFallbackToLegacyNameID(t *testing.T) {
	payload := []byte("update payload")
	checksum := sha256.Sum256(payload)
	checksumHex := strings.ToLower(hex.EncodeToString(checksum[:]))

	tempDir := t.TempDir()
	var queriedIDs []string
	updater := &Updater{
		TempDir: tempDir,
		FindPeersByReleaseID: func(ctx context.Context, artifactID string) ([]string, error) {
			queriedIDs = append(queriedIDs, artifactID)
			// Só o ID legado (derivado do nome) tem peers.
			if strings.EqualFold(artifactID, "name:selfupdate-"+checksumHex+".exe") {
				return []string{"peer-legacy"}, nil
			}
			return nil, nil
		},
		DownloadFromPeer: func(ctx context.Context, artifactID, peerID string) (string, error) {
			path := filepath.Join(tempDir, "selfupdate-"+checksumHex+".exe")
			if err := os.WriteFile(path, payload, 0o644); err != nil {
				return "", err
			}
			return path, nil
		},
	}

	path, sha, fromP2P, err := updater.downloadFromCacheOrPublic(context.Background(), checksumHex)
	if err != nil {
		t.Fatalf("downloadFromCacheOrPublic: %v", err)
	}
	defer os.Remove(path)

	if !fromP2P {
		t.Fatalf("fromP2P = false, want true (peer legado)")
	}
	if sha != checksumHex {
		t.Fatalf("sha256 = %q, want %q", sha, checksumHex)
	}

	// Deve ter consultado o ID canônico primeiro e o legado depois.
	if len(queriedIDs) < 2 {
		t.Fatalf("consultas P2P = %v, want >= 2 (canônico + legado)", queriedIDs)
	}
	if queriedIDs[0] != "selfupdate:"+checksumHex {
		t.Fatalf("primeira consulta = %q, want selfupdate:%s", queriedIDs[0], checksumHex)
	}
	if queriedIDs[1] != "name:selfupdate-"+checksumHex+".exe" {
		t.Fatalf("segunda consulta = %q, want name:selfupdate-%s.exe", queriedIDs[1], checksumHex)
	}
}

// TestDownloadFromCacheOrPublic_P2PCanonicalIDFirst valida que o ID canônico
// é consultado primeiro e o download P2P funciona quando um peer o anuncia.
func TestDownloadFromCacheOrPublic_P2PCanonicalIDFirst(t *testing.T) {
	payload := []byte("update payload")
	checksum := sha256.Sum256(payload)
	checksumHex := strings.ToLower(hex.EncodeToString(checksum[:]))

	tempDir := t.TempDir()
	var queriedIDs []string
	updater := &Updater{
		TempDir: tempDir,
		FindPeersByReleaseID: func(ctx context.Context, artifactID string) ([]string, error) {
			queriedIDs = append(queriedIDs, artifactID)
			if strings.EqualFold(artifactID, "selfupdate:"+checksumHex) {
				return []string{"peer-canonical"}, nil
			}
			return nil, nil
		},
		DownloadFromPeer: func(ctx context.Context, artifactID, peerID string) (string, error) {
			path := filepath.Join(tempDir, "selfupdate-"+checksumHex+".exe")
			if err := os.WriteFile(path, payload, 0o644); err != nil {
				return "", err
			}
			return path, nil
		},
	}

	path, sha, fromP2P, err := updater.downloadFromCacheOrPublic(context.Background(), checksumHex)
	if err != nil {
		t.Fatalf("downloadFromCacheOrPublic: %v", err)
	}
	defer os.Remove(path)

	if !fromP2P {
		t.Fatalf("fromP2P = false, want true")
	}
	if sha != checksumHex {
		t.Fatalf("sha256 = %q, want %q", sha, checksumHex)
	}
	if len(queriedIDs) != 1 || queriedIDs[0] != "selfupdate:"+checksumHex {
		t.Fatalf("consultas = %v, want apenas [selfupdate:%s] (peer canônico encontrado, sem fallback)", queriedIDs, checksumHex)
	}
}

// TestDownloadFromCacheOrPublic_P2PStalePeerRejected valida que um peer com
// SHA256 divergente (artifact stale) é rejeitado e o fluxo cai para HTTP.
func TestDownloadFromCacheOrPublic_P2PStalePeerRejected(t *testing.T) {
	payload := []byte("update payload")
	checksum := sha256.Sum256(payload)
	checksumHex := strings.ToLower(hex.EncodeToString(checksum[:]))

	stalePayload := []byte("STALE old version")
	staleChecksum := sha256.Sum256(stalePayload)
	staleHex := strings.ToLower(hex.EncodeToString(staleChecksum[:]))

	tempDir := t.TempDir()
	updater := &Updater{
		TempDir: tempDir,
		FindPeersByReleaseID: func(ctx context.Context, artifactID string) ([]string, error) {
			return []string{"peer-stale"}, nil
		},
		DownloadFromPeer: func(ctx context.Context, artifactID, peerID string) (string, error) {
			path := filepath.Join(tempDir, "selfupdate-"+staleHex+".exe")
			if err := os.WriteFile(path, stalePayload, 0o644); err != nil {
				return "", err
			}
			return path, nil
		},
	}

	// Sem endpoint HTTP configurado (ApiServer vazio) — se o peer stale fosse
	// aceito, retornaria fromP2P=true; rejeitado, cai no HTTP e falha.
	_, _, fromP2P, err := updater.downloadFromCacheOrPublic(context.Background(), checksumHex)
	if err == nil {
		t.Fatalf("esperava erro (HTTP indisponível após rejeitar peer stale)")
	}
	if fromP2P {
		t.Fatalf("fromP2P = true, want false (peer stale deve ser rejeitado)")
	}
}
