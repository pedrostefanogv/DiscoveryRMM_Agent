package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"discovery/internal/buildinfo"
)

func TestDownloadToTemp_FollowsSignedRedirectAndValidatesManifestSHA(t *testing.T) {
	payload := []byte("signed update payload")
	checksum := sha256.Sum256(payload)
	checksumHex := hex.EncodeToString(checksum[:])

	redirectHeaders := struct {
		authorization string
		agentID       string
		artifactType  string
	}{}

	signedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/signed/update.exe" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer signedServer.Close()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent-auth/me/update/download" {
			http.NotFound(w, r)
			return
		}
		redirectHeaders.authorization = r.Header.Get("Authorization")
		redirectHeaders.agentID = r.Header.Get("X-Agent-ID")
		redirectHeaders.artifactType = r.URL.Query().Get("artifactType")
		w.Header().Set("Location", signedServer.URL+"/signed/update.exe?sig=abc123")
		w.WriteHeader(http.StatusFound)
	}))
	defer apiServer.Close()

	apiURL, err := url.Parse(apiServer.URL)
	if err != nil {
		t.Fatalf("url.Parse(apiServer): %v", err)
	}

	releaseID := "rel-1"
	version := "1.2.3"
	artifactType := "installer"
	updater := &Updater{
		ApiScheme:  apiURL.Scheme,
		ApiServer:  apiURL.Host,
		GetToken:   func() string { return "token-123" },
		GetAgentID: func() string { return "agent-123" },
		TempDir:    t.TempDir(),
	}

	path, err := updater.downloadToTemp(context.Background(), &UpdateManifest{
		ReleaseID:     &releaseID,
		LatestVersion: &version,
		ArtifactType:  artifactType,
		Sha256:        &checksumHex,
	})
	if err != nil {
		t.Fatalf("downloadToTemp: %v", err)
	}
	defer os.Remove(path)

	if redirectHeaders.authorization != "Bearer token-123" {
		t.Fatalf("Authorization = %q", redirectHeaders.authorization)
	}
	if redirectHeaders.agentID != "agent-123" {
		t.Fatalf("X-Agent-ID = %q", redirectHeaders.agentID)
	}
	if redirectHeaders.artifactType != "Installer" {
		t.Fatalf("artifactType = %q, want %q", redirectHeaders.artifactType, "Installer")
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

func TestDownloadToTemp_UsesPreferredArtifactTypeFromPolicy(t *testing.T) {
	payload := []byte("signed update payload")
	checksum := sha256.Sum256(payload)
	checksumHex := hex.EncodeToString(checksum[:])

	artifactType := ""
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent-auth/me/update/download" {
			http.NotFound(w, r)
			return
		}
		artifactType = r.URL.Query().Get("artifactType")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer apiServer.Close()

	apiURL, err := url.Parse(apiServer.URL)
	if err != nil {
		t.Fatalf("url.Parse(apiServer): %v", err)
	}

	releaseID := "rel-2"
	version := "1.2.4"
	updater := &Updater{
		ApiScheme:  apiURL.Scheme,
		ApiServer:  apiURL.Host,
		GetToken:   func() string { return "token-123" },
		GetAgentID: func() string { return "agent-123" },
		GetPolicy: func() Policy {
			return Policy{CheckEveryHours: 6, PreferredArtifactType: "PortableZip"}
		},
		TempDir: t.TempDir(),
	}

	path, err := updater.downloadToTemp(context.Background(), &UpdateManifest{
		ReleaseID:     &releaseID,
		LatestVersion: &version,
		ArtifactType:  "Installer",
		Sha256:        &checksumHex,
	})
	if err != nil {
		t.Fatalf("downloadToTemp: %v", err)
	}
	defer os.Remove(path)

	if artifactType != "PortableZip" {
		t.Fatalf("artifactType = %q, want PortableZip", artifactType)
	}
}

func TestResumePendingInstallReport_ReportsSuccessAndClearsState(t *testing.T) {
	events := make([]reportPayload, 0, 1)
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent-auth/me/update/report" {
			http.NotFound(w, r)
			return
		}
		defer r.Body.Close()
		var payload reportPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode report payload: %v", err)
		}
		events = append(events, payload)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer apiServer.Close()

	apiURL, err := url.Parse(apiServer.URL)
	if err != nil {
		t.Fatalf("url.Parse(apiServer): %v", err)
	}

	previousVersion := buildinfo.Version
	buildinfo.Version = "1.2.5"
	defer func() {
		buildinfo.Version = previousVersion
	}()

	releaseID := "rel-3"
	tempDir := t.TempDir()
	updater := &Updater{
		ApiScheme:  apiURL.Scheme,
		ApiServer:  apiURL.Host,
		GetToken:   func() string { return "token-123" },
		GetAgentID: func() string { return "agent-123" },
		TempDir:    tempDir,
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

	if len(events) != 1 {
		t.Fatalf("expected exactly one report event, got %d", len(events))
	}
	if events[0].EventType != "InstallSucceeded" {
		t.Fatalf("eventType = %q, want InstallSucceeded", events[0].EventType)
	}
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
