package apiclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const healthTestAgentID = "019faa6f-8eaa-7c38-9878-36cc96f9be3e"

func healthTestService(serverURL string) *Service {
	return New(Deps{GetDebugConfig: func() DebugConfig {
		return DebugConfig{
			ApiScheme: "http",
			ApiServer: strings.TrimPrefix(serverURL, "http://"),
			AuthToken: "mdz_test",
			AgentID:   healthTestAgentID,
		}
	}})
}

// O estado é otimista no boot e só cai após falhas consecutivas (threshold).
func TestRecordAPIProbeThreshold(t *testing.T) {
	s := New(Deps{})
	if !s.APIReachable() {
		t.Fatal("deve iniciar otimista (reachable=true)")
	}
	s.recordAPIProbe(errors.New("falha 1"))
	if !s.APIReachable() {
		t.Fatal("1 falha não deve derrubar (threshold=2)")
	}
	s.recordAPIProbe(errors.New("falha 2"))
	if s.APIReachable() {
		t.Fatal("2 falhas consecutivas devem marcar a API inacessível")
	}
	s.recordAPIProbe(nil)
	if !s.APIReachable() {
		t.Fatal("um sucesso deve reabrir o estado")
	}
	if _, _, streak := s.APIHealth(); streak != 0 {
		t.Fatalf("streak deveria zerar no sucesso, veio %d", streak)
	}
}

// 4xx é o servidor respondendo (credencial/rota) — NÃO derruba o agent.
func TestProbeAPI_4xxKeepsReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	s := healthTestService(srv.URL)

	if err := s.ProbeAPI(context.Background()); err != nil {
		t.Fatalf("4xx não é queda: %v", err)
	}
	if !s.APIReachable() {
		t.Fatal("4xx não pode marcar a API inacessível")
	}
}

// 5xx (servidor de pé mas quebrado) conta como queda após o threshold.
func TestProbeAPI_5xxMarksDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	s := healthTestService(srv.URL)

	_ = s.ProbeAPI(context.Background())
	_ = s.ProbeAPI(context.Background())
	if s.APIReachable() {
		t.Fatal("2 falhas 5xx consecutivas devem marcar a API inacessível")
	}
}

// Sem credenciais configuradas o probe é no-op (ausência de config não é queda).
func TestProbeAPI_SkipsWithoutCredentials(t *testing.T) {
	s := New(Deps{GetDebugConfig: func() DebugConfig { return DebugConfig{} }})
	if err := s.ProbeAPI(context.Background()); err != nil {
		t.Fatalf("sem credenciais deveria ser no-op, veio: %v", err)
	}
	if !s.APIReachable() {
		t.Fatal("sem credenciais não pode marcar offline")
	}
}
