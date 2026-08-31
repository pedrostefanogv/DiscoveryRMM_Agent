package ai

import (
	"strings"
	"testing"
)

func TestSanitizeAssistantText_DSMLBlock(t *testing.T) {
	in := "Perfeito! Vou fazer um diagnóstico geral. Só um instante...\n\n<｜DSML｜tool_invokes>\n<invoke name=\"get_inventory\">\n<parameter name=\"agentId\">019faa6f</parameter>\n</invoke>\n</｜DSML｜tool_invokes>"
	clean, removed := sanitizeAssistantText(in)
	if !removed {
		t.Fatal("esperado removed=true para bloco DSML")
	}
	if strings.Contains(clean, "DSML") || strings.Contains(clean, "invoke") {
		t.Fatalf("vazamento DSML não removido: %q", clean)
	}
	if !strings.Contains(clean, "diagnóstico geral") {
		t.Fatalf("texto útil perdido: %q", clean)
	}
}

func TestSanitizeAssistantText_DSMLAsciiPipe(t *testing.T) {
	in := "Texto antes <|DSML|tool_invokes><invoke name=\"x\"/></|DSML|tool_invokes> texto depois"
	clean, removed := sanitizeAssistantText(in)
	if !removed {
		t.Fatal("esperado removed=true")
	}
	if strings.Contains(clean, "DSML") || strings.Contains(clean, "invoke") {
		t.Fatalf("vazamento não removido: %q", clean)
	}
	if !strings.Contains(clean, "Texto antes") || !strings.Contains(clean, "texto depois") {
		t.Fatalf("texto útil perdido: %q", clean)
	}
}

func TestSanitizeAssistantText_JsonInvokeArray(t *testing.T) {
	in := "Vou buscar o contexto.\n\n```json\n[\n  {\"name\": \"memory.search\", \"arguments\": {\"query\": \"perfil\"}}\n]\n```\n\nVou olhar o inventário."
	clean, removed := sanitizeAssistantText(in)
	if !removed {
		t.Fatal("esperado removed=true para bloco json com invokes")
	}
	if strings.Contains(clean, "memory.search") || strings.Contains(clean, "```json") {
		t.Fatalf("vazamento json não removido: %q", clean)
	}
	if !strings.Contains(clean, "Vou buscar o contexto") || !strings.Contains(clean, "Vou olhar o inventário") {
		t.Fatalf("texto útil perdido: %q", clean)
	}
}

func TestSanitizeAssistantText_JsonA2uiAction(t *testing.T) {
	in := "Vou verificar o que sei sobre você.\n\n```json{\"version\":\"a2ui\",\"action\":\"search\",\"query\":\"perfil\"}```\n\nOlá! Como posso ajudar?"
	clean, removed := sanitizeAssistantText(in)
	if !removed {
		t.Fatal("esperado removed=true para ação A2UI em bloco json")
	}
	if strings.Contains(clean, "a2ui") {
		t.Fatalf("vazamento a2ui não removido: %q", clean)
	}
	if !strings.Contains(clean, "Como posso ajudar") {
		t.Fatalf("texto útil perdido: %q", clean)
	}
}

func TestSanitizeAssistantText_KeepsNormalJsonFence(t *testing.T) {
	in := "Aqui está um exemplo:\n\n```json\n{\"status\": \"ok\", \"count\": 3}\n```\n\nFim."
	clean, removed := sanitizeAssistantText(in)
	if removed {
		t.Fatalf("bloco json legítimo não deveria ser removido: %q", clean)
	}
	if !strings.Contains(clean, "\"status\"") {
		t.Fatalf("bloco json legítimo perdido: %q", clean)
	}
}

func TestSanitizeAssistantText_OnlyLeak(t *testing.T) {
	in := "<｜DSML｜tool_invokes><invoke name=\"get_inventory\"></invoke></｜DSML｜tool_invokes>"
	clean, removed := sanitizeAssistantText(in)
	if !removed {
		t.Fatal("esperado removed=true")
	}
	if clean != "" {
		t.Fatalf("esperado texto vazio quando só havia vazamento, obteve: %q", clean)
	}
}

func TestSanitizeAssistantText_NoLeak(t *testing.T) {
	in := "Resposta normal com **markdown** e `código`.\n\n- item 1\n- item 2"
	clean, removed := sanitizeAssistantText(in)
	if removed {
		t.Fatal("texto limpo não deveria ser modificado")
	}
	if clean != in {
		t.Fatalf("texto limpo alterado: %q", clean)
	}
}
