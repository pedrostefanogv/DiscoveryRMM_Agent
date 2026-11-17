package app

// Verificação end-to-end (sem mocks do estado interno): o App precisa sair do
// estado "aguardando liberação" exatamente como o servidor responde.
//
// Reproduz o ciclo do relato 2026-11-17:
//   1. /me/configuration diz zeroTouchPending=true  → espera de liberação;
//   2. o admin libera e o servidor responde false   → volta ao normal sozinho.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"discovery/app/agentconfig"
	"discovery/app/debug"
)

func TestRefreshAgentConfigurationTracksServerAuthorization(t *testing.T) {
	pending := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/me/configuration") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if pending {
			_, _ = w.Write([]byte(`{"agentId":"8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7","clientId":"c1","siteId":"s1","zeroTouchPending":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"agentId":"8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7","clientId":"c1","siteId":"s1","zeroTouchPending":false}`))
	}))
	defer server.Close()

	a := &App{ctx: context.Background()}
	// O agente já recebeu o token (cenário do relato); sem isso o modo
	// correto seria "aguardando provisionamento".
	prev := hasServerCredentials
	hasServerCredentials = func() bool { return true }
	t.Cleanup(func() { hasServerCredentials = prev })
	host := strings.TrimPrefix(server.URL, "http://")
	a.AgentConfigSvc = agentconfig.New(agentconfig.FetchDeps{
		GetDebugConfig: func() debug.Config {
			// ApiInsecure=true porque o httptest local fala HTTP simples; o
			// scheme efetivo é derivado dele (config.APIScheme()).
			return debug.Config{
				ApiInsecure: true, ApiServer: host, AuthToken: "mdz_test",
				AgentID: "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7",
			}
		},
	})

	// 1. Servidor diz que o agente aguarda liberação.
	if err := a.refreshAgentConfiguration(context.Background()); err != nil {
		t.Fatalf("refresh com agente pendente não pode falhar: %v", err)
	}
	if !a.IsServerAuthorizationPending() {
		t.Fatal("zeroTouchPending=true precisa colocar o agente em 'aguardando liberação do servidor'")
	}
	if got, _ := a.GetOnboardingStatus()["mode"].(string); got != "awaiting-approval" {
		t.Fatalf("mode = %q, esperado 'awaiting-approval'", got)
	}

	// 2. O admin libera o agente no painel.
	pending = false
	if err := a.refreshAgentConfiguration(context.Background()); err != nil {
		t.Fatalf("refresh após a liberação falhou: %v", err)
	}
	if a.IsServerAuthorizationPending() {
		t.Fatal("zeroTouchPending=false precisa LIMPAR a espera de liberação")
	}
	if got, _ := a.GetOnboardingStatus()["mode"].(string); got != "normal" {
		t.Fatalf("após liberação, mode = %q, esperado 'normal'", got)
	}
}

// O binding consumido pela UI (IsServerAuthorizationPending) precisa ser
// nil-safe: a UI companion pode consultá-lo antes do boot completo.
func TestIsServerAuthorizationPendingNilSafe(t *testing.T) {
	var a *App
	if a.IsServerAuthorizationPending() {
		t.Fatal("App nil não pode reportar pendência")
	}
}
