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

// TestServiceConnectedMode_DefaultFalse verifica que o modo service-connected
// começa como false (sem service detectado por padrão).

// TestServiceConnectedMode_CanBeSetTrue verifica que o modo pode ser ativado
// (simulando a deteção bem-sucedida do service no startup).

func TestHeartbeatIntervalFromAgentConfig_UsesConfigValue(t *testing.T) {
	configured := 45
	got := heartbeatIntervalFromAgentConfig(AgentConfiguration{
		AgentHeartbeatIntervalSeconds: &configured,
	})
	if got != configured {
		t.Fatalf("HeartbeatInterval = %d, want %d", got, configured)
	}
}

func TestHeartbeatIntervalFromAgentConfig_RespectsMinimum(t *testing.T) {
	tooLow := 5
	got := heartbeatIntervalFromAgentConfig(AgentConfiguration{
		AgentHeartbeatIntervalSeconds: &tooLow,
	})
	if got != minHeartbeatIntervalSeconds {
		t.Fatalf("HeartbeatInterval = %d, want min %d", got, minHeartbeatIntervalSeconds)
	}
}

func TestHeartbeatIntervalFromAgentConfig_DefaultWhenNil(t *testing.T) {
	got := heartbeatIntervalFromAgentConfig(AgentConfiguration{})
	if got != defaultHeartbeatIntervalSeconds {
		t.Fatalf("HeartbeatInterval (fallback) = %d, want %d", got, defaultHeartbeatIntervalSeconds)
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
