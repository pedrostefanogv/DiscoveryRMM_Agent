package netutil

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeAgentToken(t *testing.T) {
	valid := "mdz_abc123"
	if got, err := NormalizeAgentToken(valid); err != nil || got != valid {
		t.Fatalf("NormalizeAgentToken(%q) = %q, %v; esperado %q, nil", valid, got, err, valid)
	}
	if got, err := NormalizeAgentToken("  " + valid + "  "); err != nil || got != valid {
		t.Fatalf("NormalizeAgentToken com espaços externos = %q, %v", got, err)
	}
	for _, tc := range []struct {
		in   string
		want string // substring esperada no erro
	}{
		{"", "ausente"},
		{"Bearer mdz_abc", "Bearer"},
		{"mdz_abc mdz_def", "espaços"},
		{"abc_sem_prefixo", "mdz_"},
	} {
		if _, err := NormalizeAgentToken(tc.in); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("NormalizeAgentToken(%q) esperava erro contendo %q, obteve %v", tc.in, tc.want, err)
		}
	}
}

func TestNormalizeAgentID(t *testing.T) {
	valid := "11111111-1111-1111-1111-111111111111"
	if got, err := NormalizeAgentID(valid); err != nil || got != valid {
		t.Fatalf("NormalizeAgentID(%q) = %q, %v", valid, got, err)
	}
	// Case-insensitive é aceito e devolvido como veio (canônico na origem).
	if _, err := NormalizeAgentID("ABCDEF01-2345-6789-ABCD-EF0123456789"); err != nil {
		t.Fatalf("NormalizeAgentID GUID uppercase falhou: %v", err)
	}
	for _, invalid := range []string{"", "nao-e-guid", "11111111", "zzzzzzzz-zzzz-zzzz-zzzz-zzzzzzzzzzzz"} {
		if _, err := NormalizeAgentID(invalid); err == nil {
			t.Fatalf("NormalizeAgentID(%q) deveria falhar", invalid)
		}
	}
}

func TestSetAgentAuthHeaders(t *testing.T) {
	req := httptest.NewRequest("POST", "https://api.test/x", nil)
	if err := SetAgentAuthHeadersWithAgentID(req, "mdz_tok", "11111111-1111-1111-1111-111111111111"); err != nil {
		t.Fatalf("SetAgentAuthHeadersWithAgentID: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer mdz_tok" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := req.Header.Get("X-Agent-ID"); got != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("X-Agent-ID = %q", got)
	}
	req2 := httptest.NewRequest("POST", "https://api.test/x", nil)
	if err := SetAgentAuthHeadersWithAgentID(req2, "mdz_tok", "id-invalido"); err == nil {
		t.Fatal("X-Agent-ID inválido deveria falhar")
	}
}

func TestSanitizeHTTPErrorBody(t *testing.T) {
	if got := SanitizeHTTPErrorBody(502, "  "); !strings.Contains(got, "502") {
		t.Fatalf("corpo vazio: %q", got)
	}
	if got := SanitizeHTTPErrorBody(502, "<html><body>nginx</body></html>"); !strings.Contains(got, "intermediário") {
		t.Fatalf("HTML deveria ser saneado: %q", got)
	}
	long := strings.Repeat("x", 500)
	got := SanitizeHTTPErrorBody(500, long)
	if len(got) > 310 || !strings.HasSuffix(got, "...") {
		t.Fatalf("truncamento esperado, obteve len=%d suffix=%q", len(got), got[len(got)-6:])
	}
	if got := SanitizeHTTPErrorBody(500, "erro simples"); got != "erro simples" {
		t.Fatalf("texto simples deveria passar: %q", got)
	}
}
