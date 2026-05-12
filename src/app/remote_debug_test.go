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
			"natsSubject": "tenant.client-1.site.site-1.agent.agent-1.remote-debug.log",
		},
	})
	if err != nil {
		t.Fatalf("parseRemoteDebugCommand: %v", err)
	}
	if got := strings.TrimSpace(cmd.Stream.NatsSubject); got != "tenant.client-1.site.site-1.agent.agent-1.remote-debug.log" {
		t.Fatalf("NatsSubject = %q", got)
	}
}

func TestParseRemoteDebugCommand_AllowsNullOptionalFields(t *testing.T) {
	cmd, err := parseRemoteDebugCommand(map[string]any{
		"action":       "stop",
		"sessionId":    "sess-2",
		"logLevel":     nil,
		"startedAtUtc": nil,
		"expiresAtUtc": nil,
		"stoppedAtUtc": nil,
		"stream": map[string]any{
			"natsSubject": "tenant.client-1.site.site-1.agent.agent-1.remote-debug.log",
			"natsWssUrl":  nil,
		},
	})
	if err != nil {
		t.Fatalf("parseRemoteDebugCommand with nulls: %v", err)
	}
	if cmd.LogLevel != "info" {
		t.Fatalf("LogLevel = %q, want info", cmd.LogLevel)
	}
	if cmd.ExpiresAtUTC != "" {
		t.Fatalf("ExpiresAtUTC = %q, want empty", cmd.ExpiresAtUTC)
	}
	if cmd.Stream.NatsWssURL != "" {
		t.Fatalf("NatsWssURL = %q, want empty", cmd.Stream.NatsWssURL)
	}
}

func TestParseRemoteDebugCommand_AcceptsJSONStringPayload(t *testing.T) {
	raw := `{"action":"start","sessionId":"sess-3","logLevel":"debug","stream":{"natsSubject":"tenant.client-1.site.site-1.agent.agent-1.remote-debug.log"}}`
	cmd, err := parseRemoteDebugCommand(raw)
	if err != nil {
		t.Fatalf("parseRemoteDebugCommand string payload: %v", err)
	}
	if cmd.Action != "start" {
		t.Fatalf("Action = %q, want start", cmd.Action)
	}
	if cmd.SessionID != "sess-3" {
		t.Fatalf("SessionID = %q, want sess-3", cmd.SessionID)
	}
	if cmd.LogLevel != "debug" {
		t.Fatalf("LogLevel = %q, want debug", cmd.LogLevel)
	}
}

func TestBuildRemoteDebugPublishers_RequiresCanonicalNATSSubject(t *testing.T) {
	_, err := buildRemoteDebugPublishers(DebugConfig{}, remoteDebugStreamConfig{}, "token", "", "")
	if err == nil {
		t.Fatalf("expected error when natsSubject is missing")
	}
	if !strings.Contains(err.Error(), "subject NATS ausente") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildRemoteDebugPublishers_RejectsNonCanonicalNATSSubject(t *testing.T) {
	_, err := buildRemoteDebugPublishers(DebugConfig{}, remoteDebugStreamConfig{
		NatsSubject: "tenant.client-1.site.site-1.agent.agent-1.remote.debug",
	}, "token", "", "")
	if err == nil {
		t.Fatalf("expected error for non-canonical remote debug subject")
	}
	if !strings.Contains(err.Error(), ".remote-debug.log") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFormatRemoteDebugMessageWithOrigin_UI(t *testing.T) {
	if got := formatRemoteDebugMessageWithOrigin("ui", "erro xyz"); got != "[ui] erro xyz" {
		t.Fatalf("formatRemoteDebugMessageWithOrigin = %q", got)
	}
	if got := formatRemoteDebugMessageWithOrigin("ui", "[ui] erro xyz"); got != "[ui] erro xyz" {
		t.Fatalf("formatRemoteDebugMessageWithOrigin should keep existing prefix, got %q", got)
	}
	if got := formatRemoteDebugMessageWithOrigin("", "mensagem sem origem"); got != "mensagem sem origem" {
		t.Fatalf("formatRemoteDebugMessageWithOrigin with empty origin = %q", got)
	}
}

func TestDetectRemoteDebugLevel_DefaultsToTrace(t *testing.T) {
	if got := detectRemoteDebugLevel("linha sem tag de nivel"); got != "trace" {
		t.Fatalf("detectRemoteDebugLevel default = %q, want trace", got)
	}
}

func TestNormalizeRemoteDebugStreamLevel_DefaultsToTrace(t *testing.T) {
	if got := normalizeRemoteDebugStreamLevel("verbose"); got != "trace" {
		t.Fatalf("normalizeRemoteDebugStreamLevel = %q, want trace", got)
	}
}

func TestHandleAgentRuntimeCommand_UpdatePending(t *testing.T) {
	a := &App{updateTrigger: make(chan struct{}, 1)}
	handled, code, output, errText := a.handleAgentRuntimeCommand(context.Background(), "update", map[string]any{"action": "check-update"})
	if !handled {
		t.Fatalf("expected update command to be handled")
	}
	if code != 0 {
		t.Fatalf("expected update command code=0, got code=%d err=%q", code, errText)
	}
	if strings.TrimSpace(output) == "" {
		t.Fatalf("expected non-empty output on success, got output=%q err=%q", output, errText)
	}
}

func TestRemoteDebugHandleCommand_StopWithJSONStringPayloadDoesNotReturnParseError(t *testing.T) {
	m := newRemoteDebugManager(nil, nil, nil, nil, nil)
	raw := `{"action":"stop","sessionId":"sess-raw","logLevel":"info","expiresAtUtc":null,"stream":{"natsSubject":"tenant.client-1.site.site-1.agent.agent-1.remote-debug.log"}}`
	handled, code, _, errText := m.HandleCommand(context.Background(), "remotedebug", raw)
	if !handled {
		t.Fatalf("expected remotedebug command to be handled")
	}
	if code == 2 {
		t.Fatalf("expected no parse error (code=2), got err=%q", errText)
	}
	if code != 0 {
		t.Fatalf("expected stop to return code=0, got code=%d err=%q", code, errText)
	}
}

func TestRemoteDebugHandleCommand_StartWithJSONStringPayloadDoesNotReturnParseError(t *testing.T) {
	m := newRemoteDebugManager(nil, nil, nil, nil, nil)
	raw := `{"action":"start","sessionId":"sess-raw","logLevel":"debug","expiresAtUtc":"2026-05-12T02:35:31.6225487Z","stream":{"natsSubject":"tenant.client-1.site.site-1.agent.agent-1.remote-debug.log"}}`
	handled, code, _, errText := m.HandleCommand(context.Background(), "remotedebug", raw)
	if !handled {
		t.Fatalf("expected remotedebug command to be handled")
	}
	if code == 2 {
		t.Fatalf("expected no parse error (code=2), got err=%q", errText)
	}
	if code != 1 {
		t.Fatalf("expected start without config to fail as business error (code=1), got code=%d err=%q", code, errText)
	}
}
