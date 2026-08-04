package agentconn

import (
	"encoding/json"
	"testing"
)

func TestNormalizeCommandType_AcceptsNumericAndString(t *testing.T) {
	if got := normalizeCommandType(" 8 "); got != "8" {
		t.Fatalf("normalizeCommandType string = %q, want 8", got)
	}
	if got := normalizeCommandType(float64(8)); got != "8" {
		t.Fatalf("normalizeCommandType numeric = %q, want 8", got)
	}
	if got := normalizeCommandType("RemoteDebug"); got != "remotedebug" {
		t.Fatalf("normalizeCommandType text = %q, want remotedebug", got)
	}
}

func TestNATSCommandEnvelope_AllowsNumericCommandType(t *testing.T) {
	const raw = `{"commandId":"abc","commandType":8,"payload":{"action":"start"}}`
	var env natsCommandEnvelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("json.Unmarshal falhou: %v", err)
	}
	if normalizeCommandType(env.CommandType) != "8" {
		t.Fatalf("commandType normalizado = %q, want 8", normalizeCommandType(env.CommandType))
	}
}
