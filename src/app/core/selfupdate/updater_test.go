package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"discovery/app/core/buildinfo"
)

func TestDownloadFromURL_DownloadsAndReturnsSHA256(t *testing.T) {
	payload := []byte("update payload")
	checksum := sha256.Sum256(payload)
	checksumHex := hex.EncodeToString(checksum[:])

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/download/agent" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer apiServer.Close()

	apiURL, err := url.Parse(apiServer.URL)
	if err != nil {
		t.Fatalf("url.Parse(apiServer): %v", err)
	}

	updater := &Updater{
		ApiScheme:  apiURL.Scheme,
		ApiServer:  apiURL.Host,
		GetToken:   func() string { return "mdz_token_123" },
		GetAgentID: func() string { return "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7" },
		TempDir:    t.TempDir(),
	}

	downloadURL := apiServer.URL + "/api/v1/download/agent"
	path, sha, err := updater.downloadFromURL(context.Background(), downloadURL)
	if err != nil {
		t.Fatalf("downloadFromURL: %v", err)
	}
	defer os.Remove(path)

	if sha != checksumHex {
		t.Fatalf("sha256 = %q, want %q", sha, checksumHex)
	}
	if filepath.Ext(path) != ".exe" {
		t.Fatalf("download path = %q, want .exe suffix", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%s): %v", path, err)
	}
	if string(data) != string(payload) {
		t.Fatalf("downloaded payload mismatch: got %q want %q", string(data), string(payload))
	}
}

func TestDownloadFromCacheOrPublic_UsesPublicEndpoint(t *testing.T) {
	payload := []byte("update payload")
	checksum := sha256.Sum256(payload)
	checksumHex := hex.EncodeToString(checksum[:])

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/download/agent" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer apiServer.Close()

	apiURL, err := url.Parse(apiServer.URL)
	if err != nil {
		t.Fatalf("url.Parse(apiServer): %v", err)
	}

	updater := &Updater{
		ApiScheme:  apiURL.Scheme,
		ApiServer:  apiURL.Host,
		GetToken:   func() string { return "mdz_token_123" },
		GetAgentID: func() string { return "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7" },
		TempDir:    t.TempDir(),
	}

	path, sha, fromP2P, err := updater.downloadFromCacheOrPublic(context.Background(), "")
	if err != nil {
		t.Fatalf("downloadFromCacheOrPublic: %v", err)
	}
	defer os.Remove(path)

	if fromP2P {
		t.Fatalf("fromP2P = true, want false (HTTP fallback)")
	}

	if sha != checksumHex {
		t.Fatalf("sha256 = %q, want %q", sha, checksumHex)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%s): %v", path, err)
	}
	if string(data) != string(payload) {
		t.Fatalf("downloaded payload mismatch: got %q want %q", string(data), string(payload))
	}
}

func TestResumePendingInstallReport_ClearsStateOnVersionMatch(t *testing.T) {
	previousVersion := buildinfo.Version
	buildinfo.Version = "1.2.5"
	defer func() {
		buildinfo.Version = previousVersion
	}()

	releaseID := "rel-3"
	tempDir := t.TempDir()
	updater := &Updater{
		TempDir: tempDir,
	}
	if err := updater.persistPendingInstallState(pendingInstallState{
		ReleaseID:      &releaseID,
		CurrentVersion: "1.2.4",
		TargetVersion:  "1.2.5",
		CorrelationID:  "corr-1",
		RecordedAtUTC:  "2026-04-19T12:00:00Z",
	}); err != nil {
		t.Fatalf("persistPendingInstallState: %v", err)
	}

	updater.ResumePendingInstallReport(context.Background())

	if _, err := os.Stat(filepath.Join(tempDir, pendingInstallFile)); !os.IsNotExist(err) {
		t.Fatalf("expected pending install file to be removed, stat err=%v", err)
	}
}

func TestCompareVersions_SemverSegments(t *testing.T) {
	if got := compareVersions("1.10.0", "1.2.9"); got <= 0 {
		t.Fatalf("compareVersions should treat 1.10.0 > 1.2.9, got %d", got)
	}
	if got := compareVersions("1.2.0", "1.2.0"); got != 0 {
		t.Fatalf("compareVersions equality = %d", got)
	}
	if got := compareVersions("1.2.0", "1.2.1"); got >= 0 {
		t.Fatalf("compareVersions should treat 1.2.0 < 1.2.1, got %d", got)
	}
}

func TestNormalizeArtifactType_ToleratesInstallerCasing(t *testing.T) {
	if got := normalizeArtifactType(""); got != "Installer" {
		t.Fatalf("normalizeArtifactType(\"\") = %q", got)
	}
	if got := normalizeArtifactType("installer"); got != "Installer" {
		t.Fatalf("normalizeArtifactType(lowercase) = %q", got)
	}
	if got := normalizeArtifactType("Installer"); got != "Installer" {
		t.Fatalf("normalizeArtifactType(canonical) = %q", got)
	}
	if got := normalizeArtifactType("PortableZip"); got != "PortableZip" {
		t.Fatalf("normalizeArtifactType(custom) = %q", got)
	}
}
