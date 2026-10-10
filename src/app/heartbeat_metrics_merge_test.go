package app

import (
	"testing"
	"time"

	"discovery/app/core/agentconn"
)

// TestMergeHeartbeatMetrics_CopiesLoggedUser é a regressão do bug em que
// getHeartbeatMetrics remonta o struct e o merge descartava o usuário logado,
// fazendo o heartbeat real sair sem o campo.
func TestMergeHeartbeatMetrics_CopiesLoggedUser(t *testing.T) {
	since := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	dst := &agentconn.AgentHeartbeatMetrics{}
	src := &agentconn.AgentHeartbeatMetrics{LoggedUser: "CORP\\pedro", LoggedUserSince: since}

	mergeHeartbeatMetrics(dst, src)

	if dst.LoggedUser != "CORP\\pedro" {
		t.Fatalf("LoggedUser = %q, want CORP\\pedro", dst.LoggedUser)
	}
	if !dst.LoggedUserSince.Equal(since) {
		t.Fatalf("LoggedUserSince = %v, want %v", dst.LoggedUserSince, since)
	}
}

// Sem sessão o merge deve preservar o vazio do destino (o heartbeat envia ""
// de propósito, sinalizando "agent novo sem sessão").
func TestMergeHeartbeatMetrics_KeepsEmptyLoggedUser(t *testing.T) {
	dst := &agentconn.AgentHeartbeatMetrics{}
	mergeHeartbeatMetrics(dst, &agentconn.AgentHeartbeatMetrics{})

	if dst.LoggedUser != "" {
		t.Fatalf("LoggedUser = %q, want empty", dst.LoggedUser)
	}
}
