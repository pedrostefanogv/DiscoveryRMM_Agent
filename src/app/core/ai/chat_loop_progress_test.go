package ai

import (
	"strings"
	"testing"
)

// TestParseMultiRoundSSE_LoopProgress verifica que o evento "loop_progress"
// emitido pelo servidor (heartbeat do agent loop: round atual / máximo)
// chega ao callback onLoopProgress com os valores corretos e não altera o
// estado do loop (não gera token, tool call nem encerra o stream).
//
// Contexto: sem esse heartbeat, o frontend não distinguia "processando"
// de "travado" durante tool chains longas — ver
// DiscoveryRMM_API/docs_planejamento/AI_CHAT_AGENT_LOOP_PLAN.md.
func TestParseMultiRoundSSE_LoopProgress(t *testing.T) {
	const sse = "data: {\"type\":\"loop_progress\",\"loopRound\":2,\"loopMaxRounds\":10}\n" +
		"data: {\"type\":\"token\",\"content\":\"resposta\"}\n" +
		"data: {\"type\":\"done\",\"sessionId\":\"s1\"}"

	type progress struct {
		round, maxRounds int
	}
	var received []progress
	var pending []pendingToolCall
	var tokens []string

	sessionID, done, err := (&Service{}).parseMultiRoundSSEWithProgress(
		strings.NewReader(sse),
		func(tok string) { tokens = append(tokens, tok) },
		&pending,
		func(round, maxRounds int) { received = append(received, progress{round, maxRounds}) },
	)
	if err != nil {
		t.Fatalf("parseMultiRoundSSEWithProgress erro: %v", err)
	}
	if !done {
		t.Errorf("esperado done=true")
	}
	if sessionID != "s1" {
		t.Errorf("sessionID: esperado s1, obteve %q", sessionID)
	}
	if len(received) != 1 {
		t.Fatalf("esperado 1 evento de progresso, obteve %d", len(received))
	}
	if received[0].round != 2 || received[0].maxRounds != 10 {
		t.Errorf("progresso: esperado round=2 maxRounds=10, obteve round=%d maxRounds=%d", received[0].round, received[0].maxRounds)
	}
	if len(tokens) != 1 || tokens[0] != "resposta" {
		t.Errorf("tokens: esperado [resposta], obteve %v", tokens)
	}
	if len(pending) != 0 {
		t.Errorf("loop_progress não deve gerar tool calls, obteve %v", pending)
	}
}

// TestParseMultiRoundSSE_LoopProgressWithoutCallback verifica que o evento
// loop_progress é ignorado silenciosamente quando não há callback (chamadores
// antigos via parseMultiRoundSSE) e que campos ausentes/zero não disparam o
// callback.
func TestParseMultiRoundSSE_LoopProgressWithoutCallback(t *testing.T) {
	const sse = "data: {\"type\":\"loop_progress\",\"loopRound\":0,\"loopMaxRounds\":0}\n" +
		"data: {\"type\":\"done\",\"sessionId\":\"s1\"}"

	var pending []pendingToolCall
	callbackCalled := false

	sessionID, done, err := (&Service{}).parseMultiRoundSSEWithProgress(
		strings.NewReader(sse),
		nil,
		&pending,
		func(round, maxRounds int) { callbackCalled = true },
	)
	if err != nil {
		t.Fatalf("erro: %v", err)
	}
	if !done || sessionID != "s1" {
		t.Errorf("esperado done=true sessionID=s1, obteve done=%v sessionID=%q", done, sessionID)
	}
	if callbackCalled {
		t.Errorf("callback não deveria ser chamado com loopMaxRounds=0")
	}
}
