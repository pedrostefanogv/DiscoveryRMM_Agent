package decommission

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestPerformDeleteTreatsGoneAgentsAsSuccess cobre o contrato do DELETE remoto
// de descomissionamento: 404/410 = registro já removido e 401/403 = token
// revogado (descomissionamento iniciado pelo painel) são estados TERMINAIS —
// insistir só geraria retry inútil no outbox. 5xx continua sendo falha.
func TestPerformDeleteTreatsGoneAgentsAsSuccess(t *testing.T) {
	cases := []struct {
		status  int
		wantErr bool
	}{
		{http.StatusOK, false},
		{http.StatusNoContent, false},
		{http.StatusNotFound, false},
		{http.StatusGone, false},
		{http.StatusUnauthorized, false},
		{http.StatusForbidden, false},
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
	}

	for _, tc := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete {
				t.Errorf("esperado DELETE, recebido %s", r.Method)
			}
			if r.URL.Path != "/api/v1/agent-auth/me" {
				t.Errorf("path inesperado: %s", r.URL.Path)
			}
			w.WriteHeader(tc.status)
		}))

		parsed, err := url.Parse(server.URL)
		if err != nil {
			t.Fatalf("parse url: %v", err)
		}

		target := Target{
			Scheme:  parsed.Scheme,
			Server:  parsed.Host,
			Token:   "mdz_test_token",
			AgentID: "11111111-1111-1111-1111-111111111111",
		}

		deleteErr := PerformDelete(context.Background(), target)
		server.Close()

		if tc.wantErr && deleteErr == nil {
			t.Fatalf("HTTP %d: esperado erro, obtido nil", tc.status)
		}
		if !tc.wantErr && deleteErr != nil {
			t.Fatalf("HTTP %d: erro inesperado: %v", tc.status, deleteErr)
		}
	}
}
