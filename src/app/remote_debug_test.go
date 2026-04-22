package app

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestComputeRemoteDebugDeadline_DefaultOneHourCap(t *testing.T) {
	now := time.Date(2026, 3, 28, 10, 0, 0, 0, time.UTC)
	got := computeRemoteDebugDeadline("", now)
	want := now.Add(time.Hour)
	if !got.Equal(want) {
		t.Fatalf("deadline = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestComputeRemoteDebugDeadline_UsesSoonerExpiry(t *testing.T) {
	now := time.Date(2026, 3, 28, 10, 0, 0, 0, time.UTC)
	expires := now.Add(20 * time.Minute).Format(time.RFC3339)
	got := computeRemoteDebugDeadline(expires, now)
	want := now.Add(20 * time.Minute)
	if !got.Equal(want) {
		t.Fatalf("deadline = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestComputeRemoteDebugDeadline_CapsLongExpiryToOneHour(t *testing.T) {
	now := time.Date(2026, 3, 28, 10, 0, 0, 0, time.UTC)
	expires := now.Add(3 * time.Hour).Format(time.RFC3339)
	got := computeRemoteDebugDeadline(expires, now)
	want := now.Add(time.Hour)
	if !got.Equal(want) {
		t.Fatalf("deadline = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestIsRemoteDebugCommandType(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{in: "8", want: true},
		{in: "RemoteDebug", want: true},
		{in: "remote-debug", want: true},
		{in: "cmd", want: false},
	}
	for _, tc := range cases {
		if got := isRemoteDebugCommandType(tc.in); got != tc.want {
			t.Fatalf("isRemoteDebugCommandType(%q) = %t, want %t", tc.in, got, tc.want)
		}
	}
}

func TestParseRemoteDebugCommand_UsesCanonicalNATSSubject(t *testing.T) {
	cmd, err := parseRemoteDebugCommand(map[string]any{
		"action":    "start",
		"sessionId": "sess-1",
		"stream": map[string]any{
			"natsSubject": "tenant.client-1.site.site-1.agent.agent-1.remote.debug",
		},
	})
	if err != nil {
		t.Fatalf("parseRemoteDebugCommand: %v", err)
	}
	if got := strings.TrimSpace(cmd.Stream.NatsSubject); got != "tenant.client-1.site.site-1.agent.agent-1.remote.debug" {
		t.Fatalf("NatsSubject = %q", got)
	}
}

func TestBuildRemoteDebugPublishers_RequiresCanonicalNATSSubject(t *testing.T) {
	_, err := buildRemoteDebugPublishers(DebugConfig{}, remoteDebugStreamConfig{}, "token")
	if err == nil {
		t.Fatalf("expected error when natsSubject is missing")
	}
	if !strings.Contains(err.Error(), "subject NATS ausente") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseRemoteDebugCommand_PreservesSignalRMethod(t *testing.T) {
	cmd, err := parseRemoteDebugCommand(map[string]any{
		"action":    "start",
		"sessionId": "sess-1",
		"stream": map[string]any{
			"natsSubject":   "tenant.client-1.site.site-1.agent.agent-1.remote.debug",
			"signalRMethod": "PushRemoteDebugLog",
		},
	})
	if err != nil {
		t.Fatalf("parseRemoteDebugCommand: %v", err)
	}
	if got := strings.TrimSpace(cmd.Stream.SignalRMethod); got != "PushRemoteDebugLog" {
		t.Fatalf("SignalRMethod = %q", got)
	}
}

func TestHandleAgentRuntimeCommand_UpdateRequiresService(t *testing.T) {
	a := &App{}
	handled, code, output, errText := a.handleAgentRuntimeCommand(context.Background(), "update", map[string]any{"action": "check-update"})
	if !handled {
		t.Fatalf("expected update command to be handled")
	}
	if code != 1 {
		t.Fatalf("expected update command to fail without service, got code=%d err=%q", code, errText)
	}
	if strings.TrimSpace(output) != "" {
		t.Fatalf("expected no output on failure, got %q", output)
	}
	if !strings.Contains(errText, "Windows Service") {
		t.Fatalf("expected service-first error, got %q", errText)
	}
}
