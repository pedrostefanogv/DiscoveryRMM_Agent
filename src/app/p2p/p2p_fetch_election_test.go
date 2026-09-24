package p2p

import (
	"testing"
	"time"
)

// Regressão do backoff/cooldown do fetch: o re-seed chama runLocalElection
// direto e ignorava o NextAttemptUTC, re-baixando artifacts em falha ou
// recém-obtidos com sucesso.
func TestCanStartLocalElectionRespectsBackoffAndCooldown(t *testing.T) {
	now := time.Now()

	if !canStartLocalElection(&ArtifactFetchState{Status: "missing"}, now, true) {
		t.Fatal("esperava eleição permitida para artifact missing sem backoff")
	}
	if canStartLocalElection(&ArtifactFetchState{Status: "available"}, now, true) {
		t.Fatal("não esperava eleição para artifact available")
	}

	failed := &ArtifactFetchState{Status: "failed", NextAttemptUTC: now.Add(time.Minute)}
	if canStartLocalElection(failed, now, true) {
		t.Fatal("esperava eleição bloqueada dentro do backoff de falha")
	}

	cooldown := &ArtifactFetchState{Status: "missing", NextAttemptUTC: now.Add(artifactFetchSuccessCooldown)}
	if canStartLocalElection(cooldown, now, true) {
		t.Fatal("esperava eleição bloqueada dentro do cooldown pós-sucesso")
	}

	expired := &ArtifactFetchState{Status: "missing", NextAttemptUTC: now.Add(-time.Second)}
	if !canStartLocalElection(expired, now, true) {
		t.Fatal("esperava eleição liberada após o backoff expirar")
	}

	lease := &ArtifactFetchState{Status: "fetching", LeaseUntil: now.Add(time.Minute)}
	if canStartLocalElection(lease, now, true) {
		t.Fatal("esperava eleição bloqueada durante lease ativo")
	}

	if canStartLocalElection(&ArtifactFetchState{Status: "missing"}, now, false) {
		t.Fatal("esperava eleição bloqueada com host sobrecarregado")
	}
}
