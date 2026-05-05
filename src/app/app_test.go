package app

import (
	"fmt"
	"strings"
	"testing"
)

// TestParseUpgradeOutput_WithSpinner verifies that the parser handles the \r-only
// progress spinners that winget emits before the actual table header.
func TestParseUpgradeOutput_WithSpinner(t *testing.T) {
	// Reproduce the exact byte pattern from winget: spinner lines use bare \r
	// (no \n) to overwrite, followed by the real table terminated with \r\n.
	spinner := "\r   - " + strings.Repeat(" ", 115) + "\r"
	raw := spinner + spinner +
		"Name                  Id                          Version Available Source\r\n" +
		"--------------------------------------------------------------------------\r\n" +
		"BCUninstaller 5.9.0.0 Klocman.BulkCrapUninstaller 5.9.0.0 6.0       winget\r\n" +
		"1 upgrades available.\r\n"

	items := parseUpgradeOutput(raw)
	if len(items) != 1 {
		t.Fatalf("expected 1 upgrade item, got %d", len(items))
	}
	item := items[0]
	if item.ID != "Klocman.BulkCrapUninstaller" {
		t.Errorf("ID = %q, want %q", item.ID, "Klocman.BulkCrapUninstaller")
	}
	if item.CurrentVersion != "5.9.0.0" {
		t.Errorf("CurrentVersion = %q, want %q", item.CurrentVersion, "5.9.0.0")
	}
	if item.AvailableVersion != "6.0" {
		t.Errorf("AvailableVersion = %q, want %q", item.AvailableVersion, "6.0")
	}
	if item.Source != "winget" {
		t.Errorf("Source = %q, want %q", item.Source, "winget")
	}
}

// TestParseUpgradeOutput_Clean verifies parsing without any spinner prefix.
func TestParseUpgradeOutput_Clean(t *testing.T) {
	raw := "Name                  Id                          Version Available Source\r\n" +
		"--------------------------------------------------------------------------\r\n" +
		"BCUninstaller 5.9.0.0 Klocman.BulkCrapUninstaller 5.9.0.0 6.0       winget\r\n" +
		"1 upgrades available.\r\n"

	items := parseUpgradeOutput(raw)
	if len(items) != 1 {
		t.Fatalf("expected 1 upgrade item, got %d", len(items))
	}
	if items[0].ID != "Klocman.BulkCrapUninstaller" {
		t.Errorf("ID = %q, want %q", items[0].ID, "Klocman.BulkCrapUninstaller")
	}
}

// TestParseUpgradeOutput_Empty verifies that no items are returned when there are no upgrades.
func TestParseUpgradeOutput_Empty(t *testing.T) {
	raw := "No applicable upgrade found.\r\n"
	items := parseUpgradeOutput(raw)
	if len(items) != 0 {
		t.Errorf("expected 0 items, got %d", len(items))
	}
}

