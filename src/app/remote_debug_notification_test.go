package app

import (
	"context"
	"strings"
	"testing"
)

func TestHandleAgentRuntimeCommand_NotificationDispatchApproved(t *testing.T) {
	a := &App{notificationByKey: map[string]string{}}
	handled, code, output, errText := a.handleAgentRuntimeCommand(context.Background(), "notification", map[string]any{
		"notificationId": "n1",
		"mode":           "notify_only",
		"title":          "Aviso",
		"message":        "Teste",
	})
	if !handled {
		t.Fatalf("expected command to be handled")
	}
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d (err=%s)", code, errText)
	}
	if !strings.Contains(output, "n1") {
		t.Fatalf("expected output to include notification id")
	}
}

func TestHandleAgentRuntimeCommand_NotificationPayloadInvalid(t *testing.T) {
	a := &App{}
	handled, code, _, errText := a.handleAgentRuntimeCommand(context.Background(), "notify", "{invalid")
	if !handled {
		t.Fatalf("expected command to be handled")
	}
	if code != 2 {
		t.Fatalf("expected exit code 2 for invalid payload, got %d", code)
	}
	if strings.TrimSpace(errText) == "" {
		t.Fatalf("expected non-empty error text")
	}
}
