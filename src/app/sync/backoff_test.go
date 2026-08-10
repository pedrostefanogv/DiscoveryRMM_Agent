package sync

import (
	"strings"
	"testing"
	"time"

	"discovery/app/core/agentconn"
	debug "discovery/app/debug"
	"discovery/app/syncmeta"
)

func TestRandomNonCriticalBackoffDuration_Range(t *testing.T) {
	for i := 0; i < 50; i++ {
		d := randomNonCriticalBackoffDuration()
		if d < syncmeta.NonCriticalBackoffMin || d > syncmeta.NonCriticalBackoffMax {
			t.Fatalf("duracao fora do range: %s", d)
		}
	}
}

func TestHandleGlobalPong_OverloadedSetsBackoff(t *testing.T) {
	deps := newFakeDeps(t)
	b := NewBackoff(deps)
	overloaded := true
	b.HandleGlobalPong(agentconn.GlobalPongMessage{EventType: "pong", ServerOverloaded: &overloaded})

	remaining, deferred, reason := b.NonCriticalBackoffWindow()
	if !deferred {
		t.Fatalf("esperava trafego nao-critico adiado")
	}
	if remaining < 9*time.Minute || remaining > syncmeta.NonCriticalBackoffMax {
		t.Fatalf("remaining fora do intervalo esperado: %s", remaining)
	}
	if reason == "" {
		t.Fatalf("esperava motivo de adiamento")
	}
}

func TestHandleGlobalPong_NotOverloadedClearsBackoff(t *testing.T) {
	deps := newFakeDeps(t)
	b := NewBackoff(deps)
	b.ExtendNonCriticalBackoff(12*time.Minute, "manual")

	overloaded := false
	b.HandleGlobalPong(agentconn.GlobalPongMessage{EventType: "pong", ServerOverloaded: &overloaded})

	if _, deferred, _ := b.NonCriticalBackoffWindow(); deferred {
		t.Fatalf("nao esperava trafego nao-critico adiado apos overloaded=false")
	}
}

func TestResolveAgentConnectivity_TransportDisconnected(t *testing.T) {
	deps := newFakeDeps(t)
	b := NewBackoff(deps)
	status := b.ResolveAgentConnectivity(debug.AgentStatus{Connected: false, TransportConnected: false})
	if status.Connected {
		t.Fatalf("esperava connected=false quando transporte estiver desconectado")
	}
	if !strings.Contains(strings.ToLower(status.OnlineReason), "transporte") {
		t.Fatalf("onlineReason inesperado: %q", status.OnlineReason)
	}
}

func TestResolveAgentConnectivity_FreshPongKeepsOnline(t *testing.T) {
	deps := newFakeDeps(t)
	b := NewBackoff(deps)
	now := time.Now().UTC()
	status := b.ResolveAgentConnectivity(debug.AgentStatus{
		Connected:           true,
		TransportConnected:  true,
		LastGlobalPongAtUTC: now.Format(time.RFC3339),
	})
	if !status.Connected {
		t.Fatalf("esperava connected=true com pong recente")
	}
	if status.GlobalPongStale {
		t.Fatalf("nao esperava stale=true com pong recente")
	}
}

func TestResolveAgentConnectivity_StalePongKeepsOnlineWhenTransportUp(t *testing.T) {
	deps := newFakeDeps(t)
	b := NewBackoff(deps)
	stale := time.Now().UTC().Add(-(syncmeta.GlobalPongStaleAfter + 2*time.Minute))
	status := b.ResolveAgentConnectivity(debug.AgentStatus{
		Connected:           true,
		TransportConnected:  true,
		LastGlobalPongAtUTC: stale.Format(time.RFC3339),
	})
	// Transporte conectado mantém o agente online, mesmo com pong stale
	// (a staleness é informativa — não derruba a conectividade).
	if !status.Connected {
		t.Fatalf("esperava connected=true com transporte conectado, mesmo com pong stale")
	}
	if !status.GlobalPongStale {
		t.Fatalf("esperava stale=true com pong antigo")
	}
}

// TestResolveAgentConnectivity_StalePongMarksOfflineWithoutTransport garante
// que, sem transporte conectado, o agente fica offline (e o stale do pong não
// é sinalizado, pois a prioridade é "transporte desconectado").
func TestResolveAgentConnectivity_StalePongMarksOfflineWithoutTransport(t *testing.T) {
	deps := newFakeDeps(t)
	b := NewBackoff(deps)
	stale := time.Now().UTC().Add(-(syncmeta.GlobalPongStaleAfter + 2*time.Minute))
	status := b.ResolveAgentConnectivity(debug.AgentStatus{
		Connected:           false,
		TransportConnected:  false,
		LastGlobalPongAtUTC: stale.Format(time.RFC3339),
	})
	if status.Connected {
		t.Fatalf("esperava connected=false sem transporte conectado")
	}
}
