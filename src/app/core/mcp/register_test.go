package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// stubAppBridge satisfaz AppBridge por embedding. Somente a validacao de
// parametros (que falha antes de tocar no bridge) e exercitada aqui.
type stubAppBridge struct{ AppBridge }

// TestTicketLifecycleToolsRegistered garante que a IA tem as tres acoes do
// ciclo de vida do chamado expostas e que os parametros obrigatorios sao
// validados antes de chamar a API.
func TestTicketLifecycleToolsRegistered(t *testing.T) {
	reg := NewRegistry()
	RegisterDiscoveryTools(reg, stubAppBridge{})

	for _, name := range []string{"close_ticket", "reopen_ticket", "rate_ticket"} {
		if reg.Find(name) == nil {
			t.Fatalf("ferramenta %q nao registrada", name)
		}
	}

	call := func(tool string, args map[string]any) error {
		raw, _ := json.Marshal(args)
		_, err := reg.Call(context.Background(), tool, raw)
		return err
	}

	if err := call("close_ticket", map[string]any{}); err == nil || !strings.Contains(err.Error(), "ticketId") {
		t.Fatalf("close_ticket deveria exigir ticketId, got=%v", err)
	}
	if err := call("reopen_ticket", map[string]any{"ticketId": "   "}); err == nil || !strings.Contains(err.Error(), "ticketId") {
		t.Fatalf("reopen_ticket deveria exigir ticketId, got=%v", err)
	}
	if err := call("rate_ticket", map[string]any{"ticketId": "abc", "rating": 9}); err == nil || !strings.Contains(err.Error(), "rating") {
		t.Fatalf("rate_ticket deveria validar rating, got=%v", err)
	}
	if err := call("close_ticket", map[string]any{"ticketId": "abc", "rating": 9}); err == nil || !strings.Contains(err.Error(), "rating") {
		t.Fatalf("close_ticket deveria validar rating quando informado, got=%v", err)
	}
}
