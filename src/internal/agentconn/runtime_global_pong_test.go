package agentconn

import "testing"

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
