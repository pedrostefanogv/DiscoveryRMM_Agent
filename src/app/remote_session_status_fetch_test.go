//go:build windows

package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// configureRemoteConsentTestRuntime aponta o endpoint/token/agentId VIVOS do
// runtime de consentimento para o servidor de teste e devolve o restore.
func configureRemoteConsentTestRuntime(apiBase string) func() {
	remoteConsentRuntimeMu.Lock()
	prevAPI, prevToken, prevID := remoteConsentAPI, remoteConsentToken, remoteConsentAgentID
	remoteConsentAPI = apiBase
	remoteConsentToken = "mdz_testtoken123456"
	remoteConsentAgentID = "019faa6f-8eaa-7c38-9878-36cc96f9be3e"
	remoteConsentRuntimeMu.Unlock()
	return func() {
		remoteConsentRuntimeMu.Lock()
		remoteConsentAPI, remoteConsentToken, remoteConsentAgentID = prevAPI, prevToken, prevID
		remoteConsentRuntimeMu.Unlock()
	}
}

// TestFetchRemoteSessionStatus cobre o GET de status usado no replay do worker:
// a resposta do servidor (status + consent.outcome) precisa chegar tipada, com
// agent-auth no header e erro em respostas não-2xx.
func TestFetchRemoteSessionStatus(t *testing.T) {
	var gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if r.Method != http.MethodGet {
			t.Errorf("método = %s, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"active","consent":{"outcome":"granted"}}`))
	}))
	defer server.Close()

	oldAPI := configureRemoteConsentTestRuntime(server.URL)
	defer oldAPI()

	status, err := fetchRemoteSessionStatus(context.Background(), "sess-123")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if status.Status != "active" || status.ConsentOutcome != "granted" {
		t.Fatalf("status = %+v", status)
	}
	if !strings.HasSuffix(gotPath, "/api/v1/agent-auth/me/remote-sessions/sess-123") {
		t.Fatalf("path = %q", gotPath)
	}
	if strings.TrimSpace(gotAuth) == "" {
		t.Fatal("header Authorization (agent-auth) ausente")
	}
}

func TestFetchRemoteSessionStatusErroHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer server.Close()

	oldAPI := configureRemoteConsentTestRuntime(server.URL)
	defer oldAPI()

	if _, err := fetchRemoteSessionStatus(context.Background(), "sess-123"); err == nil {
		t.Fatal("HTTP 409 deveria retornar erro")
	}
}
