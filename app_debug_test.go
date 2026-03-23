package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestInstallerConfigUnmarshalDiscoveryEnabledBool(t *testing.T) {
	var cfg InstallerConfig
	err := json.Unmarshal([]byte(`{"serverUrl":"api.example.com","apiKey":"key","discoveryEnabled":true}`), &cfg)
	if err != nil {
		t.Fatalf("unmarshal bool: %v", err)
	}
	if cfg.DiscoveryEnabled == nil || !*cfg.DiscoveryEnabled {
		t.Fatalf("discoveryEnabled deveria ser true")
	}
}

func TestInstallerConfigUnmarshalDiscoveryEnabledNumericCompatibility(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "one means true", raw: `{"discoveryEnabled":1}`, want: true},
		{name: "zero means false", raw: `{"discoveryEnabled":0}`, want: false},
		{name: "string one means true", raw: `{"discoveryEnabled":"1"}`, want: true},
		{name: "string false means false", raw: `{"discoveryEnabled":"false"}`, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var cfg InstallerConfig
			if err := json.Unmarshal([]byte(tc.raw), &cfg); err != nil {
				t.Fatalf("unmarshal compat: %v", err)
			}
			if cfg.DiscoveryEnabled == nil {
				t.Fatal("discoveryEnabled nao deveria ser nil")
			}
			if *cfg.DiscoveryEnabled != tc.want {
				t.Fatalf("discoveryEnabled = %v, want %v", *cfg.DiscoveryEnabled, tc.want)
			}
		})
	}
}

func TestInstallerConfigUnmarshalDiscoveryEnabledInvalid(t *testing.T) {
	var cfg InstallerConfig
	err := json.Unmarshal([]byte(`{"discoveryEnabled":2}`), &cfg)
	if err == nil {
		t.Fatal("esperava erro para discoveryEnabled invalido")
	}
}

func TestGetRealtimeStatus_SetsAgentAuthHeadersAndAgentID(t *testing.T) {
	const (
		token   = "token-123"
		agentID = "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7"
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Fatalf("Authorization = %q, want %q", got, "Bearer "+token)
		}
		if got := r.Header.Get("X-Agent-Token"); got != token {
			t.Fatalf("X-Agent-Token = %q, want %q", got, token)
		}
		if got := r.Header.Get("X-Agent-ID"); got != agentID {
			t.Fatalf("X-Agent-ID = %q, want %q", got, agentID)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"natsConnected":true,"signalrConnectedAgents":7,"checkedAtUtc":"2026-03-23T12:00:00Z"}`))
	}))
	defer server.Close()

	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}

	a := &App{}
	a.debugConfig = DebugConfig{
		ApiScheme: "http",
		ApiServer: u.Host,
		AuthToken: token,
		AgentID:   agentID,
	}

	status, err := a.GetRealtimeStatus()
	if err != nil {
		t.Fatalf("GetRealtimeStatus: %v", err)
	}
	if !status.NATSConnected {
		t.Fatal("NATSConnected deveria ser true")
	}
	if status.SignalRConnectedAgents != 7 {
		t.Fatalf("SignalRConnectedAgents = %d, want 7", status.SignalRConnectedAgents)
	}
}
