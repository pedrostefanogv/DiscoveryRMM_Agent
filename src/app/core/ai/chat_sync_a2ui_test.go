package ai

import (
	"encoding/json"
	"testing"
)

// O endpoint síncrono devolve as interfaces A2UI no JSON da resposta (não há
// SSE). Regressão: o campo era ignorado e o card sumia no fallback.
func TestAgentChatSyncResponseParsesA2uiMessages(t *testing.T) {
	raw := `{"sessionId":"s1","assistantMessage":"texto com card",` +
		`"tokensUsed":10,"conversationTokensTotal":20,"latencyMs":30,` +
		`"a2uiMessages":["{\"version\":\"v0.9\",\"createSurface\":{}}","{\"version\":\"v0.9\",\"updateComponents\":{}}"]}`

	var resp agentChatSyncResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.A2uiMessages) != 2 {
		t.Fatalf("esperado 2 mensagens A2UI, obteve %d", len(resp.A2uiMessages))
	}
	if resp.AssistantMessage != "texto com card" {
		t.Fatalf("assistantMessage perdido: %q", resp.AssistantMessage)
	}
}

// Servidores antigos não enviam o campo: slice vazia, sem erro.
func TestAgentChatSyncResponseWithoutA2ui(t *testing.T) {
	var resp agentChatSyncResponse
	if err := json.Unmarshal([]byte(`{"sessionId":"s1","assistantMessage":"ok"}`), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.A2uiMessages) != 0 {
		t.Fatalf("deveria ficar vazio: %#v", resp.A2uiMessages)
	}
}
