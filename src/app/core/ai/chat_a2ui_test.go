package ai

import (
	"strings"
	"testing"
)

// TestParseMultiRoundSSE_A2ui verifica que o evento "a2ui" emitido pelo
// servidor (com A2uiJson como string) chega ao callback onA2ui SEM aspas
// duplas literais — ou seja, como JSON cru pronto para o frontend parsear.
//
// Regressão: antes, A2UI era json.RawMessage, o que causava dupla codificação
// (o frontend recebia "{\"version\":...}" com aspas e descartava a mensagem).
func TestParseMultiRoundSSE_A2ui(t *testing.T) {
	const sse = `data: {"type":"a2ui","a2uiJson":"{\"version\":\"v0.9\",\"createSurface\":{\"surfaceId\":\"inv\",\"catalogId\":\"https://a2ui.org/specification/v0_9/basic_catalog.json\"}}"}
data: {"type":"done","sessionId":"abc-123"}`

	var received []string
	var pending []pendingToolCall

	sessionID, done, err := (&Service{}).parseMultiRoundSSE(
		strings.NewReader(sse),
		nil,
		&pending,
		func(msg string) { received = append(received, msg) },
	)
	if err != nil {
		t.Fatalf("parseMultiRoundSSE erro: %v", err)
	}
	if !done {
		t.Errorf("esperado done=true")
	}
	if sessionID != "abc-123" {
		t.Errorf("sessionID: esperado abc-123, obteve %q", sessionID)
	}
	if len(received) != 1 {
		t.Fatalf("esperado 1 mensagem a2ui, obteve %d", len(received))
	}

	msg := received[0]
	// Não deve começar com aspas duplas (dupla codificação).
	if strings.HasPrefix(msg, "\"") {
		t.Errorf("mensagem a2ui veio com aspas duplas (dupla codificação): %q", msg)
	}
	// Deve conter o JSON válido.
	if !strings.Contains(msg, `"version":"v0.9"`) {
		t.Errorf("mensagem a2ui sem version: %q", msg)
	}
	if !strings.Contains(msg, `"createSurface"`) {
		t.Errorf("mensagem a2ui sem createSurface: %q", msg)
	}
}

// TestParseMultiRoundSSE_A2uiNullIgnored verifica que "a2uiJson":null é ignorado.
func TestParseMultiRoundSSE_A2uiNullIgnored(t *testing.T) {
	const sse = `data: {"type":"a2ui","a2uiJson":null}
data: {"type":"done","sessionId":"s1"}`

	var received []string
	var pending []pendingToolCall

	_, _, err := (&Service{}).parseMultiRoundSSE(
		strings.NewReader(sse),
		nil,
		&pending,
		func(msg string) { received = append(received, msg) },
	)
	if err != nil {
		t.Fatalf("parseMultiRoundSSE erro: %v", err)
	}
	if len(received) != 0 {
		t.Errorf("esperado 0 mensagens a2ui (null ignorado), obteve %d", len(received))
	}
}
