package app

import (
	"context"
	"testing"

	"discovery/app/apiclient"
)

// API inacessível força offline mesmo com o NATS de pé (requisito:
// NATS nativo/wss OU API fora ⇒ agent offline).
func TestApplyAPIHealth_ForcesOffline(t *testing.T) {
	svc := apiclient.New(apiclient.Deps{
		GetDebugConfig: func() apiclient.DebugConfig {
			return apiclient.DebugConfig{
				ApiScheme: "http",
				ApiServer: "127.0.0.1:1", // porta fechada: connection refused imediato
				AuthToken: "mdz_test",
				AgentID:   "019faa6f-8eaa-7c38-9878-36cc96f9be3e",
			}
		},
	})
	a := &App{}
	a.ApiClientSvc = svc

	// Sem evidência de queda, o status permanece como veio.
	st := a.applyAPIHealth(AgentStatus{Connected: true, OnlineReason: "ok"})
	if !st.Connected {
		t.Fatal("sem evidência de queda o status deve permanecer online")
	}

	// Duas falhas consecutivas do probe derrubam a API.
	_ = svc.ProbeAPI(context.Background())
	_ = svc.ProbeAPI(context.Background())
	st = a.applyAPIHealth(AgentStatus{Connected: true, OnlineReason: "ok"})
	if st.Connected {
		t.Fatal("API inacessível deve forçar offline")
	}
	if st.OnlineReason != "API inacessivel" {
		t.Fatalf("motivo inesperado: %q", st.OnlineReason)
	}
}

// Sem AgentConn não há transporte → offline, independentemente da API.
func TestEffectiveConnectivity_NoTransport(t *testing.T) {
	a := &App{}
	connected, _, apiReachable := a.effectiveConnectivity()
	if connected {
		t.Fatal("sem AgentConn não pode estar online")
	}
	if !apiReachable {
		t.Fatal("API deve começar otimista (reachable=true)")
	}
}
