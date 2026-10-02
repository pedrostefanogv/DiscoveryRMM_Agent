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

	for _, name := range []string{"close_ticket", "reopen_ticket", "rate_ticket", "get_department_fields"} {
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
	if err := call("get_department_fields", map[string]any{}); err == nil || !strings.Contains(err.Error(), "departmentId") {
		t.Fatalf("get_department_fields deveria exigir departmentId, got=%v", err)
	}
}

// TestRegisteredToolNamesMatchProviderPattern é a regressão do erro
// "Provider stream error: Invalid 'tools[0].function.name'": provedores
// OpenAI/OpenRouter exigem ^[a-zA-Z0-9_-]{1,64}$, e nomes como "memory/list"
// derrubavam o turno inteiro (a tool nem chegava a ser oferecida ao LLM).
func TestRegisteredToolNamesMatchProviderPattern(t *testing.T) {
	reg := NewRegistry()
	RegisterDiscoveryTools(reg, stubAppBridge{})
	tools := reg.Tools()
	if len(tools) < 10 {
		t.Fatalf("poucas tools registradas: %d", len(tools))
	}
	for _, tool := range tools {
		if !providerToolNamePattern.MatchString(tool.Name) {
			t.Errorf("nome de tool invalido para o provedor: %q", tool.Name)
		}
		if tool.Name != SanitizeToolName(tool.Name) {
			t.Errorf("nome nao normalizado: %q -> %q", tool.Name, SanitizeToolName(tool.Name))
		}
	}
	// As memorias locais continuam registradas (com o nome ja normalizado).
	for _, name := range []string{"memory_list", "memory_create", "memory_delete"} {
		if reg.Find(name) == nil {
			t.Errorf("tool %q nao registrada", name)
		}
	}
}

func TestSanitizeToolName(t *testing.T) {
	cases := map[string]string{
		"memory/list":   "memory_list",
		"memory/create": "memory_create",
		"ok_name-1":     "ok_name-1",
		"  espaco  ":    "espaco",
		"a.b:c d":       "a_b_c_d",
		"":              "tool",
		"///":           "tool",
	}
	for in, want := range cases {
		if got := SanitizeToolName(in); got != want {
			t.Errorf("SanitizeToolName(%q) = %q, want %q", in, got, want)
		}
	}
	// Nome muito longo é truncado em 64 caracteres.
	long := strings.Repeat("a", 100)
	if got := SanitizeToolName(long); len(got) != 64 {
		t.Errorf("nome longo = %d chars, want 64", len(got))
	}
	// O registro guarda o nome já normalizado (senão a tool_call não casaria).
	reg := NewRegistry()
	reg.Register(Tool{Name: "memoria/antiga"})
	if reg.Find("memoria_antiga") == nil {
		t.Error("registro deveria normalizar o nome da tool")
	}
}

func TestRegisterDeduplicatesToolNames(t *testing.T) {
	reg := NewRegistry()
	reg.Register(Tool{Name: "memory/list"})
	reg.Register(Tool{Name: "memory_list"}) // colide depois da normalização
	reg.Register(Tool{Name: "memory_list"})
	tools := reg.Tools()
	if len(tools) != 3 {
		t.Fatalf("tools registradas = %d, want 3", len(tools))
	}
	if tools[0].Name != "memory_list" {
		t.Errorf("primeira tool = %q, want memory_list", tools[0].Name)
	}
	if tools[1].Name == tools[0].Name || tools[2].Name == tools[0].Name || tools[1].Name == tools[2].Name {
		t.Fatalf("nomes duplicados: %q %q %q", tools[0].Name, tools[1].Name, tools[2].Name)
	}
	for _, tool := range tools {
		if !providerToolNamePattern.MatchString(tool.Name) {
			t.Errorf("nome invalido apos dedupe: %q", tool.Name)
		}
		if reg.Find(tool.Name) == nil {
			t.Errorf("Find(%q) falhou", tool.Name)
		}
	}
}
