package agentconn

import (
	"testing"
	"time"
)

// O servidor REENTREGA comandos por agente enquanto o agente não confirma
// (ex.: estava offline). Sem dedupe por CommandId, cada reentrega reexecutaria
// o comando — e um restart/uninstall rodaria várias vezes.
func TestAgentCommandDedupeKey(t *testing.T) {
	if key := agentCommandDedupeKey(natsCommandEnvelope{}); key != "" {
		t.Fatalf("envelope sem CommandId não deve gerar chave de dedupe, veio %q", key)
	}

	key := agentCommandDedupeKey(natsCommandEnvelope{CommandID: "  ABC-123  "})
	if key != "agent-cmd:abc-123" {
		t.Fatalf("chave inesperada: %q", key)
	}

	// Mesmo comando (variações de caixa/espaço) → mesma chave.
	same := agentCommandDedupeKey(natsCommandEnvelope{CommandID: "abc-123"})
	if same != key {
		t.Fatalf("chave instável: %q vs %q", key, same)
	}
}

func TestReserveAgentCommandDispatch_ExecutaUmaVezEPublicaResultadoCacheado(t *testing.T) {
	r := NewRuntime(Options{})
	key := agentCommandDedupeKey(natsCommandEnvelope{CommandID: "cmd-1"})

	cached, shouldExecute := r.reserveFanoutDispatch(key, commandAgentDedupeTTL)
	if !shouldExecute || cached != nil {
		t.Fatalf("primeira entrega deve executar e não ter resultado cacheado: %v %v", shouldExecute, cached)
	}

	// Reentrega enquanto a execução está em curso: suprime.
	if _, again := r.reserveFanoutDispatch(key, commandAgentDedupeTTL); again {
		t.Fatal("reentrega durante a execução não pode reexecutar o comando")
	}

	// Execução conclui e o resultado é cacheado.
	result := natsResultEnvelope{CommandID: "cmd-1", ExitCode: 0, Output: "ok"}
	r.completeFanoutDispatch(key, result)

	cached, shouldExecute = r.reserveFanoutDispatch(key, commandAgentDedupeTTL)
	if shouldExecute {
		t.Fatal("reentrega após o resultado não pode reexecutar o comando")
	}
	if cached == nil || cached.Output != "ok" {
		t.Fatalf("resultado cacheado deve ser republicado, veio %+v", cached)
	}
}

func TestCommandAgentDedupeTTL_CobreJanelaDeReentrega(t *testing.T) {
	// A reentrega no servidor tem janela de 24h; o dedupe precisa cobrir.
	if commandAgentDedupeTTL < 24*time.Hour {
		t.Fatalf("TTL de dedupe (%s) menor que a janela de reentrega (24h)", commandAgentDedupeTTL)
	}
}
