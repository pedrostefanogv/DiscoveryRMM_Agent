package automation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCollectorValueFromOutput(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"ultima linha nao vazia", "a\nID 123\n", "ID 123"},
		{"ignora linhas vazias no fim", "a\nID 123\n\n  \n", "ID 123"},
		{"saida de uma linha", "  so-uma  ", "so-uma"},
		{"saida vazia", "", ""},
		{"apenas espacos", "   \n\n", ""},
		{"preserva a ultima quando ha varias", "x\ny\nz", "z"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := collectorValueFromOutput(testCase.input)
			if got != testCase.want {
				t.Fatalf("collectorValueFromOutput(%q) = %q; esperado %q", testCase.input, got, testCase.want)
			}
		})
	}
}

func TestPostCollectorValue_SendsLastOutputLine(t *testing.T) {
	var gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{\"saved\":true,\"definitionId\":\"def-9\"}"))
	}))
	defer srv.Close()

	svc := NewService(func() RuntimeConfig {
		return RuntimeConfig{BaseURL: srv.URL, Token: "mdz_tok", AgentID: "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7"}
	}, func(string) {})

	task := AutomationTask{TaskID: "t1", ScriptID: "s1", CollectorFieldDefinitionID: "def-9"}
	result := ExecutionResult{Success: true, Output: "linha 1\nID DO ANYDESK: 123 456 789\n\n"}
	svc.postCollectorValue(context.Background(), svc.getConfig(), task, result, "c1")

	if !strings.Contains(gotPath, "/custom-fields/collected") {
		t.Fatalf("caminho inesperado: %q", gotPath)
	}
	if gotBody["definitionId"] != "def-9" {
		t.Errorf("definitionId = %v; esperado def-9", gotBody["definitionId"])
	}
	if gotBody["value"] != "ID DO ANYDESK: 123 456 789" {
		t.Errorf("value = %v; esperado a ultima linha", gotBody["value"])
	}
	if gotBody["taskId"] != "t1" {
		t.Errorf("taskId = %v; esperado t1", gotBody["taskId"])
	}

	// Tarefa sem coletor nao deve chamar o endpoint.
	gotPath = ""
	svc.postCollectorValue(context.Background(), svc.getConfig(), AutomationTask{TaskID: "t2"}, result, "c1")
	if gotPath != "" {
		t.Errorf("tarefa sem campo coletor nao deveria chamar o endpoint (path=%q)", gotPath)
	}
}


