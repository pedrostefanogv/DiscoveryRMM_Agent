package support

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"discovery/app/debug"
)

const formSchemaDepartmentID = "44444444-4444-4444-4444-444444444444"

// O formulário de abertura é montado pelo servidor: o agent só repassa o que
// vier do endpoint por departamento (campos, visibilidade, valor padrão e
// opções). Este teste fixa a rota, o parse e a normalização das listas.
func TestGetTicketDepartmentFormSchema_ParsesServerFields(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization") + "|" + r.Header.Get("X-Agent-Id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"departmentId": "` + formSchemaDepartmentID + `",
			"fields": [
				{
					"key": "category",
					"label": "Categoria",
					"type": "select",
					"required": false,
					"visible": true,
					"options": [
						{"value": "Rede", "label": "Rede"},
						{"value": "Impressora", "label": "Impressora"}
					]
				},
				{
					"key": "priority",
					"label": "Prioridade",
					"type": "select",
					"required": false,
					"defaultValue": "2",
					"options": null
				},
				{
					"key": "campoOculto",
					"label": "Campo oculto",
					"type": "select",
					"required": false,
					"visible": false
				}
			]
		}`))
	}))
	t.Cleanup(srv.Close)

	svc := NewService(Options{
		DebugConfig: func() debug.Config {
			return debug.Config{
				ApiScheme: "http",
				ApiServer: strings.TrimPrefix(srv.URL, "http://"),
				AuthToken: "mdz_test",
				AgentID:   lifecycleAgentID,
			}
		},
	})

	schema, err := svc.GetTicketDepartmentFormSchema(formSchemaDepartmentID)
	if err != nil {
		t.Fatalf("schema do departamento falhou: %v", err)
	}
	if !strings.HasSuffix(gotPath, "/api/v1/agent-auth/me/tickets/departments/"+formSchemaDepartmentID+"/form-schema") {
		t.Fatalf("rota inesperada: %q", gotPath)
	}
	if !strings.Contains(gotAuth, "mdz_test") || !strings.Contains(gotAuth, lifecycleAgentID) {
		t.Fatalf("headers de auth ausentes: %q", gotAuth)
	}
	if schema.DepartmentID != formSchemaDepartmentID {
		t.Fatalf("departmentId inesperado: %q", schema.DepartmentID)
	}
	if len(schema.Fields) != 3 {
		t.Fatalf("campos inesperados: %d", len(schema.Fields))
	}
	if schema.Fields[0].Options[1].Value != "Impressora" || schema.Fields[0].Options[1].Label != "Impressora" {
		t.Fatalf("opções do campo category não parseadas: %+v", schema.Fields[0].Options)
	}
	if schema.Fields[1].DefaultValue != "2" {
		t.Fatalf("defaultValue da prioridade inesperado: %q", schema.Fields[1].DefaultValue)
	}
	if schema.Fields[1].Options == nil {
		t.Fatalf("options nulas deveriam ser normalizadas para []")
	}
	// Campo que o servidor NÃO marcou não pode desaparecer por zero value.
	if !schema.Fields[1].IsVisible() {
		t.Fatalf("campo sem visible explícito deveria ser visível")
	}
	if schema.Fields[2].IsVisible() {
		t.Fatalf("campo com visible=false não pode ser marcado como visível")
	}
}

// Departamento fora do formato GUID não gera chamada HTTP.
func TestGetTicketDepartmentFormSchema_RejectsInvalidDepartment(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	t.Cleanup(srv.Close)

	svc := NewService(Options{
		DebugConfig: func() debug.Config {
			return debug.Config{
				ApiScheme: "http",
				ApiServer: strings.TrimPrefix(srv.URL, "http://"),
				AuthToken: "mdz_test",
				AgentID:   lifecycleAgentID,
			}
		},
	})

	if _, err := svc.GetTicketDepartmentFormSchema("nao-e-guid"); err == nil {
		t.Fatalf("departmentId inválido deveria falhar")
	}
	if called {
		t.Fatalf("departmentId inválido não pode gerar chamada HTTP")
	}
}
