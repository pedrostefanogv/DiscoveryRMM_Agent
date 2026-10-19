//go:build windows

package app

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestRespawnBudgetStore(t *testing.T) {
	var s respawnBudgetStore
	s.byID = map[string]int{}

	s.reset("sess-1", 2)
	if !s.consume("sess-1") {
		t.Fatalf("1o consume deveria ser true")
	}
	if !s.consume("sess-1") {
		t.Fatalf("2o consume deveria ser true")
	}
	if s.consume("sess-1") {
		t.Fatalf("3o consume deveria ser false (esgotado)")
	}

	// Reset rearma o orcamento.
	s.reset("sess-1", 1)
	if !s.consume("sess-1") {
		t.Fatalf("consume apos reset deveria ser true")
	}

	// Sessao desconhecida nao tem orcamento.
	if s.consume("outra") {
		t.Fatalf("sessao desconhecida nao deveria ter orcamento")
	}
}

func TestWorkerLineSignalsIntentionalExit(t *testing.T) {
	cases := map[string]bool{
		remoteSessionWorkerExitMarker:                true,
		remoteSessionWorkerExitMarker + ": detalhe":  true,
		"[remote-session-worker] encerrado":          false,
		"[remote-session-worker] iniciando sessão x": false,
		"": false,
	}
	for line, want := range cases {
		if got := workerLineSignalsIntentionalExit(line); got != want {
			t.Errorf("workerLineSignalsIntentionalExit(%q) = %v, want %v", line, got, want)
		}
	}
}

// budgetActual lê o orçamento de relançamento de uma sessão (teste).
func budgetActual(id string) int {
	remoteSessionRespawnBudget.mu.Lock()
	defer remoteSessionRespawnBudget.mu.Unlock()
	return remoteSessionRespawnBudget.byID[id]
}

// TestMaybeRespawnNaoRelancaSaidaIntencional é a regressão do bug de 18/10:
// depois do usuário NEGAR o consentimento o worker encerra de forma intencional
// (marcador no stderr); o watchdog não pode relançar — relançar reabriria o
// prompt e faria o usuário negar de novo.
func TestMaybeRespawnNaoRelancaSaidaIntencional(t *testing.T) {
	remoteSessionWorkers.mu.Lock()
	remoteSessionWorkers.byID = map[string]*remoteSessionWorkerProc{}
	remoteSessionWorkers.mu.Unlock()
	remoteSessionRespawnBudget.reset("sess-intencional", maxRemoteSessionRespawns)

	drainDone := make(chan struct{})
	close(drainDone)
	w := &remoteSessionWorkerProc{
		sessionID:       "sess-intencional",
		parent:          context.Background(),
		stderrDrainDone: drainDone,
		done:            make(chan struct{}),
		handshake:       make(chan struct{}),
	}
	w.intentionalExit.Store(true)

	started := time.Now()
	w.maybeRespawn()
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("maybeRespawn demorou %s — deveria retornar imediatamente", elapsed)
	}
	if got := budgetActual("sess-intencional"); got != maxRemoteSessionRespawns {
		t.Fatalf("orçamento consumido após saída intencional: %d (want %d)", got, maxRemoteSessionRespawns)
	}
}

// TestMaybeRespawnNaoRelancaComStopping garante que o stop explícito continua
// impedindo o relançamento.
func TestMaybeRespawnNaoRelancaComStopping(t *testing.T) {
	remoteSessionWorkers.mu.Lock()
	remoteSessionWorkers.byID = map[string]*remoteSessionWorkerProc{}
	remoteSessionWorkers.mu.Unlock()
	remoteSessionRespawnBudget.reset("sess-stop", maxRemoteSessionRespawns)

	drainDone := make(chan struct{})
	close(drainDone)
	w := &remoteSessionWorkerProc{
		sessionID:       "sess-stop",
		parent:          context.Background(),
		stderrDrainDone: drainDone,
		done:            make(chan struct{}),
		handshake:       make(chan struct{}),
	}
	w.stopping.Store(true)

	w.maybeRespawn()
	if got := budgetActual("sess-stop"); got != maxRemoteSessionRespawns {
		t.Fatalf("orçamento consumido após stop explícito: %d (want %d)", got, maxRemoteSessionRespawns)
	}
}

// TestDrainStderrMarcaSaidaIntencional exercita o caminho REAL do dreno: o
// worker imprime o marcador no stderr e o watchdog passa a não relançar.
func TestDrainStderrMarcaSaidaIntencional(t *testing.T) {
	pipeRead, pipeWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	w := &remoteSessionWorkerProc{
		sessionID:       "sess-drain",
		handshake:       make(chan struct{}),
		stderrDrainDone: make(chan struct{}),
	}
	go func() {
		defer close(w.stderrDrainDone)
		w.drainStderr(pipeRead)
	}()

	// Simula o worker: handshake + falha de consentimento + marcador de saída.
	if _, err := pipeWrite.WriteString("[remote-session-worker] iniciando sessão sess-drain\n" +
		"[remote-session-worker] mensagem: acesso remoto negado: user_denied\n" +
		remoteSessionWorkerExitMarker + "\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = pipeWrite.Close()

	select {
	case <-w.stderrDrainDone:
	case <-time.After(3 * time.Second):
		t.Fatal("dreno do stderr não terminou")
	}
	if !w.intentionalExit.Load() {
		t.Fatal("marcador não foi detectado no dreno do stderr")
	}
	select {
	case <-w.handshake:
	default:
		t.Fatal("handshake não detectado")
	}
}

// TestMarkWorkerRespawnPayload garante que o replay do watchdog marca o payload
// sem mutar o mapa original (que pode ser reutilizado).
func TestMarkWorkerRespawnPayload(t *testing.T) {
	original := map[string]any{"sessionId": "sess-1", "action": "start"}
	marked := markWorkerRespawnPayload(original)
	if marked == nil {
		t.Fatal("payload marcado nil")
	}
	if respawn, _ := marked["respawn"].(bool); !respawn {
		t.Fatalf("respawn ausente no payload marcado: %#v", marked)
	}
	if _, mutated := original["respawn"]; mutated {
		t.Fatal("payload original não pode ser mutado")
	}
	if marked["sessionId"] != "sess-1" {
		t.Fatalf("sessionId perdido: %#v", marked)
	}
}
