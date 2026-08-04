package app

import (
	"testing"

	"discovery/app/status"
)

func TestNormalizeStatusServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "https with port", input: "https://exemplo.com.br:443", want: "exemplo.com.br"},
		{name: "wss with path", input: "wss://exemplo.com.br:443/nats", want: "exemplo.com.br"},
		{name: "nats with port", input: "nats://broker.exemplo.com.br:4222", want: "broker.exemplo.com.br"},
		{name: "host and port", input: "api.exemplo.com.br:8443", want: "api.exemplo.com.br"},
		{name: "ipv4 and port", input: "10.10.1.20:4222", want: "10.10.1.20"},
		{name: "already fqdn", input: "server.local", want: "server.local"},
		{name: "empty", input: "", want: ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := status.NormalizeServer(tt.input); got != tt.want {
				t.Fatalf("NormalizeServer(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestResolveStatusConnectionType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		transport string
		cfg       status.DebugConfig
		want      string
	}{
		{name: "direct nats transport", transport: "nats", want: "nats"},
		{name: "websocket transport wss", transport: "nats-wss", want: "wss"},
		{name: "websocket transport ws", transport: "nats-ws", want: "wss"},
		{name: "fallback from cfg nats", transport: "", cfg: status.DebugConfig{Scheme: "nats"}, want: "nats"},
		{name: "fallback from cfg websocket", transport: "", cfg: status.DebugConfig{NatsWsServer: "wss://bus.exemplo.com.br:443/nats"}, want: "wss"},
		{name: "unknown transport", transport: "mqtt", want: "mqtt"},
		{name: "no data", transport: "", cfg: status.DebugConfig{}, want: "-"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := status.ResolveConnectionType(tt.transport, tt.cfg); got != tt.want {
				t.Fatalf("ResolveConnectionType(%q) = %q, want %q", tt.transport, got, tt.want)
			}
		})
	}
}

func TestFirstStatusServerCandidate(t *testing.T) {
	t.Parallel()

	cfg := status.DebugConfig{
		Server:       "",
		NatsWsServer: "wss://ws.exemplo.com.br:443/nats",
		NatsServer:   "nats://nats.exemplo.com.br:4222",
		ApiServer:    "https://api.exemplo.com.br:443",
	}

	if got := status.FirstServerCandidate("nats://runtime.exemplo.com.br:4222", cfg); got != "nats://runtime.exemplo.com.br:4222" {
		t.Fatalf("FirstServerCandidate(agentServer) = %q", got)
	}

	if got := status.FirstServerCandidate("", cfg); got != "wss://ws.exemplo.com.br:443/nats" {
		t.Fatalf("FirstServerCandidate(cfg fallback) = %q", got)
	}
}
