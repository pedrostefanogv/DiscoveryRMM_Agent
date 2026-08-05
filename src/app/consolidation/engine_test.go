package consolidation

import (
	"testing"

	"discovery/app/agentconfig"
)

func TestApplyAgentConfig(t *testing.T) {
	enabled := true
	rolloutEnabled := true
	engine := New(nil, "agent-1")

	engine.ApplyAgentConfig(agentconfig.AgentConfiguration{
		Consolidation: agentconfig.AgentConsolidationConfig{
			Enabled: &enabled,
			Policies: []agentconfig.AgentConsolidationPolicy{
				{DataType: "p2p_telemetry", WindowMode: "batch_1min"},
				{DataType: "logs", WindowMode: "batch_5min"},
			},
		},
		Rollout: agentconfig.AgentRolloutConfig{EnableConsolidationEngine: &rolloutEnabled},
	})

	if !engine.IsEnabled() {
		t.Fatalf("expected consolidation engine to be enabled")
	}
	if got := engine.GetWindowMode("p2p_telemetry"); got != agentconfig.ConsolidationMode1Min {
		t.Fatalf("expected p2p_telemetry mode %q, got %q", agentconfig.ConsolidationMode1Min, got)
	}
	if got := engine.GetWindowMode("logs"); got != agentconfig.ConsolidationMode5Min {
		t.Fatalf("expected logs mode %q, got %q", agentconfig.ConsolidationMode5Min, got)
	}
	if got := engine.GetWindowMode("command_result"); got != agentconfig.ConsolidationModeRealtime {
		t.Fatalf("expected default command_result mode %q, got %q", agentconfig.ConsolidationModeRealtime, got)
	}
}

func TestApplyAgentConfigRolloutDisabled(t *testing.T) {
	enabled := true
	rolloutDisabled := false
	engine := New(nil, "agent-1")

	engine.ApplyAgentConfig(agentconfig.AgentConfiguration{
		Consolidation: agentconfig.AgentConsolidationConfig{Enabled: &enabled},
		Rollout:       agentconfig.AgentRolloutConfig{EnableConsolidationEngine: &rolloutDisabled},
	})

	if engine.IsEnabled() {
		t.Fatalf("expected consolidation engine to be disabled when rollout disables it")
	}
}
