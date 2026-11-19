package status

import "testing"

func TestFirstServerCandidate(t *testing.T) {
	cfg := DebugConfig{
		Server:       "cfg.example.com",
		NatsWsServer: "wss.example.com",
		NatsServer:   "nats.example.com",
		ApiServer:    "api.example.com",
	}
	if got := FirstServerCandidate("  agent.example.com  ", cfg); got != "agent.example.com" {
		t.Errorf("agentServer should win, got %q", got)
	}
	if got := FirstServerCandidate("   ", cfg); got != "cfg.example.com" {
		t.Errorf("cfg.Server should win next, got %q", got)
	}
	cfg.Server = ""
	if got := FirstServerCandidate("", cfg); got != "wss.example.com" {
		t.Errorf("cfg.NatsWsServer should win next, got %q", got)
	}
	cfg.NatsWsServer = ""
	if got := FirstServerCandidate("", cfg); got != "nats.example.com" {
		t.Errorf("cfg.NatsServer should win next, got %q", got)
	}
	cfg.NatsServer = ""
	if got := FirstServerCandidate("", cfg); got != "api.example.com" {
		t.Errorf("cfg.ApiServer should win last, got %q", got)
	}
	cfg.ApiServer = ""
	if got := FirstServerCandidate("", cfg); got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestMapTransportConnectionType(t *testing.T) {
	cases := map[string]string{
		"":            "",
		"   ":         "",
		"nats":        "nats",
		"NATS":        "nats",
		"nats-ws":     "wss",
		"nats-wss":    "wss",
		"ws":          "wss",
		"wss":         "wss",
		"custom-ws":   "wss",
		"nats-custom": "nats",
		"grpc":        "grpc",
	}
	for input, want := range cases {
		if got := MapTransportConnectionType(input); got != want {
			t.Errorf("MapTransportConnectionType(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestResolveConnectionType(t *testing.T) {
	if got := ResolveConnectionType("nats-ws", DebugConfig{NatsServer: "nats://h:4222"}); got != "wss" {
		t.Errorf("explicit transport should win, got %q", got)
	}
	if got := ResolveConnectionType("", DebugConfig{NatsWsServer: "wss://h/nats"}); got != "wss" {
		t.Errorf("NatsWsServer alone = %q, want wss", got)
	}
	if got := ResolveConnectionType("", DebugConfig{NatsServer: "nats://h:4222"}); got != "nats" {
		t.Errorf("NatsServer = %q, want nats", got)
	}
	if got := ResolveConnectionType("", DebugConfig{Scheme: "NATS"}); got != "nats" {
		t.Errorf("Scheme=NATS = %q, want nats", got)
	}
	if got := ResolveConnectionType("", DebugConfig{}); got != "-" {
		t.Errorf("empty config = %q, want '-'", got)
	}
}

func TestNormalizeServer(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"", ""},
		{"   ", ""},
		{"https://api.example.com:8443/v1/health", "api.example.com"},
		{"wss://nats.example.com/nats", "nats.example.com"},
		{"api.example.com:443/path?x=1", "api.example.com"},
		{"192.168.1.10:8080", "192.168.1.10"},
		{"[::1]:4222", "::1"},
		{"[2001:db8::1]", "2001:db8::1"},
		{"plain.host.local", "plain.host.local"},
	}
	for _, tc := range cases {
		if got := NormalizeServer(tc.raw); got != tc.want {
			t.Errorf("NormalizeServer(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}
