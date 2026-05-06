package app

import (
	"strings"
	"testing"
	"time"

	"discovery/internal/agentconn"
)

func TestRandomNonCriticalBackoffDuration_Range(t *testing.T) {
	for i := 0; i < 50; i++ {
		d := randomNonCriticalBackoffDuration()
		if d < nonCriticalBackoffMin || d > nonCriticalBackoffMax {
			t.Fatalf("duracao fora do range: %s", d)
		}
	}
}

func TestHandleGlobalPong_OverloadedSetsBackoff(t *testing.T) {
	a := &App{}
	overloaded := true
	a.handleGlobalPong(agentconn.GlobalPongMessage{EventType: "pong", ServerOverloaded: &overloaded})

	remaining, deferred, reason := a.nonCriticalBackoffWindow()
	if !deferred {
		t.Fatalf("esperava trafego nao-critico adiado")
	}
	if remaining < 9*time.Minute || remaining > nonCriticalBackoffMax {
		t.Fatalf("remaining fora do intervalo esperado: %s", remaining)
	}
	if reason == "" {
		t.Fatalf("esperava motivo de adiamento")
	}
}

func TestHandleGlobalPong_NotOverloadedClearsBackoff(t *testing.T) {
	a := &App{}
	a.extendNonCriticalBackoff(12*time.Minute, "manual")

	overloaded := false
	a.handleGlobalPong(agentconn.GlobalPongMessage{EventType: "pong", ServerOverloaded: &overloaded})

	if _, deferred, _ := a.nonCriticalBackoffWindow(); deferred {
		t.Fatalf("nao esperava trafego nao-critico adiado apos overloaded=false")
	}
}

func TestResolveAgentConnectivity_TransportDisconnected(t *testing.T) {
	a := &App{}
	status := a.resolveAgentConnectivity(AgentStatus{Connected: false, TransportConnected: false})
	if status.Connected {
		t.Fatalf("esperava connected=false quando transporte estiver desconectado")
	}
	if !strings.Contains(strings.ToLower(status.OnlineReason), "transporte") {
		t.Fatalf("onlineReason inesperado: %q", status.OnlineReason)
	}
}

func TestResolveAgentConnectivity_FreshPongKeepsOnline(t *testing.T) {
	a := &App{}
	now := time.Now().UTC()
	status := a.resolveAgentConnectivity(AgentStatus{
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

func TestResolveAgentConnectivity_StalePongMarksOffline(t *testing.T) {
	a := &App{}
	stale := time.Now().UTC().Add(-(globalPongStaleAfter + 2*time.Minute))
	status := a.resolveAgentConnectivity(AgentStatus{
		Connected:           true,
		TransportConnected:  true,
		LastGlobalPongAtUTC: stale.Format(time.RFC3339),
	})
	if status.Connected {
		t.Fatalf("esperava connected=false com pong stale")
	}
	if !status.GlobalPongStale {
		t.Fatalf("esperava stale=true com pong antigo")
	}
}