func TestServiceOnlyUnavailablePayload_HasUserGuidance(t *testing.T) {
	payload := serviceOnlyUnavailablePayload("failed to connect")
	if payload.Running != false {
		t.Fatalf("expected running=false, got %v", payload.Running)
	}
	if payload.ServiceOnly != true {
		t.Fatalf("expected service_only=true, got %v", payload.ServiceOnly)
	}
	if payload.Error == nil || strings.TrimSpace(*payload.Error) == "" {
		t.Fatalf("expected non-empty error, got %v", payload.Error)
	}
	msg := payload.UserMessage
	if strings.TrimSpace(msg) == "" {
		t.Fatalf("expected non-empty user_message, got %q", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "reinicie") {
		t.Fatalf("expected restart guidance, got %q", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "suporte") {
		t.Fatalf("expected support guidance, got %q", msg)
	}
}

func TestGetServiceHealth_ServiceClientNil_ReturnsServiceOnlyGuidance(t *testing.T) {
	a := &App{}
	health := a.GetServiceHealth()
	if health["running"] != false {
		t.Fatalf("expected running=false, got %v", health["running"])
	}
	if health["service_only"] != true {
		t.Fatalf("expected service_only=true, got %v", health["service_only"])
	}
	msg, _ := health["user_message"].(string)
	if !strings.Contains(strings.ToLower(msg), "contate o suporte") {
		t.Fatalf("expected support guidance in user_message, got %q", msg)
	}
}

// TestServiceConnectedMode_DefaultFalse verifica que o modo service-connected
// começa como false (sem service detectado por padrão).
func TestServiceConnectedMode_DefaultFalse(t *testing.T) {
	a := &App{}
	if a.serviceConnectedMode.Load() {
		t.Fatal("esperado serviceConnectedMode=false no estado inicial")
	}
}

// TestServiceConnectedMode_CanBeSetTrue verifica que o modo pode ser ativado
// (simulando a deteção bem-sucedida do service no startup).
func TestServiceConnectedMode_CanBeSetTrue(t *testing.T) {
	a := &App{}
	a.serviceConnectedMode.Store(true)
	if !a.serviceConnectedMode.Load() {
		t.Fatal("esperado serviceConnectedMode=true após Store(true)")
	}
}

func TestAgentStatusFromServiceStatusData(t *testing.T) {
	got := agentStatusFromServiceStatusData(map[string]interface{}{
		"agent_connected":  true,
		"agent_id":         "agent-123",
		"agent_server":     "server.example:443",
		"agent_last_event": "conectado",
		"agent_transport":  "signalr",
	})

	if !got.Connected {
		t.Fatal("expected connected=true")
	}
	if got.AgentID != "agent-123" {
		t.Fatalf("AgentID = %q", got.AgentID)
	}
	if got.Server != "server.example:443" {
		t.Fatalf("Server = %q", got.Server)
	}
	if got.Transport != "signalr" {
		t.Fatalf("Transport = %q", got.Transport)
	}
	if got.LastEvent != "conectado" {
		t.Fatalf("LastEvent = %q", got.LastEvent)
	}
}

func TestShouldRunLocalP2P_NoServiceConnected(t *testing.T) {
	a := &App{}
	if !a.shouldRunLocalP2P() {
		t.Fatal("expected local P2P to run when service is not connected")
	}
}

func TestShouldRunLocalP2P_ServiceConnectedNormalModeSkips(t *testing.T) {
	a := &App{}
	a.serviceConnectedMode.Store(true)
	if a.shouldRunLocalP2P() {
		t.Fatal("expected local P2P to be skipped when service is connected in normal mode")
	}
}

func TestShouldRunLocalP2P_ServiceConnectedDebugModeRuns(t *testing.T) {
	a := &App{runtimeFlags: RuntimeFlags{DebugMode: true}}
	a.serviceConnectedMode.Store(true)
	if !a.shouldRunLocalP2P() {
		t.Fatal("expected local P2P to run in debug mode even when service is connected")
	}
}

func TestHeartbeatIntervalFromAgentConfig_DebugForcedInterval(t *testing.T) {
	configured := 45
	got := heartbeatIntervalFromAgentConfig(AgentConfiguration{
		AgentHeartbeatIntervalSeconds: &configured,
	})
	if got != debugForcedHeartbeatIntervalSeconds {
		t.Fatalf("HeartbeatInterval = %d, want %d", got, debugForcedHeartbeatIntervalSeconds)
	}

	got = heartbeatIntervalFromAgentConfig(AgentConfiguration{})
	if got != debugForcedHeartbeatIntervalSeconds {
		t.Fatalf("HeartbeatInterval (fallback) = %d, want %d", got, debugForcedHeartbeatIntervalSeconds)
	}
}

func TestApplyRealtimeFallbackFromAgentStatus_UsesLocalConnectionOnUnauthorized(t *testing.T) {
	out := StatusOverview{}
	applyRealtimeFallbackFromAgentStatus(&out, AgentStatus{
		Connected: true,
		Transport: "nats",
	}, fmt.Errorf("HTTP 401 Unauthorized: {\"message\":\"Autenticação necessária.\"}"))

	if !out.RealtimeAvailable {
		t.Fatal("expected realtimeAvailable=true")
	}
	if !out.RealtimeNATSConnected {
		t.Fatal("expected realtimeNatsConnected=true")
	}
	if out.RealtimeConnectedAgents != 1 {
		t.Fatalf("RealtimeConnectedAgents = %d", out.RealtimeConnectedAgents)
	}
	if !strings.Contains(strings.ToLower(out.RealtimeMessage), "nats") {
		t.Fatalf("unexpected RealtimeMessage = %q", out.RealtimeMessage)
	}
}
