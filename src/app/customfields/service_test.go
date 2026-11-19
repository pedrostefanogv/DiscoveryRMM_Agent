package customfields

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testAgentID = "11111111-2222-3333-4444-555555555555"

func newTestServerConfig(rawURL, token, agentID string) DebugConfig {
	host := strings.TrimPrefix(rawURL, "http://")
	return DebugConfig{
		ApiScheme: "http",
		ApiServer: host,
		AuthToken: token,
		AgentID:   agentID,
	}
}

func TestUpsertCollectedCustomField_Success(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotAgentID, gotContentType string
	var gotReq UpsertCollectedCustomFieldRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAgentID = r.Header.Get("X-Agent-ID")
		gotContentType = r.Header.Get("Content-Type")

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(body, &gotReq); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	svc := New(Deps{
		GetDebugConfig: func() DebugConfig {
			return newTestServerConfig(srv.URL, "mdz_secret_token_abc", testAgentID)
		},
	})

	err := svc.UpsertCollectedCustomField(context.Background(), "  asset_tag ", " WIN-42 ", " agent ")
	if err != nil {
		t.Fatalf("UpsertCollectedCustomField failed: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/v1/agent-auth/me/custom-fields/collected" {
		t.Errorf("path = %q, want /api/v1/agent-auth/me/custom-fields/collected", gotPath)
	}
	if gotAuth != "Bearer mdz_secret_token_abc" {
		t.Errorf("Authorization = %q, want Bearer mdz_secret_token_abc", gotAuth)
	}
	if gotAgentID != testAgentID {
		t.Errorf("X-Agent-ID = %q, want %q", gotAgentID, testAgentID)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotReq.Name != "asset_tag" || gotReq.Value != "WIN-42" || gotReq.Scope != "agent" {
		t.Errorf("decoded payload = %+v, want trimmed fields", gotReq)
	}
}

func TestUpsertCollectedCustomField_AuthValidationErrors(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cases := []struct {
		name    string
		token   string
		agentID string
	}{
		{"empty token", "", testAgentID},
		{"token without mdz prefix", "invalid_token", testAgentID},
		{"invalid agent id", "mdz_valid", "not-a-guid"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called = false
			svc := New(Deps{
				GetDebugConfig: func() DebugConfig {
					return newTestServerConfig(srv.URL, tc.token, tc.agentID)
				},
			})
			err := svc.UpsertCollectedCustomField(context.Background(), "k", "v", "agent")
			if err == nil {
				t.Fatal("expected auth validation error, got nil")
			}
			if called {
				t.Fatal("HTTP server should not be called when auth headers fail validation")
			}
		})
	}
}

func TestUpsertCollectedCustomField_HTTPErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("  campo invalido  \n"))
	}))
	defer srv.Close()

	svc := New(Deps{
		GetDebugConfig: func() DebugConfig {
			return newTestServerConfig(srv.URL, "mdz_valid_token", testAgentID)
		},
	})

	err := svc.UpsertCollectedCustomField(context.Background(), "k", "v", "agent")
	if err == nil {
		t.Fatal("expected error for HTTP 400")
	}
	if !strings.Contains(err.Error(), "HTTP 400") || !strings.Contains(err.Error(), "campo invalido") {
		t.Fatalf("unexpected error string: %v", err)
	}
}

func TestUpsertCollectedCustomField_CancelledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	svc := New(Deps{
		GetDebugConfig: func() DebugConfig {
			return newTestServerConfig(srv.URL, "mdz_valid_token", testAgentID)
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := svc.UpsertCollectedCustomField(ctx, "k", "v", "agent")
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}
