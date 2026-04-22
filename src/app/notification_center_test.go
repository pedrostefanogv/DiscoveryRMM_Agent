package app

import (
	"testing"
)

func TestNormalizeNotificationResult(t *testing.T) {
	if got := normalizeNotificationResult("approved"); got != "approved" {
		t.Fatalf("expected approved, got %q", got)
	}
	if got := normalizeNotificationResult("DENIED"); got != "denied" {
		t.Fatalf("expected denied, got %q", got)
	}
	if got := normalizeNotificationResult("other"); got != "timeout_policy_applied" {
		t.Fatalf("expected timeout_policy_applied, got %q", got)
	}
}

func TestNormalizeNotificationModeAndSeverity(t *testing.T) {
	if got := normalizeNotificationMode("silencioso"); got != "silent" {
		t.Fatalf("expected silent, got %q", got)
	}
	if got := normalizeNotificationMode("confirm"); got != "require_confirmation" {
		t.Fatalf("expected require_confirmation, got %q", got)
	}
	if got := normalizeNotificationMode("qualquer"); got != "notify_only" {
		t.Fatalf("expected notify_only default, got %q", got)
	}

	if got := normalizeNotificationSeverity("informativo"); got != "low" {
		t.Fatalf("expected low, got %q", got)
	}
	if got := normalizeNotificationSeverity("alerta"); got != "medium" {
		t.Fatalf("expected medium, got %q", got)
	}
	if got := normalizeNotificationSeverity("critico"); got != "critical" {
		t.Fatalf("expected critical, got %q", got)
	}
}

func TestRespondToNotification(t *testing.T) {
	a := &App{pendingNotifyResult: map[string]chan string{"n1": make(chan string, 1)}}
	if ok := a.RespondToNotification("n1", "approved"); !ok {
		t.Fatalf("expected notification response to be accepted")
	}
	if ok := a.RespondToNotification("unknown", "approved"); ok {
		t.Fatalf("expected unknown notification to return false")
	}
}

func TestDispatchNotificationHeadless(t *testing.T) {
	a := &App{}
	resp := a.DispatchNotification(NotificationDispatchRequest{
		NotificationID: "n-headless",
		Mode:           "require_confirmation",
		Title:          "Teste",
		Message:        "Sem contexto",
	})
	if !resp.Accepted {
		t.Fatalf("expected accepted=true")
	}
	if resp.Result != "timeout_policy_applied" {
		t.Fatalf("expected timeout_policy_applied, got %q", resp.Result)
	}
}

func TestDispatchNotification_IdempotencyDeduplicates(t *testing.T) {
	a := &App{notificationByKey: map[string]string{}}

	first := a.DispatchNotification(NotificationDispatchRequest{
		IdempotencyKey: "notif-key-1",
		NotificationID: "notif-1",
		Mode:           "notify_only",
		Title:          "Teste",
	})
	if first.NotificationID == "" {
		t.Fatalf("expected notification id for first dispatch")
	}

	second := a.DispatchNotification(NotificationDispatchRequest{
		IdempotencyKey: "notif-key-1",
		NotificationID: "notif-2",
		Mode:           "notify_only",
		Title:          "Teste",
	})
	if second.AgentAction != "deduplicated" {
		t.Fatalf("expected deduplicated action, got %q", second.AgentAction)
	}
	if second.NotificationID != first.NotificationID {
		t.Fatalf("expected same notification id for duplicated key")
	}
}

func TestDispatchNotification_RolloutDisableNotifications(t *testing.T) {
	disabled := false
	a := &App{
		notificationByKey: map[string]string{},
		agentConfig: AgentConfiguration{
			Rollout: AgentRolloutConfig{EnableNotifications: &disabled},
		},
	}

	resp := a.DispatchNotification(NotificationDispatchRequest{
		NotificationID: "n-rollout-disabled",
		Mode:           "notify_only",
		EventType:      "install_start",
		Title:          "Teste",
	})
	if resp.Accepted {
		t.Fatalf("expected accepted=false when notifications are disabled")
	}
	if resp.AgentAction != "disabled_by_rollout" {
		t.Fatalf("expected disabled_by_rollout, got %q", resp.AgentAction)
	}
}

