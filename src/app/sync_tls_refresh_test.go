package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"discovery/app/debug"
	"discovery/internal/agentconn"
)

type syncTestAgentConn struct {
	reloadCount int
}

func (f *syncTestAgentConn) Reload() {
	f.reloadCount++
}

func (f *syncTestAgentConn) GetStatus() agentconn.Status {
	return agentconn.Status{}
}

func TestRefreshAgentConfiguration_AppliesRemoteSecurityAndReloads(t *testing.T) {
	tempBase := t.TempDir()
	oldProgramData := os.Getenv("ProgramData")
	oldLocalAppData := os.Getenv("LOCALAPPDATA")
	oldHome := os.Getenv("HOME")
	oldUserProfile := os.Getenv("USERPROFILE")
	defer func() {
		_ = os.Setenv("ProgramData", oldProgramData)
		_ = os.Setenv("LOCALAPPDATA", oldLocalAppData)
		_ = os.Setenv("HOME", oldHome)
		_ = os.Setenv("USERPROFILE", oldUserProfile)
	}()
	_ = os.Setenv("ProgramData", tempBase)
	_ = os.Setenv("LOCALAPPDATA", tempBase)
	_ = os.Setenv("HOME", tempBase)
	_ = os.Setenv("USERPROFILE", tempBase)

	payload := map[string]any{
		"enforceTlsHashValidation": true,
		"handshakeEnabled":         true,
		"apiTlsCertHash":           "AA11",
		"natsTlsCertHash":          "BB22",
		"natsServerHost":           "nats.example.local",
		"natsUseWssExternal":       true,
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agent-auth/me/configuration" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}

	fakeConn := &syncTestAgentConn{}
	debugSvc := debug.NewService(debug.Options{AgentConn: fakeConn})
	debugSvc.ApplyRuntimeConnectionConfig("http", u.Host, "token-123", "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7", "", "")

	a := &App{debugSvc: debugSvc}
	if err := a.refreshAgentConfiguration(context.Background()); err != nil {
		t.Fatalf("refreshAgentConfiguration: %v", err)
	}

	cfg := debugSvc.GetConfig()
	if !cfg.EnforceTlsHashValidation {
		t.Fatal("EnforceTlsHashValidation deveria ser true")
	}
	if cfg.ApiTlsCertHash != "AA11" {
		t.Fatalf("ApiTlsCertHash = %q", cfg.ApiTlsCertHash)
	}
	if cfg.NatsTlsCertHash != "BB22" {
		t.Fatalf("NatsTlsCertHash = %q", cfg.NatsTlsCertHash)
	}
	if cfg.NatsServerHost != "nats.example.local" {
		t.Fatalf("NatsServerHost = %q", cfg.NatsServerHost)
	}
	if !cfg.NatsUseWssExternal {
		t.Fatal("NatsUseWssExternal deveria ser true")
	}
	if fakeConn.reloadCount != 1 {
		t.Fatalf("reloadCount = %d, want 1", fakeConn.reloadCount)
	}
}
