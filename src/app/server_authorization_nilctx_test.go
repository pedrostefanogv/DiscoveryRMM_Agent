package app

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"discovery/app/agentconfig"
	"discovery/app/coreagent"
	"discovery/app/debug"
	"discovery/app/logs"
)

// Correção de premissa (revisão): ctx nil NÃO panica no
// http.NewRequestWithContext — ele devolve o erro "net/http: nil Context".
// O teste abaixo fixa esse comportamento real para que a justificativa da
// proteção em contextOrBackground não seja reescrita com uma premissa errada.
func TestNilContextReturnsErrorNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil ctx não deve panicar; veio: %v", r)
		}
	}()
	_, err := http.NewRequestWithContext(nil, http.MethodGet, "http://127.0.0.1:1/", nil)
	if err == nil {
		t.Fatal("nil ctx precisa devolver erro")
	}
	if got := err.Error(); got != "net/http: nil Context" {
		t.Fatalf("erro inesperado para nil ctx: %q", got)
	}
}

// O bug REAL: confirmServerAuthorizationAsync passava a.ctx direto ao refresh.
// Com a.ctx nil a requisição falha com "net/http: nil Context" — a confirmação
// de liberação NUNCA acontecia, e a UI podia ficar presa na tela de espera
// mesmo depois do admin liberar o agente. contextOrBackground resolve isso.
func TestNilAppContextBreaksConfirmationButIsProtected(t *testing.T) {
	server := newConfigServer(false) // servidor já LIBERADO
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")

	newSvc := func() *agentconfig.Service {
		return agentconfig.New(agentconfig.FetchDeps{
			GetDebugConfig: func() debug.Config {
				return debug.Config{
					ApiInsecure: true, ApiServer: host, AuthToken: "mdz_test",
					AgentID: "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7",
				}
			},
		})
	}

	// (1) Com ctx nil a confirmação falha — é a premissa do bug.
	if _, err := newSvc().Fetch(nil); err == nil {
		t.Fatal("fetch com ctx nil deveria falhar (premissa do bug)")
	}

	// (2) Com o contexto protegido a confirmação funciona.
	a := &App{}
	a.Logs = coreagent.LogBuffer{Buffer: logs.New()}
	a.AgentConfigSvc = newSvc()
	if err := a.refreshAgentConfiguration(a.contextOrBackground()); err != nil {
		t.Fatalf("refresh com contextOrBackground não pode falhar: %v", err)
	}
	if a.IsServerAuthorizationPending() {
		t.Fatal("servidor respondeu autorizado: não pode continuar pendente")
	}
}

// O caminho COMPLETO da goroutine (safeGo + confirmação) precisa concluir e
// deixar o agente liberado, sem stack trace de pânico no log.
func TestConfirmServerAuthorizationAsyncCompletesWithNilAppContext(t *testing.T) {
	// Servidor JÁ LIBERADO: o objetivo é provar que a confirmação em background
	// conclui e limpa a pendência (e não que ela permanece).
	server := newConfigServer(false)
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")

	a := &App{} // a.ctx nil de propósito
	a.Logs = coreagent.LogBuffer{Buffer: logs.New()}
	a.AgentConfigSvc = agentconfig.New(agentconfig.FetchDeps{
		GetDebugConfig: func() debug.Config {
			return debug.Config{
				ApiInsecure: true, ApiServer: host, AuthToken: "mdz_test",
				AgentID: "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7",
			}
		},
	})

	// Pré-condição: o agente está pendente e a confirmação é disparada.
	a.markServerAuthorizationPending(onboardPendingReasonApproval)
	a.confirmServerAuthorizationAsync()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		joined := strings.Join(a.Logs.GetAll(), "\n")
		if strings.Contains(joined, "[PANIC]") {
			t.Fatal("a confirmação em background não pode panicar")
		}
		if !a.IsServerAuthorizationPending() {
			return // confirmação concluída: agente liberado
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("a confirmação em background não concluiu (agente preso na espera); log=%s",
		strings.Join(a.Logs.GetAll(), "\n"))
}

// contextOrBackground é o contrato que sustenta as proteções acima.
func TestContextOrBackgroundNeverNil(t *testing.T) {
	var nilApp *App
	if nilApp.contextOrBackground() == nil {
		t.Fatal("App nil precisa devolver um contexto utilizável")
	}
	if (&App{}).contextOrBackground() == nil {
		t.Fatal("App com ctx nil precisa devolver um contexto utilizável")
	}
	if got := (&App{ctx: context.Background()}).contextOrBackground(); got == nil {
		t.Fatal("App com ctx definido precisa devolvê-lo")
	}
}
