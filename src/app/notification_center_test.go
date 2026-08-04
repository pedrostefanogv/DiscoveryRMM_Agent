package app

import (
	"testing"

	"discovery/app/services/notifications"
)

func TestRespondToNotification(t *testing.T) {
	s := notifications.New(notifications.Deps{})
	if ok := s.Respond("unknown", "approved"); ok {
		t.Fatalf("expected unknown notification to return false")
	}
}

func TestDispatchNotificationHeadless(t *testing.T) {
	s := notifications.New(notifications.Deps{})
	resp := s.Dispatch(notifications.DispatchRequest{
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
	s := notifications.New(notifications.Deps{})

	first := s.Dispatch(notifications.DispatchRequest{
		IdempotencyKey: "notif-key-1",
		NotificationID: "notif-1",
		Mode:           "notify_only",
		Title:          "Teste",
	})
	if first.NotificationID == "" {
		t.Fatalf("expected notification id for first dispatch")
	}

	second := s.Dispatch(notifications.DispatchRequest{
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
	s := notifications.New(notifications.Deps{
		GetAgentConfiguration: func() notifications.AgentConfiguration {
			return notifications.AgentConfiguration{
				Rollout: notifications.AgentRolloutConfig{EnableNotifications: &disabled},
			}
		},
	})

	resp := s.Dispatch(notifications.DispatchRequest{
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
	s := notifications.New(notifications.Deps{
		GetAgentConfiguration: func() notifications.AgentConfiguration {
			return notifications.AgentConfiguration{
				Rollout: notifications.AgentRolloutConfig{
					EnableNotifications:       &allowNotifications,
					EnableRequireConfirmation: &disableConfirm,
				},
			}
		},
	})

	resp := s.Dispatch(notifications.DispatchRequest{
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
