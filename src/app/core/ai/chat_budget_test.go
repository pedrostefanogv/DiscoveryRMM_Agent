package ai

import (
	"fmt"
	"strings"
	"testing"
)

// O agent precisa SURFACAR o orçamento de rounds do servidor para a UI: sem esse
// sinal o usuário não entende por que a ação clicada ainda não executou (caso
// YogaDNS, 2026-10-08: "não consegui concluir" sem nenhum aviso no chat).
func TestParseMultiRoundSSE_BudgetEventsReachObserver(t *testing.T) {
	s := &Service{}
	var got []string
	s.SetBudgetObserver(func(state, message string, round, maxRounds int) {
		got = append(got, fmt.Sprintf("%s|%s|%d|%d", state, message, round, maxRounds))
	})

	sse := strings.Join([]string{
		`data: {"type":"budget_exhausted","content":"Limite de rounds atingido (3/3)","loopRound":3,"loopMaxRounds":3}`,
		"",
		`data: {"type":"budget_renewed","content":"Orçamento renovado","loopMaxRounds":3}`,
		"",
	}, "\n")

	var pending []pendingToolCall
	if _, _, err := s.parseMultiRoundSSE(strings.NewReader(sse), nil, &pending); err != nil {
		t.Fatalf("parseMultiRoundSSE erro: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("esperado 2 eventos de orçamento, obtido %v", got)
	}
	if !strings.HasPrefix(got[0], "exhausted|Limite de rounds atingido (3/3)|3|3") {
		t.Fatalf("evento 1 inesperado: %q", got[0])
	}
	if !strings.HasPrefix(got[1], "renewed|Orçamento renovado|0|3") {
		t.Fatalf("evento 2 inesperado: %q", got[1])
	}
}

// Sem observador registrado (headless/testes) o parser não pode quebrar.
func TestParseMultiRoundSSE_BudgetWithoutObserver(t *testing.T) {
	s := &Service{}
	sse := "data: {\"type\":\"budget_exhausted\",\"content\":\"x\",\"loopRound\":1,\"loopMaxRounds\":3}\n\n"
	var pending []pendingToolCall
	if _, _, err := s.parseMultiRoundSSE(strings.NewReader(sse), nil, &pending); err != nil {
		t.Fatalf("sem observador não deveria falhar: %v", err)
	}
}
