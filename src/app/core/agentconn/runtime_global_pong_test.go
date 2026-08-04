package agentconn

import (
	"testing"
	"time"
)

func TestParseGlobalPongMessage_BoolOverloaded(t *testing.T) {
	pong, err := parseGlobalPongMessage([]byte(`{"eventType":"pong","serverTimeUtc":"2026-05-06T12:00:00Z","serverOverloaded":true}`))
	if err != nil {
		t.Fatalf("parseGlobalPongMessage: %v", err)
	}
	if pong.EventType != "pong" {
		t.Fatalf("EventType = %q", pong.EventType)
	}
	if pong.ServerTimeUTC != "2026-05-06T12:00:00Z" {
		t.Fatalf("ServerTimeUTC = %q", pong.ServerTimeUTC)
	}
	if pong.ServerOverloaded == nil || !*pong.ServerOverloaded {
		t.Fatalf("ServerOverloaded esperado true, got=%v", pong.ServerOverloaded)
	}
}

func TestParseGlobalPongMessage_NullOverloaded(t *testing.T) {
	pong, err := parseGlobalPongMessage([]byte(`{"eventType":"pong","serverOverloaded":null}`))
	if err != nil {
		t.Fatalf("parseGlobalPongMessage: %v", err)
	}
	if pong.ServerOverloaded != nil {
		t.Fatalf("ServerOverloaded esperado nil, got=%v", *pong.ServerOverloaded)
	}
}

func TestParseGlobalPongMessage_RejectsInvalidEventType(t *testing.T) {
	if _, err := parseGlobalPongMessage([]byte(`{"eventType":"ping","serverOverloaded":false}`)); err == nil {
		t.Fatalf("esperava erro para eventType invalido")
	}
}

func TestShouldReconnectForMissingGlobalPong_WithoutAnyPong(t *testing.T) {
	now := time.Date(2026, 5, 6, 14, 0, 0, 0, time.UTC)
	connectedAt := now.Add(-(globalPongReconnectAfter + 2*time.Second))

	reconnect, age := shouldReconnectForMissingGlobalPong(now, connectedAt, time.Time{})
	if !reconnect {
		t.Fatal("esperava reconexao quando nao ha global pong por periodo prolongado")
	}
	if age <= globalPongReconnectAfter {
		t.Fatalf("idade = %s, esperado > %s", age, globalPongReconnectAfter)
	}
}

func TestShouldReconnectForMissingGlobalPong_UsesLastPongTimestamp(t *testing.T) {
	now := time.Date(2026, 5, 6, 14, 0, 0, 0, time.UTC)
	connectedAt := now.Add(-30 * time.Minute)
	lastPongAt := now.Add(-2 * time.Minute)

	reconnect, age := shouldReconnectForMissingGlobalPong(now, connectedAt, lastPongAt)
	if reconnect {
		t.Fatal("nao esperava reconexao com global pong recente")
	}
	if age >= globalPongReconnectAfter {
		t.Fatalf("idade = %s, esperado < %s", age, globalPongReconnectAfter)
	}
}

func TestShouldReconnectForMissingGlobalPong_WhenLastPongIsStale(t *testing.T) {
	now := time.Date(2026, 5, 6, 14, 0, 0, 0, time.UTC)
	connectedAt := now.Add(-30 * time.Minute)
	lastPongAt := now.Add(-(globalPongReconnectAfter + time.Second))

	reconnect, age := shouldReconnectForMissingGlobalPong(now, connectedAt, lastPongAt)
	if !reconnect {
		t.Fatal("esperava reconexao quando ultimo global pong ficou stale")
	}
	if age <= globalPongReconnectAfter {
		t.Fatalf("idade = %s, esperado > %s", age, globalPongReconnectAfter)
	}
}
