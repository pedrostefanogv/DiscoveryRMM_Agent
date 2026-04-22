package app

import "testing"

func TestConsolidationEngineApplyAgentConfig(t *testing.T) {
	enabled := true
	rolloutEnabled := true
	engine := newConsolidationEngine(nil, "agent-1")

	engine.ApplyAgentConfig(AgentConfiguration{
		Consolidation: AgentConsolidationConfig{
			Enabled: &enabled,
			Policies: []AgentConsolidationPolicy{
				{DataType: "p2p_telemetry", WindowMode: "batch_1min"},
				{DataType: "logs", WindowMode: "batch_5min"},
			},
		},
		Rollout: AgentRolloutConfig{EnableConsolidationEngine: &rolloutEnabled},
	})

	if !engine.IsEnabled() {
		t.Fatalf("expected consolidation engine to be enabled")
	}
	if got := engine.GetWindowMode("p2p_telemetry"); got != ConsolidationMode1Min {
		t.Fatalf("expected p2p_telemetry mode %q, got %q", ConsolidationMode1Min, got)
	}
	if got := engine.GetWindowMode("logs"); got != ConsolidationMode5Min {
		t.Fatalf("expected logs mode %q, got %q", ConsolidationMode5Min, got)
	}
	if got := engine.GetWindowMode("command_result"); got != ConsolidationModeRealtime {
		t.Fatalf("expected default command_result mode %q, got %q", ConsolidationModeRealtime, got)
	}
}

func TestConsolidationEngineApplyAgentConfigRolloutDisabled(t *testing.T) {
	enabled := true
	rolloutDisabled := false
	engine := newConsolidationEngine(nil, "agent-1")

	engine.ApplyAgentConfig(AgentConfiguration{
		Consolidation: AgentConsolidationConfig{Enabled: &enabled},
		Rollout:       AgentRolloutConfig{EnableConsolidationEngine: &rolloutDisabled},
	})

	if engine.IsEnabled() {
		t.Fatalf("expected consolidation engine to remain disabled when rollout blocks it")
	}
}
