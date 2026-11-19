package status

import (
	"errors"
	"strings"
	"testing"
)

func TestApplyRealtimeStatus(t *testing.T) {
	// Nil safety.
	ApplyRealtimeStatus(nil, RealtimeStatus{NATSConnected: true})

	var ov Overview
	ApplyRealtimeStatus(&ov, RealtimeStatus{NATSConnected: true, RealtimeConnectedAgents: 5})
	if !ov.RealtimeAvailable || !ov.RealtimeNATSConnected || ov.RealtimeConnectedAgents != 5 {
		t.Fatalf("unexpected overview when NATS connected: %+v", ov)
	}
	if ov.RealtimeMessage != "Realtime operacional" {
		t.Fatalf("RealtimeMessage = %q, want 'Realtime operacional'", ov.RealtimeMessage)
	}

	ApplyRealtimeStatus(&ov, RealtimeStatus{NATSConnected: false, RealtimeConnectedAgents: 0})
	if !ov.RealtimeAvailable || ov.RealtimeNATSConnected || ov.RealtimeConnectedAgents != 0 {
		t.Fatalf("unexpected overview when NATS disconnected: %+v", ov)
	}
	if ov.RealtimeMessage != "Realtime indisponível no momento" {
		t.Fatalf("RealtimeMessage = %q, want 'Realtime indisponível no momento'", ov.RealtimeMessage)
	}
}

func TestApplyRealtimeFallbackFromAgentStatus_NilGuards(t *testing.T) {
	ApplyRealtimeFallbackFromAgentStatus(nil, AgentStatus{}, errors.New("boom"))
	var ov Overview
	ApplyRealtimeFallbackFromAgentStatus(&ov, AgentStatus{Connected: true}, nil)
	if ov.RealtimeAvailable || ov.RealtimeMessage != "" {
		t.Fatalf("expected no-op when err is nil, got %+v", ov)
	}
}

func TestApplyRealtimeFallbackFromAgentStatus_NetworkErrors(t *testing.T) {
	var ovOnline Overview
	ApplyRealtimeFallbackFromAgentStatus(&ovOnline, AgentStatus{TransportConnected: true}, errors.New("dial tcp 10.0.0.1:443: connectex: connection refused"))
	if !ovOnline.RealtimeAvailable || !ovOnline.RealtimeNATSConnected {
		t.Fatalf("expected realtime available via local transport, got %+v", ovOnline)
	}
	if strings.Contains(ovOnline.RealtimeMessage, "connectex") {
		t.Fatalf("raw network error leaked to RealtimeMessage: %q", ovOnline.RealtimeMessage)
	}

	var ovOffline Overview
	ApplyRealtimeFallbackFromAgentStatus(&ovOffline, AgentStatus{}, errors.New("lookup api.example.com: no such host"))
	if ovOffline.RealtimeAvailable || ovOffline.RealtimeNATSConnected {
		t.Fatalf("expected realtime unavailable when disconnected, got %+v", ovOffline)
	}
	if strings.Contains(ovOffline.RealtimeMessage, "no such host") {
		t.Fatalf("raw DNS error leaked to RealtimeMessage: %q", ovOffline.RealtimeMessage)
	}
}

func TestApplyRealtimeFallbackFromAgentStatus_UnauthorizedAndOtherErrors(t *testing.T) {
	// Generic non-network, non-401 error preserves message.
	var ov500 Overview
	ApplyRealtimeFallbackFromAgentStatus(&ov500, AgentStatus{Connected: true}, errors.New("HTTP 500 internal error"))
	if ov500.RealtimeMessage != "HTTP 500 internal error" || ov500.RealtimeAvailable {
		t.Fatalf("unexpected 500 fallback: %+v", ov500)
	}

	// 401 when transport is down.
	var ov401Down Overview
	ApplyRealtimeFallbackFromAgentStatus(&ov401Down, AgentStatus{}, errors.New("HTTP 401 Unauthorized"))
	if ov401Down.RealtimeAvailable || !strings.Contains(ov401Down.RealtimeMessage, "não autorizado") {
		t.Fatalf("unexpected 401 disconnected fallback: %+v", ov401Down)
	}

	// 401 when transport is NATS/WSS.
	var ov401Nats Overview
	ApplyRealtimeFallbackFromAgentStatus(&ov401Nats, AgentStatus{TransportConnected: true, Transport: "nats-wss"}, errors.New("autenticação necessária"))
	if !ov401Nats.RealtimeAvailable || !ov401Nats.RealtimeNATSConnected || ov401Nats.RealtimeConnectedAgents != 1 {
		t.Fatalf("unexpected 401 nats-wss fallback: %+v", ov401Nats)
	}

	// 401 when connected over non-NATS transport.
	var ov401Other Overview
	ApplyRealtimeFallbackFromAgentStatus(&ov401Other, AgentStatus{Connected: true, Transport: "http"}, errors.New("HTTP 401"))
	if !ov401Other.RealtimeAvailable || ov401Other.RealtimeNATSConnected || ov401Other.RealtimeConnectedAgents != 1 {
		t.Fatalf("unexpected 401 other transport fallback: %+v", ov401Other)
	}
}

func TestIsRealtimeNetworkAndUnauthorizedErrors(t *testing.T) {
	if IsRealtimeNetworkError(nil) {
		t.Fatal("nil should not be network error")
	}
	for _, msg := range []string{
		"No Such Host",
		"dial tcp 1.2.3.4:443",
		"dial udp 1.2.3.4:53",
		"i/o timeout",
		"context deadline exceeded",
		"tls: handshake failure",
		"network is unreachable",
		"no route to host",
		"temporary failure in name resolution",
		"Servidor Inacessível",
	} {
		if !IsRealtimeNetworkError(errors.New(msg)) {
			t.Errorf("IsRealtimeNetworkError(%q) = false, want true", msg)
		}
	}
	if IsRealtimeNetworkError(errors.New("HTTP 401 unauthorized")) {
		t.Error("401 should not match IsRealtimeNetworkError")
	}

	if IsRealtimeUnauthorizedError(nil) {
		t.Fatal("nil should not be unauthorized error")
	}
	for _, msg := range []string{"HTTP 401", "Unauthorized", "autenticação necessária", "autenticacao necessaria"} {
		if !IsRealtimeUnauthorizedError(errors.New(msg)) {
			t.Errorf("IsRealtimeUnauthorizedError(%q) = false, want true", msg)
		}
	}
	if IsRealtimeUnauthorizedError(errors.New("HTTP 503 service unavailable")) {
		t.Error("503 should not match IsRealtimeUnauthorizedError")
	}
}