func TestBuildCustomFieldReportMetadata(t *testing.T) {
	cfCtx := &ExecutionCustomFieldCtx{Fields: map[string]any{"anydesk": "123", "ativo": true}}
	collected := []CollectedValueRequest{
		{Name: strPtr("anydesk"), Value: json.RawMessage("\"999\"")},
		{DefinitionID: strPtr("def-ativo")},
	}

	raw := buildCustomFieldReportMetadata(cfCtx, collected, "def-anydesk")
	var payload struct {
		Read           []string `json:"read"`
		Collected      []collectedFieldReport `json:"collected"`
		CollectorField string   `json:"collectorField"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("json invalido: %v", err)
	}

	if len(payload.Read) != 2 || payload.Read[0] != "anydesk" || payload.Read[1] != "ativo" {
		t.Errorf("read inesperado: %v", payload.Read)
	}
	if len(payload.Collected) != 1 || payload.Collected[0].Name != "anydesk" {
		t.Fatalf("collected inesperado: %v", payload.Collected)
	}
	if payload.Collected[0].Value != "999" {
		t.Errorf("valor coletado deveria aparecer truncado no relatorio: %v", payload.Collected[0].Value)
	}
	if payload.CollectorField != "def-anydesk" {
		t.Errorf("collectorField = %q", payload.CollectorField)
	}
}

func TestBuildCustomFieldReportMetadata_TruncatesAndMasksSecrets(t *testing.T) {
	secretID := "def-secret"
	cfCtx := &ExecutionCustomFieldCtx{
		Fields: map[string]any{},
		rawByID: map[string]RuntimeCustomField{
			secretID: {DefinitionID: secretID, Name: "api_key", IsSecret: true},
		},
	}
	collected := []CollectedValueRequest{
		{DefinitionID: &secretID, Value: json.RawMessage(`"segredo"`)},
	}

	raw := buildCustomFieldReportMetadata(cfCtx, collected, "")
	if strings.Contains(raw, "segredo") {
		t.Error("campo secreto nao pode ir em claro no relatorio")
	}
	if !strings.Contains(raw, "***") {
		t.Errorf("esperado mascaramento: %s", raw)
	}

	long := strings.Repeat("a", 120)
	truncated := truncateReportValue(long, 60)
	if len(truncated) != 63 {
		t.Errorf("truncateReportValue deveria cortar em 60 + reticencias, got %d", len(truncated))
	}
}

func TestMergeJSONMetadata(t *testing.T) {
	merged := mergeJSONMetadata(`{"wingetDecision":{"skip":true}}`, "customFields", `{"read":["x"]}`)

	var payload map[string]any
	if err := json.Unmarshal([]byte(merged), &payload); err != nil {
		t.Fatalf("json invalido: %v", err)
	}
	if payload["customFields"] == nil {
		t.Error("bloco customFields nao foi injetado")
	}
	if payload["wingetDecision"] == nil {
		t.Error("metadata original deve ser preservada")
	}

	// Base vazia continua funcionando.
	if got := mergeJSONMetadata("", "customFields", `{"read":[]}`); !strings.Contains(got, "customFields") {
		t.Errorf("base vazia: %q", got)
	}
}


func TestBuildExecutionNoteContent(t *testing.T) {
	task := AutomationTask{TaskID: "t1", Name: "Coletar inventário"}
	ok := ExecutionResult{Success: true, ExitCodeSet: true, ExitCode: 0, Output: "linha A\nlinha B"}
	note := buildExecutionNoteContent(task, ok)

	if !strings.Contains(note, "Tarefa: Coletar inventário") || !strings.Contains(note, "Resultado: sucesso") {
		t.Errorf("nota sem cabeçalho esperado: %q", note)
	}
	if !strings.Contains(note, "ExitCode: 0") || !strings.Contains(note, "linha B") {
		t.Errorf("nota sem exit code/saida: %q", note)
	}

	failed := buildExecutionNoteContent(task, ExecutionResult{Success: false, ExitCodeSet: true, ExitCode: 1, ErrorMessage: "boom"})
	if !strings.Contains(failed, "Resultado: falha") || !strings.Contains(failed, "Erro: boom") {
		t.Errorf("nota de falha inesperada: %q", failed)
	}
}

func TestCreateAgentNote_PostsToMeNotes(t *testing.T) {
	var gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	client := NewClient(0)
	err := client.CreateAgentNote(
		context.Background(),
		RuntimeConfig{BaseURL: srv.URL, Token: "mdz_tok", AgentID: "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7"},
		"Tarefa: x\nResultado: sucesso",
		"c1")
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if !strings.Contains(gotPath, "/agent-auth/me/notes") {
		t.Fatalf("caminho inesperado: %q", gotPath)
	}
	if gotBody["content"] != "Tarefa: x\nResultado: sucesso" {
		t.Errorf("conteudo da nota inesperado: %v", gotBody["content"])
	}
}

func strPtr(value string) *string { return &value }

func TestPostCollectorValue_NoOutputDoesNotCall(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	svc := NewService(func() RuntimeConfig {
		return RuntimeConfig{BaseURL: srv.URL, Token: "mdz_tok", AgentID: "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7"}
	}, func(string) {})

	svc.postCollectorValue(
		context.Background(),
		svc.getConfig(),
		AutomationTask{TaskID: "t1", CollectorFieldDefinitionID: "def-9"},
		ExecutionResult{Success: true, Output: "   \n"},
		"c1")

	if called {
		t.Error("sem saida nao deveria gravar o campo")
	}
}
