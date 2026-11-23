//go:build windows

package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"discovery/app/core/testutil"
	debug "discovery/app/debug"
	"discovery/app/logs"
)

func TestWorkerConfigFailClosedWithoutStdinAuthToken(t *testing.T) {
	// Even if a disk config.json/debug_config.json exists in ProgramData,
	// worker MUST NOT read authToken from disk.
	pd := testutil.TempDir(t)
	t.Setenv("ProgramData", pd)
	discDir := filepath.Join(pd, "Discovery")
	if err := os.MkdirAll(discDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(discDir, "config.json"), []byte(`{"authToken":"disk-token-forbidden","clientId":"c1","siteId":"s1"}`), 0o600); err != nil {
		t.Fatalf("WriteFile config.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(discDir, "debug_config.json"), []byte(`{"apiServer":"api.example.local","authToken":"disk-debug-token","agentId":"8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7"}`), 0o600); err != nil {
		t.Fatalf("WriteFile debug_config.json: %v", err)
	}

	if got := GetInstallerAuthTokenForWorker(); got != "" {
		t.Fatalf("GetInstallerAuthTokenForWorker must never read token from disk, got %q", got)
	}
	if got := GetDebugConfigForWorker().AuthToken; got != "" {
		t.Fatalf("GetDebugConfigForWorker must never return AuthToken from disk, got %q", got)
	}
	if got := stringAuthTokenFromPayload(map[string]any{"sessionId": "s-1"}); got != "" {
		t.Fatalf("expected empty token when stdin payload omits authToken, got %q", got)
	}

	// spawnRemoteSessionWorkerAttempt must fail closed before spawning if authToken or agentId missing
	if err := spawnRemoteSessionWorkerAttempt(context.Background(), map[string]any{
		"sessionId": "s-1",
		"agentId":   "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7",
	}, false); err == nil {
		t.Fatalf("expected spawnRemoteSessionWorkerAttempt to fail closed without stdin authToken")
	}
	if err := spawnRemoteSessionWorkerAttempt(context.Background(), map[string]any{
		"sessionId": "s-1",
		"authToken": "stdin-tok",
	}, false); err == nil {
		t.Fatalf("expected spawnRemoteSessionWorkerAttempt to fail closed without stdin agentId")
	}
}

func TestInjectWorkerRuntimeHintsAndMerge(t *testing.T) {
	a := &App{ctx: context.Background()}
	a.Logs.Buffer = logs.New()
	cfgPath := filepath.Join(testutil.TempDir(t), "debug_config.json")
	a.DebugSvc = debug.NewService(debug.Options{
		ConfigPaths:         func() []string { return []string{cfgPath} },
		WatcherInterval:     time.Hour,
		HardenFileACL:       func(string) error { return nil },
		ValidateFileACL:     func(string) error { return nil },
		ValidateLoadFileACL: func(string) error { return nil },
	})
	err := a.DebugSvc.SetConfig(debug.Config{
		ApiScheme:                "https",
		ApiServer:                "api.example.local",
		NatsServer:               "nats://api.example.local:4222",
		NatsWsServer:             "wss://api.example.local/nats/",
		AuthToken:                "live-stdin-token",
		AgentID:                  "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7",
		AllowInsecureTLS:         true,
		EnforceTlsHashValidation: true,
		HandshakeEnabled:         true,
		NatsTlsCertHash:          "AABBCC",
	})
	if err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	a.AgentConfig.ClientID = "client-1"
	a.AgentConfig.SiteID = "site-1"

	payload := map[string]any{"sessionId": "sess-1"}
	injectWorkerRuntimeHints(a, payload)

	if payload["authToken"] != "live-stdin-token" {
		t.Fatalf("payload authToken = %v", payload["authToken"])
	}
	if payload["agentId"] != "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7" {
		t.Fatalf("payload agentId = %v", payload["agentId"])
	}
	if payload["clientId"] != "client-1" || payload["siteId"] != "site-1" {
		t.Fatalf("payload clientId/siteId = %v/%v", payload["clientId"], payload["siteId"])
	}

	merged := mergeWorkerDebugConfigFromPayload(WorkerDebugConfig{}, payload)
	if merged.AuthToken != "live-stdin-token" ||
		merged.AgentID != "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7" ||
		!merged.AllowInsecureTLS ||
		!merged.EnforceTLSHashValidation ||
		merged.NatsTLSCertHash != "AABBCC" {
		t.Fatalf("merged WorkerDebugConfig mismatch: %+v", merged)
	}

	// remoteConsentEndpoint must not read tokens from disk
	remoteConsentRuntimeMu.Lock()
	origAPI, origTok := remoteConsentAPI, remoteConsentToken
	remoteConsentAPI, remoteConsentToken = "", ""
	remoteConsentRuntimeMu.Unlock()
	t.Cleanup(func() {
		configureRemoteConsentRuntime(origAPI, origTok, "")
	})
	_, consentTok := remoteConsentEndpoint()
	if consentTok != "" {
		t.Fatalf("remoteConsentEndpoint must not fall back to disk token, got %q", consentTok)
	}
	configureRemoteConsentRuntime("https://api.example.local", "injected-consent-tok", "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7")
	apiBase, consentTok := remoteConsentEndpoint()
	if apiBase != "https://api.example.local" || consentTok != "injected-consent-tok" {
		t.Fatalf("remoteConsentEndpoint = (%q, %q)", apiBase, consentTok)
	}
}
