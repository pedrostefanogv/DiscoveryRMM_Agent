package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"discovery/app/agentconfig"
	"discovery/app/coreagent"
	"discovery/app/debug"
	"discovery/app/logs"
)

// BUG (revisão): confirmServerAuthorizationAsync passava a.ctx direto para o
// refresh. Com a.ctx nil (processo ainda não inicializado / testes), o
// http.NewRequestWithContext PANICA. O safeGo recupera o pânico (o processo não
// cai — verificado em core/safego), mas a confirmação se perde e o log recebe
// um stack trace a cada 403. Aqui a proteção é verificada de forma DIRETA, no
// mesmo caminho que a goroutine executa.
func TestRefreshAgentConfigurationSurvivesNilContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"agentId":"8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7","clientId":"c1","siteId":"s1","zeroTouchPending":false}`))
	}))
	defer server.Close()

	a := &App{} // a.ctx NIL de propósito
	host := strings.TrimPrefix(server.URL, "http://")
	a.AgentConfigSvc = agentconfig.New(agentconfig.FetchDeps{
		GetDebugConfig: func() debug.Config {
			return debug.Config{
				ApiInsecure: true, ApiServer: host, AuthToken: "mdz_test",
				AgentID: "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7",
			}
		},
	})

	// contextOrBackground é o que protege a goroutine: nunca devolve nil.
	if a.contextOrBackground() == nil {
		t.Fatal("contextOrBackground nunca pode devolver nil")
	}

	// O caminho exato da goroutine, mas com o contexto protegido: não panica e
	// ainda confirma a autorização (prova que o contexto é utilizável de fato).
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("refresh com a.ctx nil não pode entrar em pânico: %v", r)
		}
	}()
	if err := a.refreshAgentConfiguration(a.contextOrBackground()); err != nil {
		t.Fatalf("refresh deveria ter sucesso com o contexto protegido: %v", err)
	}
	if a.IsServerAuthorizationPending() {
		t.Fatal("servidor respondeu autorizado: não pode ficar pendente")
	}
}

// E o caminho REAL da goroutine (via confirmServerAuthorizationAsync) também
// não pode deixar stack trace de pânico no log.
func TestConfirmServerAuthorizationAsyncDoesNotPanicWithNilContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"agentId":"8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7","clientId":"c1","siteId":"s1","zeroTouchPending":true}`))
	}))
	defer server.Close()

	a := &App{}
	// Buffer real: é nele que o safeGo escreve o stack trace de um pânico.
	a.Logs = coreagent.LogBuffer{Buffer: logs.New()}
	host := strings.TrimPrefix(server.URL, "http://")
	a.AgentConfigSvc = agentconfig.New(agentconfig.FetchDeps{
		GetDebugConfig: func() debug.Config {
			return debug.Config{
				ApiInsecure: true, ApiServer: host, AuthToken: "mdz_test",
				AgentID: "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7",
			}
		},
	})

	a.confirmServerAuthorizationAsync()

	// Dá tempo da goroutine rodar e verifica que NENHUM pânico foi logado.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(strings.Join(a.Logs.GetAll(), "\n"), "[PANIC]") {
			t.Fatal("a confirmação em background panicou com a.ctx nil")
		}
		if a.IsServerAuthorizationPending() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if strings.Contains(strings.Join(a.Logs.GetAll(), "\n"), "[PANIC]") {
		t.Fatal("a confirmação em background panicou com a.ctx nil")
	}
	_ = context.Background()
}
