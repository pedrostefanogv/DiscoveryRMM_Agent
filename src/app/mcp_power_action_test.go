package app

import (
	"strings"
	"testing"
)

// Mensagem do turno terminal do power_action: precisa dizer o que foi agendado
// e como cancelar, sem prometer acoes futuras (o turno de chat e encerrado).
func TestPowerActionTerminalMessage(t *testing.T) {
	restart := powerActionTerminalMessage("restart", 30)
	if !strings.Contains(restart, "reinicio") || !strings.Contains(restart, "30s") {
		t.Fatalf("mensagem de restart inesperada: %q", restart)
	}
	if !strings.Contains(restart, "Cancelar") {
		t.Fatalf("mensagem deve citar o botao Cancelar: %q", restart)
	}
	shutdown := powerActionTerminalMessage("shutdown", 15)
	if !strings.Contains(shutdown, "desligamento") || !strings.Contains(shutdown, "15s") {
		t.Fatalf("mensagem de shutdown inesperada: %q", shutdown)
	}
	if strings.Contains(shutdown, "reiniciado") {
		t.Fatalf("shutdown nao pode dizer que sera reiniciado: %q", shutdown)
	}
}

func TestPowerActionWords(t *testing.T) {
	if w, d := powerActionWords("restart"); w != "reinicio" || d != "reiniciado" {
		t.Fatalf("restart: %q/%q", w, d)
	}
	if w, d := powerActionWords("shutdown"); w != "desligamento" || d != "desligado" {
		t.Fatalf("shutdown: %q/%q", w, d)
	}
	// Acao desconhecida cai no texto de reinicio (nao deve gerar panic).
	if w, _ := powerActionWords(""); w != "reinicio" {
		t.Fatalf("fallback inesperado: %q", w)
	}
}
