package automation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetRuntimeCustomFields_Success(t *testing.T) {
	fields := []RuntimeCustomField{
		{DefinitionID: "def-1", Name: "teamviewer_id", Label: "TeamViewer ID", ScopeType: FieldScopeAgent, ValueJson: json.RawMessage(`"TV123"`), IsSecret: false},
		{DefinitionID: "def-2", Name: "api_key", Label: "API Key", ScopeType: FieldScopeAgent, ValueJson: json.RawMessage(`"secret-value"`), IsSecret: true},
	}
	body, _ := json.Marshal(fields)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/api/v1/agent-auth/me/custom-fields/runtime") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		// Valida headers obrigatórios
		if r.Header.Get("Authorization") == "" {
			t.Error("Authorization header ausente")
		}
		if r.Header.Get("X-Agent-ID") == "" {
			t.Error("X-Agent-ID header ausente")
		}
		// Valida query params
		q := r.URL.Query()
		if q.Get("taskId") != "task-1" {
			t.Errorf("expected taskId=task-1, got %q", q.Get("taskId"))
		}
		if q.Get("scriptId") != "script-1" {
			t.Errorf("expected scriptId=script-1, got %q", q.Get("scriptId"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := NewClient(0)
	cfg := RuntimeConfig{BaseURL: srv.URL, Token: "tok", AgentID: "agent-uuid"}
	result, err := c.GetRuntimeCustomFields(context.Background(), cfg, "task-1", "script-1", "corr-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 fields, got %d", len(result))
	}
	if result[0].Name != "teamviewer_id" {
		t.Errorf("expected name teamviewer_id, got %q", result[0].Name)
	}
	if !result[1].IsSecret {
		t.Error("expected IsSecret=true for second field")
	}
}

func TestGetRuntimeCustomFields_NoQueryParams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("taskId") != "" || q.Get("scriptId") != "" {
			t.Errorf("nao deveria ter query params, got: %s", r.URL.RawQuery)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	c := NewClient(0)
	cfg := RuntimeConfig{BaseURL: srv.URL, Token: "tok", AgentID: "agent-uuid"}
	result, err := c.GetRuntimeCustomFields(context.Background(), cfg, "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty, got %d", len(result))
	}
}

func TestGetRuntimeCustomFields_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := NewClient(0)
	cfg := RuntimeConfig{BaseURL: srv.URL, Token: "bad", AgentID: "agent-uuid"}
	_, err := c.GetRuntimeCustomFields(context.Background(), cfg, "", "", "")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 error, got: %v", err)
	}
}

func TestCollectCustomFieldValue_Success(t *testing.T) {
	name := "teamviewer_id"
	taskID := "task-1"
	response := CollectedValueResponse{
		DefinitionID: "def-1",
		Name:         "teamviewer_id",
		Label:        "TeamViewer ID",
		ScopeType:    FieldScopeAgent,
		EntityID:     "entity-uuid",
		ValueJson:    json.RawMessage(`"TV999"`),
		IsSecret:     false,
	}
	respBody, _ := json.Marshal(response)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("X-Agent-ID") == "" {
			t.Error("X-Agent-ID header ausente")
		}
		if r.Header.Get("X-Correlation-Id") != "corr-collect" {
			t.Errorf("X-Correlation-Id incorreto: %q", r.Header.Get("X-Correlation-Id"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(respBody)
	}))
	defer srv.Close()

	c := NewClient(0)
	cfg := RuntimeConfig{BaseURL: srv.URL, Token: "tok", AgentID: "agent-uuid"}
	req := CollectedValueRequest{
		Name:   &name,
		Value:  json.RawMessage(`"TV999"`),
		TaskID: &taskID,
	}
	result, err := c.CollectCustomFieldValue(context.Background(), cfg, req, "corr-collect")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Name != "teamviewer_id" {
		t.Errorf("expected name teamviewer_id, got %q", result.Name)
	}
}

func TestCollectCustomFieldValue_BusinessError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":1,"message":"AllowAgentWrite is false for this field"}`))
	}))
	defer srv.Close()

	c := NewClient(0)
	cfg := RuntimeConfig{BaseURL: srv.URL, Token: "tok", AgentID: "agent-uuid"}
	name := "some_field"
	req := CollectedValueRequest{Name: &name, Value: json.RawMessage(`"val"`)}
	_, err := c.CollectCustomFieldValue(context.Background(), cfg, req, "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	writeErr, ok := err.(*ErrCustomFieldWrite)
	if !ok {
		t.Fatalf("expected *ErrCustomFieldWrite, got %T", err)
	}
	if writeErr.Code != WriteErrorNotAllowed {
		t.Errorf("expected WriteErrorNotAllowed, got %d", writeErr.Code)
	}
}

func TestParseCustomFieldWriteError_Heuristic(t *testing.T) {
	cases := []struct {
		body     string
		expected CustomFieldWriteErrorCode
	}{
		{`{"message":"not found"}`, WriteErrorNotFound},
		{`{"message":"scope restriction"}`, WriteErrorScopeRestriction},
		{`{"message":"definition is inactive"}`, WriteErrorInactive},
		{`{"message":"allow write is false"}`, WriteErrorNotAllowed},
		{`{"message":"context not authorized"}`, WriteErrorContextDenied},
		{`{"message":"unknown error xyz"}`, WriteErrorUnknown},
	}

	for _, tc := range cases {
		err := parseCustomFieldWriteError([]byte(tc.body))
		if err.Code != tc.expected {
			t.Errorf("body=%q: expected code %d, got %d (msg=%q)", tc.body, tc.expected, err.Code, err.Message)
		}
	}
}

func TestSetAutomationHeaders_AgentID(t *testing.T) {
	var capturedAgentID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAgentID = r.Header.Get("X-Agent-ID")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	c := NewClient(0)
	cfg := RuntimeConfig{BaseURL: srv.URL, Token: "tok", AgentID: "my-agent-id"}
	// SyncPolicy deve enviar X-Agent-ID
	_, _ = c.SyncPolicy(context.Background(), cfg, PolicySyncRequest{}, "corr")
	if capturedAgentID != "my-agent-id" {
		t.Errorf("SyncPolicy: X-Agent-ID esperado 'my-agent-id', got %q", capturedAgentID)
	}
}