func TestDispatchNotification_RolloutDisablesRequireConfirmation(t *testing.T) {
	allowNotifications := true
	disableConfirm := false
	a := &App{
		notificationByKey: map[string]string{},
		agentConfig: AgentConfiguration{
			Rollout: AgentRolloutConfig{
				EnableNotifications:       &allowNotifications,
				EnableRequireConfirmation: &disableConfirm,
			},
		},
	}

	resp := a.DispatchNotification(NotificationDispatchRequest{
		NotificationID: "n-rollout-confirm-off",
		Mode:           "require_confirmation",
		EventType:      "install_start",
		Title:          "Teste",
		Message:        "Sem contexto",
	})
	if !resp.Accepted {
		t.Fatalf("expected accepted=true when mode is downgraded")
	}
	if resp.Result != "approved" {
		t.Fatalf("expected approved due notify_only downgrade, got %q", resp.Result)
	}
}

func TestIsNotificationEnabledForRollout_AllowBlockList(t *testing.T) {
	rollout := AgentRolloutConfig{
		AllowedNotificationEventTypes: []string{"install_start", "install_end"},
		BlockedNotificationEventTypes: []string{"install_end"},
	}
	if !isNotificationEnabledForRollout(rollout, "install_start") {
		t.Fatalf("expected install_start to be allowed")
	}
	if isNotificationEnabledForRollout(rollout, "install_end") {
		t.Fatalf("expected install_end to be blocked")
	}
	if isNotificationEnabledForRollout(rollout, "other") {
		t.Fatalf("expected events outside allow list to be denied")
	}
}

func TestApplyNotificationPolicyByEventType_OverridesPayload(t *testing.T) {
	timeout := 120
	req := NotificationDispatchRequest{
		EventType:      "install_start",
		Mode:           "notify_only",
		Severity:       "low",
		Layout:         "toast",
		TimeoutSeconds: 30,
	}
	cfg := AgentConfiguration{
		NotificationBranding: AgentNotificationBrandingConfig{CompanyName: "Meduza"},
		NotificationPolicies: []AgentNotificationPolicy{
			{
				EventType:      "install_start",
				Mode:           "require_confirmation",
				Severity:       "critical",
				TimeoutSeconds: &timeout,
				StyleOverride:  AgentNotificationStyleOverride{Layout: "modal", Background: "#111", Text: "#eee"},
				Actions:        []AgentNotificationAction{{ID: "approve", Label: "Aprovar", ActionType: "approve"}},
			},
		},
	}

	resolved := applyNotificationPolicyByEventType(req, cfg)
	if resolved.Mode != "require_confirmation" {
		t.Fatalf("expected mode overridden by policy, got %q", resolved.Mode)
	}
	if resolved.Severity != "critical" {
		t.Fatalf("expected severity overridden by policy, got %q", resolved.Severity)
	}
	if resolved.Layout != "modal" {
		t.Fatalf("expected layout overridden by policy, got %q", resolved.Layout)
	}
	if resolved.TimeoutSeconds != 120 {
		t.Fatalf("expected timeout overridden by policy, got %d", resolved.TimeoutSeconds)
	}
	if resolved.Metadata == nil {
		t.Fatalf("expected metadata enriched by policy")
	}
	if _, ok := resolved.Metadata["actions"]; !ok {
		t.Fatalf("expected policy actions in metadata")
	}
	if _, ok := resolved.Metadata["branding"]; !ok {
		t.Fatalf("expected branding in metadata")
	}
}

func TestFindNotificationPolicy_ByEventType(t *testing.T) {
	policies := []AgentNotificationPolicy{{EventType: "install_failed", Mode: "require_confirmation"}}
	policy, ok := findNotificationPolicy(policies, "INSTALL_FAILED")
	if !ok {
		t.Fatalf("expected policy to be found")
	}
	if policy.Mode != "require_confirmation" {
		t.Fatalf("expected mode require_confirmation")
	}
}
