package app

import (
	"encoding/json"
	"testing"

	"discovery/internal/selfupdate"
)

func TestParseAgentConfiguration_BasicFields(t *testing.T) {
	payloadData := []byte(`{"recoveryEnabled":true,"discoveryEnabled":false,"p2pFilesEnabled":true,"supportEnabled":true,"natsServerHost":"nats.example.local","natsUseWssExternal":true,"enforceTlsHashValidation":true,"handshakeEnabled":true,"apiTlsCertHash":"aa:bb:cc","natsTlsCertHash":"11 22 33","chatAIEnabled":false,"inventoryIntervalHours":12,"agentHeartbeatIntervalSeconds":60,"siteId":"s1","clientId":"c1","resolvedAt":"2026-03-17T13:45:00.000Z","autoUpdate":{"enabled":true,"checkEveryHours":4},"psadt":{"enabled":true,"requiredVersion":"4.1.8","autoInstallModule":true,"installSource":"powershell_gallery","executionTimeoutSeconds":1800,"fallbackPolicy":"winget_then_choco","installOnStartup":true,"installOnDemand":true},"notificationBranding":{"companyName":"Meduza","logoUrl":"https://example/logo.svg","bannerUrl":"https://example/banner.png","theme":{"surface":"#111827","text":"#f9fafb","accent":"#0ea5e9","success":"#0b6e4f","warning":"#8a4e12","danger":"#9a031e"}}}`)

	cfg, err := parseAgentConfiguration(payloadData)
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
	if cfg.NatsServerHost != "nats.example.local" {
		t.Fatalf("expected natsServerHost set")
	}
	if cfg.NatsUseWssExternal == nil || !*cfg.NatsUseWssExternal {
		t.Fatalf("expected natsUseWssExternal=true")
	}
	if cfg.EnforceTlsHashValidation == nil || !*cfg.EnforceTlsHashValidation {
		t.Fatalf("expected enforceTlsHashValidation=true")
	}
	if cfg.HandshakeEnabled == nil || !*cfg.HandshakeEnabled {
		t.Fatalf("expected handshakeEnabled=true")
	}
	if cfg.ApiTlsCertHash != "AA:BB:CC" {
		t.Fatalf("expected apiTlsCertHash normalized, got %q", cfg.ApiTlsCertHash)
	}
	if cfg.NatsTlsCertHash != "11 22 33" {
		t.Fatalf("expected natsTlsCertHash preserved casing normalization")
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
	if !cfg.AgentUpdate.Enabled || cfg.AgentUpdate.CheckEveryHours != 4 {
		t.Fatalf("expected legacy autoUpdate fallback into agentUpdate")
	}
	if cfg.PSADT.Enabled == nil || !*cfg.PSADT.Enabled {
		t.Fatalf("expected psadt.enabled=true")
	}
	if cfg.PSADT.RequiredVersion != "4.1.8" {
		t.Fatalf("expected psadt.requiredVersion=4.1.8")
	}
	if cfg.PSADT.ExecutionTimeoutSeconds == nil || *cfg.PSADT.ExecutionTimeoutSeconds != 1800 {
		t.Fatalf("expected psadt.executionTimeoutSeconds=1800")
	}
	if len(cfg.PSADT.SuccessExitCodes) != 2 || cfg.PSADT.SuccessExitCodes[0] != 0 || cfg.PSADT.SuccessExitCodes[1] != 3010 {
		t.Fatalf("expected default psadt.successExitCodes=[0,3010]")
	}
	if len(cfg.PSADT.RebootExitCodes) != 2 || cfg.PSADT.RebootExitCodes[0] != 1641 || cfg.PSADT.RebootExitCodes[1] != 3010 {
		t.Fatalf("expected default psadt.rebootExitCodes=[1641,3010]")
	}
	if cfg.PSADT.TimeoutAction != "fail" {
		t.Fatalf("expected default psadt.timeoutAction=fail")
	}
	if cfg.PSADT.UnknownExitCodePolicy != "recoverable_failure" {
		t.Fatalf("expected default psadt.unknownExitCodePolicy=recoverable_failure")
	}
	if cfg.NotificationBranding.CompanyName != "Meduza" {
		t.Fatalf("expected notificationBranding.companyName")
	}
	if cfg.NotificationBranding.Theme.Accent != "#0ea5e9" {
		t.Fatalf("expected notificationBranding.theme.accent")
	}
	if len(cfg.NotificationPolicies) != 0 {
		t.Fatalf("expected notification policies absent in this payload")
	}
}

func TestParseAgentConfiguration_AgentUpdateOverridesLegacyPolicy(t *testing.T) {
	payload := []byte(`{"autoUpdate":{"enabled":true,"checkEveryHours":24},"agentUpdate":{"enabled":false,"checkOnStartup":false,"checkPeriodically":true,"checkOnSyncManifest":false,"checkEveryHours":12,"preferredArtifactType":"PortableZip","requireSignatureValidation":true}}`)
	cfg, err := parseAgentConfiguration(payload)
	if err != nil {
		t.Fatalf("expected agentUpdate payload to parse, got %v", err)
	}
	if cfg.AgentUpdate.Enabled {
		t.Fatalf("expected agentUpdate.enabled=false to override legacy autoUpdate")
	}
	if cfg.AgentUpdate.CheckOnStartup {
		t.Fatalf("expected agentUpdate.checkOnStartup=false")
	}
	if !cfg.AgentUpdate.CheckPeriodically || cfg.AgentUpdate.CheckOnSyncManifest {
		t.Fatalf("expected explicit periodic/sync flags from agentUpdate")
	}
	if cfg.AgentUpdate.CheckEveryHours != 12 {
		t.Fatalf("expected agentUpdate.checkEveryHours=12, got %d", cfg.AgentUpdate.CheckEveryHours)
	}
	if cfg.AgentUpdate.PreferredArtifactType != "PortableZip" {
		t.Fatalf("expected preferredArtifactType preserved, got %q", cfg.AgentUpdate.PreferredArtifactType)
	}
	if !cfg.AgentUpdate.RequireSignatureValidation {
		t.Fatalf("expected requireSignatureValidation=true")
	}
}

func TestParseAgentConfiguration_NotificationPolicies(t *testing.T) {
	payloadData := []byte(`{"notificationPolicies":[{"eventType":"install_start","mode":"notify_only","severity":"medium","timeoutSeconds":8,"styleOverride":{"layout":"toast","background":"#1e293b","text":"#f8fafc"},"actions":[{"id":"details","label":"Ver detalhes","actionType":"open_logs"}]}]}`)
	cfg, err := parseAgentConfiguration(payloadData)
	if err != nil {
		t.Fatalf("expected policy payload to parse, got %v", err)
	}
	if len(cfg.NotificationPolicies) != 1 {
		t.Fatalf("expected one notification policy")
	}
	if cfg.NotificationPolicies[0].EventType != "install_start" {
		t.Fatalf("expected policy eventType=install_start")
	}
	if len(cfg.NotificationPolicies[0].Actions) != 1 {
		t.Fatalf("expected one policy action")
	}
}

func TestParseAgentConfiguration_PSADTPolicyOverrides(t *testing.T) {
	payloadData := []byte(`{"psadt":{"enabled":true,"successExitCodes":[0,2022],"rebootExitCodes":"3010,1641","ignoreExitCodes":[42],"timeoutAction":"RETRY","unknownExitCodePolicy":"FATAL"}}`)
	cfg, err := parseAgentConfiguration(payloadData)
	if err != nil {
		t.Fatalf("expected psadt override payload to parse, got %v", err)
	}
	if len(cfg.PSADT.SuccessExitCodes) != 2 || cfg.PSADT.SuccessExitCodes[0] != 0 || cfg.PSADT.SuccessExitCodes[1] != 2022 {
		t.Fatalf("expected custom successExitCodes to be preserved")
	}
	if len(cfg.PSADT.RebootExitCodes) != 2 || cfg.PSADT.RebootExitCodes[0] != 3010 || cfg.PSADT.RebootExitCodes[1] != 1641 {
		t.Fatalf("expected rebootExitCodes string to parse as list")
	}
	if len(cfg.PSADT.IgnoreExitCodes) != 1 || cfg.PSADT.IgnoreExitCodes[0] != 42 {
		t.Fatalf("expected ignoreExitCodes to parse")
	}
	if cfg.PSADT.TimeoutAction != "retry" {
		t.Fatalf("expected timeoutAction normalized to lower-case")
	}
	if cfg.PSADT.UnknownExitCodePolicy != "fatal" {
		t.Fatalf("expected unknownExitCodePolicy normalized to lower-case")
	}
}

func TestParseAgentConfiguration_RolloutDefaults(t *testing.T) {
	cfg, err := parseAgentConfiguration([]byte(`{"siteId":"s1"}`))
	if err != nil {
		t.Fatalf("expected parse without rollout, got %v", err)
	}
	if cfg.Rollout.EnableNotifications == nil || !*cfg.Rollout.EnableNotifications {
		t.Fatalf("expected rollout.enableNotifications default=true")
	}
	if cfg.Rollout.EnableRequireConfirmation == nil || !*cfg.Rollout.EnableRequireConfirmation {
		t.Fatalf("expected rollout.enableRequireConfirmation default=true")
	}
	if cfg.Rollout.EnablePSADTBootstrap == nil || !*cfg.Rollout.EnablePSADTBootstrap {
		t.Fatalf("expected rollout.enablePsadtBootstrap default=true")
	}
	if cfg.Rollout.EnableConsolidationEngine == nil || !*cfg.Rollout.EnableConsolidationEngine {
		t.Fatalf("expected rollout.enableConsolidationEngine default=true")
	}
	if cfg.Rollout.CommandResultOfflineMode != OfflineQueueModeEnqueueAndDrain {
		t.Fatalf("expected commandResultOfflineMode=%q, got %q", OfflineQueueModeEnqueueAndDrain, cfg.Rollout.CommandResultOfflineMode)
	}
	if cfg.Rollout.P2PTelemetryOfflineMode != OfflineQueueModeEnqueueAndDrain {
		t.Fatalf("expected p2pTelemetryOfflineMode=%q, got %q", OfflineQueueModeEnqueueAndDrain, cfg.Rollout.P2PTelemetryOfflineMode)
	}
}

func TestParseAgentConfiguration_RolloutOverrides(t *testing.T) {
	payload := []byte(`{"rollout":{"enableNotifications":false,"enableRequireConfirmation":false,"enablePsadtBootstrap":false,"enableConsolidationEngine":false,"commandResultOfflineMode":"enqueue_only","p2pTelemetryOfflineMode":"logging_only","allowedNotificationEventTypes":["install_start","install_end"],"blockedNotificationEventTypes":["critical_override"]}}`)
	cfg, err := parseAgentConfiguration(payload)
	if err != nil {
		t.Fatalf("expected rollout payload to parse, got %v", err)
	}
	if cfg.Rollout.EnableNotifications == nil || *cfg.Rollout.EnableNotifications {
		t.Fatalf("expected rollout.enableNotifications=false")
	}
	if cfg.Rollout.EnableRequireConfirmation == nil || *cfg.Rollout.EnableRequireConfirmation {
		t.Fatalf("expected rollout.enableRequireConfirmation=false")
	}
	if cfg.Rollout.EnablePSADTBootstrap == nil || *cfg.Rollout.EnablePSADTBootstrap {
		t.Fatalf("expected rollout.enablePsadtBootstrap=false")
	}
	if cfg.Rollout.EnableConsolidationEngine == nil || *cfg.Rollout.EnableConsolidationEngine {
		t.Fatalf("expected rollout.enableConsolidationEngine=false")
	}
	if cfg.Rollout.CommandResultOfflineMode != OfflineQueueModeEnqueueOnly {
		t.Fatalf("expected commandResultOfflineMode=%q, got %q", OfflineQueueModeEnqueueOnly, cfg.Rollout.CommandResultOfflineMode)
	}
	if cfg.Rollout.P2PTelemetryOfflineMode != OfflineQueueModeLoggingOnly {
		t.Fatalf("expected p2pTelemetryOfflineMode=%q, got %q", OfflineQueueModeLoggingOnly, cfg.Rollout.P2PTelemetryOfflineMode)
	}
	if len(cfg.Rollout.AllowedNotificationEventTypes) != 2 {
		t.Fatalf("expected two allowed event types")
	}
	if len(cfg.Rollout.BlockedNotificationEventTypes) != 1 || cfg.Rollout.BlockedNotificationEventTypes[0] != "critical_override" {
		t.Fatalf("expected blockedNotificationEventTypes to parse")
	}
}

func TestParseAgentConfiguration_ConsolidationPolicies(t *testing.T) {
	payload := []byte(`{"consolidation":{"enabled":true,"policies":[{"dataType":"p2p_telemetry","windowMode":"batch_1min"},{"dataType":"command_result","windowMode":"1MIN"},{"dataType":"logs","windowMode":"batch_5min"}]}}`)
	cfg, err := parseAgentConfiguration(payload)
	if err != nil {
		t.Fatalf("expected consolidation payload to parse, got %v", err)
	}
	if cfg.Consolidation.Enabled == nil || !*cfg.Consolidation.Enabled {
		t.Fatalf("expected consolidation.enabled=true")
	}
	if len(cfg.Consolidation.Policies) != 3 {
		t.Fatalf("expected three consolidation policies, got %d", len(cfg.Consolidation.Policies))
	}
	if cfg.Consolidation.Policies[0].WindowMode != ConsolidationMode1Min {
		t.Fatalf("expected first policy normalized to %q, got %q", ConsolidationMode1Min, cfg.Consolidation.Policies[0].WindowMode)
	}
	if cfg.Consolidation.Policies[1].WindowMode != ConsolidationMode1Min {
		t.Fatalf("expected second policy normalized to %q, got %q", ConsolidationMode1Min, cfg.Consolidation.Policies[1].WindowMode)
	}
	if cfg.Consolidation.Policies[2].WindowMode != ConsolidationMode5Min {
		t.Fatalf("expected third policy normalized to %q, got %q", ConsolidationMode5Min, cfg.Consolidation.Policies[2].WindowMode)
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
		AgentUpdate:                   selfupdate.DefaultPolicy(),
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
