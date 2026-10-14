package ai

import (
	"strings"
	"testing"
)

func TestTrimHistoryByBytesLockedCutsOldestFirst(t *testing.T) {
	s := &Service{}
	big := strings.Repeat("a", 3<<20) // 3 MB por mensagem (data URL base64)
	for i := 0; i < 5; i++ {
		appendHistoryLocked(s, Message{Role: "user", Content: big + string(rune('0'+i))})
	}

	total := historySizeLocked(s.history)
	if total > maxHistoryBytes {
		t.Fatalf("histórico com %d bytes, acima do teto %d", total, maxHistoryBytes)
	}
	if len(s.history) == 0 {
		t.Fatal("histórico não deveria ficar vazio")
	}
	// A mensagem mais recente precisa continuar presente.
	last := s.history[len(s.history)-1].Content
	if !strings.HasSuffix(last, "4") {
		t.Fatalf("última mensagem perdeu a marca esperada: sufixo = %q", last[len(last)-1:])
	}
}

func TestTrimHistoryByBytesLockedKeepsAtLeastOneMessage(t *testing.T) {
	s := &Service{}
	huge := strings.Repeat("b", maxHistoryBytes+1)
	appendHistoryLocked(s, Message{Role: "user", Content: huge})
	if len(s.history) != 1 {
		t.Fatalf("esperava manter a única mensagem, got %d", len(s.history))
	}
}

func TestTrimHistoryByBytesLockedNoopUnderBudget(t *testing.T) {
	s := &Service{}
	appendHistoryLocked(s, Message{Role: "user", Content: "oi"})
	appendHistoryLocked(s, Message{Role: "assistant", Content: "olá"})
	if len(s.history) != 2 {
		t.Fatalf("histórico pequeno não deveria ser cortado, got %d", len(s.history))
	}
}

func TestMessageSizeCountsToolCalls(t *testing.T) {
	m := Message{
		Role:    "assistant",
		Content: strings.Repeat("x", 10),
		ToolCalls: []ToolCall{{
			ID: "1", Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "ping", Arguments: strings.Repeat("y", 20)},
		}},
	}
	if got := messageSize(m); got < 30 {
		t.Fatalf("messageSize = %d, esperava somar conteúdo + tool call", got)
	}
}
