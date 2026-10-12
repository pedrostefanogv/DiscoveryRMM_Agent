package automation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A API responde {"fields":[...]} com scopeType string e valueJson duplamente
// codificado. O agent tem de continuar enxergando os campos — antes a resposta
// era descartada silenciosamente como lista vazia.
func TestGetRuntimeCustomFields_AcceptsObjectWrapperAndStringScope(t *testing.T) {
	body := `{"fields":[{"definitionId":"def-1","name":"anydesk","label":"anydeskid","scopeType":"Agent","valueJson":"\"1602799292\"","isSecret":false},{"definitionId":"def-2","name":"sitefield","label":"Site","scopeType":2,"valueJson":42,"isSecret":true}]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := NewClient(0)
	cfg := RuntimeConfig{BaseURL: srv.URL, Token: "mdz_tok", AgentID: "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7"}
	result, err := c.GetRuntimeCustomFields(context.Background(), cfg, "t1", "s1", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("esperado 2 campos, obtido %d", len(result))
	}
	if result[0].ScopeType != FieldScopeAgent {
		t.Errorf("scopeType = %d; esperado Agent", result[0].ScopeType)
	}
	if got := string(result[0].ValueJson); got != "\"1602799292\"" {
		t.Errorf("valueJson = %s; esperado \"1602799292\"", got)
	}
	if result[1].ScopeType != FieldScopeSite {
		t.Errorf("scopeType = %d; esperado Site", result[1].ScopeType)
	}
	if got := string(result[1].ValueJson); got != "42" {
		t.Errorf("valueJson = %s; esperado 42", got)
	}
}

// O formato canonico (array + scopeType numerico + valueJson bruto) segue valendo.
func TestGetRuntimeCustomFields_AcceptsCanonicalArray(t *testing.T) {
	fields := []RuntimeCustomField{
		{DefinitionID: "def-1", Name: "anydesk", Label: "anydeskid", ScopeType: FieldScopeAgent, ValueJson: json.RawMessage("\"1602799292\""), IsSecret: false},
	}
	body, _ := json.Marshal(fields)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := NewClient(0)
	cfg := RuntimeConfig{BaseURL: srv.URL, Token: "mdz_tok", AgentID: "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7"}
	result, err := c.GetRuntimeCustomFields(context.Background(), cfg, "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("esperado 1 campo, obtido %d", len(result))
	}
	if got := string(result[0].ValueJson); got != "\"1602799292\"" {
		t.Errorf("valueJson = %s", got)
	}
}

func TestFieldScopeType_UnmarshalJSON_StringAndNumber(t *testing.T) {
	var scope FieldScopeType
	if err := json.Unmarshal([]byte("\"Agent\""), &scope); err != nil || scope != FieldScopeAgent {
		t.Fatalf("Agent: err=%v scope=%d", err, scope)
	}
	if err := json.Unmarshal([]byte("2"), &scope); err != nil || scope != FieldScopeSite {
		t.Fatalf("2: err=%v scope=%d", err, scope)
	}
	if err := json.Unmarshal([]byte("\"Bogus\""), &scope); err == nil {
		t.Fatalf("esperava erro para scopeType desconhecido")
	}
}

func TestNormalizeRuntimeValueJSON(t *testing.T) {
	if got := string(normalizeRuntimeValueJSON(json.RawMessage("\"\\\"1602799292\\\"\""))); got != "\"1602799292\"" {
		t.Errorf("duplamente codificado: got %s", got)
	}
	if got := string(normalizeRuntimeValueJSON(json.RawMessage("\"abc\""))); got != "\"abc\"" {
		t.Errorf("string simples: got %s", got)
	}
	if got := string(normalizeRuntimeValueJSON(json.RawMessage("42"))); got != "42" {
		t.Errorf("numero: got %s", got)
	}
}
