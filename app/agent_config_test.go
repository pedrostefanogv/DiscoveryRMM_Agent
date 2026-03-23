package app

import (
	"encoding/json"
	"testing"
)

func TestParseAgentConfiguration_BasicFields(t *testing.T) {
	payload := `{
		"recoveryEnabled": true,
		"discoveryEnabled": false,
		"p2pFilesEnabled": true,
		"supportEnabled": true,
		"chatAIEnabled": false,
		"inventoryIntervalHours": 12,
		"agentHeartbeatIntervalSeconds": 60,
		"siteId": "s1",
		"clientId": "c1",
		"resolvedAt": "2026-03-17T13:45:00.000Z",
		"autoUpdate": {
			"enabled": true,
			"checkEveryHours": 4
		}
	}`

	cfg, err := parseAgentConfiguration([]byte(payload))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if cfg.RecoveryEnabled == nil || !*cfg.RecoveryEnabled {
		t.Fatalf("expected recoveryEnabled=true")
	}
	if cfg.DiscoveryEnabled == nil || *cfg.DiscoveryEnabled {
		t.Fatalf("expected discoveryEnabled=false")
	}
	if cfg.P2PFilesEnabled == nil || !*cfg.P2PFilesEnabled {
		t.Fatalf("expected p2pFilesEnabled=true")
	}
	if cfg.ChatAIEnabled == nil || *cfg.ChatAIEnabled {
		t.Fatalf("expected chatAIEnabled=false")
	}
	if cfg.InventoryIntervalHours == nil || *cfg.InventoryIntervalHours != 12 {
		t.Fatalf("expected inventoryIntervalHours=12")
	}
	if cfg.AgentHeartbeatIntervalSeconds == nil || *cfg.AgentHeartbeatIntervalSeconds != 60 {
		t.Fatalf("expected agentHeartbeatIntervalSeconds=60")
	}
	if cfg.SiteID != "s1" || cfg.ClientID != "c1" {
		t.Fatalf("expected siteId/clientId set")
	}
	if cfg.AutoUpdate.Enabled != true || cfg.AutoUpdate.CheckEveryHours != 4 {
		t.Fatalf("expected autoUpdate enabled and checkEveryHours")
	}
}

func TestParseAgentConfiguration_InvalidJSON(t *testing.T) {
	_, err := parseAgentConfiguration([]byte(`{invalid`))
	if err == nil {
		t.Fatalf("expected error for invalid JSON")
	}
}

func TestParseAgentConfiguration_UnknownFieldIgnored(t *testing.T) {
	payload := `{"unknownField": true}`
	_, err := parseAgentConfiguration([]byte(payload))
	if err != nil {
		t.Fatalf("expected no error for unknown field, got %v", err)
	}
}

func TestAgentConfig_MarshalRoundtrip(t *testing.T) {
	cfg := AgentConfiguration{
		RecoveryEnabled:               ptrBool(true),
		DiscoveryEnabled:              ptrBool(true),
		P2PFilesEnabled:               ptrBool(false),
		SupportEnabled:                ptrBool(true),
		ChatAIEnabled:                 ptrBool(true),
		InventoryIntervalHours:        ptrInt(3),
		AgentHeartbeatIntervalSeconds: ptrInt(45),
		SiteID:                        "site-x",
		ClientID:                      "client-x",
		ResolvedAt:                    "2026-01-01T00:00:00Z",
		AutoUpdate:                    AgentAutoUpdateConfig{Enabled: true},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var decoded AgentConfiguration
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
}

func ptrBool(v bool) *bool { return &v }
func ptrInt(v int) *int    { return &v }
