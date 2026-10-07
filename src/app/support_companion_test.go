package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	appsupport "discovery/app/support"
)

// Sem IPC client (modo serviço/standalone) o helper deve executar o caminho
// local do SupportSvc, sem tentar RPC.
func TestSupportCompanionOrLocal_FallsBackToLocalWithoutIPC(t *testing.T) {
	a := &App{}
	called := false
	got, err := supportCompanionOrLocal(a, "support:tickets", nil, supportRPCReadTimeout, func() (int, error) {
		called = true
		return 42, nil
	})
	if err != nil {
		t.Fatalf("esperava sucesso no caminho local, veio: %v", err)
	}
	if !called {
		t.Fatal("o caminho local não foi executado")
	}
	if got != 42 {
		t.Fatalf("esperava 42, veio %d", got)
	}
}

// O erro do caminho local deve ser propagado (não engolido).
func TestSupportCompanionOrLocal_PropagatesLocalError(t *testing.T) {
	a := &App{}
	wantErr := errors.New("falha local")
	_, err := supportCompanionOrLocal(a, "support:tickets", nil, supportRPCReadTimeout, func() (int, error) {
		return 0, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("esperava %v, veio %v", wantErr, err)
	}
}

// O payload do RPC precisa reidratar CreateTicketInput (inclui customFields e
// templateAnswers, que chegam como map[string]any pelo JSON do IPC).
func TestIPCRPCDecodeCreateTicketInput(t *testing.T) {
	payload := map[string]any{
		"input": map[string]any{
			"title":             "Falha no backup",
			"description":       "Descrição",
			"priority":          float64(3),
			"category":          "Infra",
			"departmentId":      "dep-1",
			"templateId":        "tpl-1",
			"customFieldValues": map[string]any{"campo": "valor"},
			"templateAnswers":   map[string]any{"pergunta": "resposta"},
		},
	}
	var input appsupport.CreateTicketInput
	if err := ipcRPCDecodeInput(payload, "input", &input); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if input.Title != "Falha no backup" || input.Priority != 3 || input.DepartmentID != "dep-1" {
		t.Fatalf("input decodificado incorretamente: %+v", input)
	}
	if input.CustomFields["campo"] != "valor" || input.TemplateAnswers["pergunta"] != "resposta" {
		t.Fatalf("mapas opcionais perdidos: %+v", input)
	}
}

func TestIPCRPCDecodeCloseTicketInput(t *testing.T) {
	payload := map[string]any{
		"input": map[string]any{"rating": float64(5), "comment": "ok", "workflowStateId": "ws-1"},
	}
	var input appsupport.CloseTicketInput
	if err := ipcRPCDecodeInput(payload, "input", &input); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if input.Rating == nil || *input.Rating != 5 || input.Comment != "ok" || input.WorkflowStateID != "ws-1" {
		t.Fatalf("input decodificado incorretamente: %+v", input)
	}
}

// O envelope deve preservar o shape do DTO para o cliente reidratar o tipo.
func TestIPCRPCResultEnvelope(t *testing.T) {
	original := appsupport.SupportTicketList{
		Tickets:  []appsupport.APITicket{{ID: "t1", Title: "Chamado"}},
		Stale:    true,
		CachedAt: "2026-10-06T11:28:15Z",
	}
	resp := ipcRPCResult(original)
	ok, _ := resp["ok"].(bool)
	if !ok {
		t.Fatalf("envelope sem ok=true: %v", resp)
	}
	data, _ := resp["data"].(map[string]any)
	raw, err := json.Marshal(data["result"])
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var got appsupport.SupportTicketList
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(got.Tickets) != 1 || got.Tickets[0].ID != "t1" || !got.Stale || got.CachedAt != original.CachedAt {
		t.Fatalf("round-trip perdeu campos: %+v", got)
	}
}

func TestIPCRPCError(t *testing.T) {
	resp := ipcRPCError(errors.New("falha X"))
	if ok, _ := resp["ok"].(bool); ok {
		t.Fatal("erro não pode ter ok=true")
	}
	if msg, _ := resp["error"].(string); msg != "falha X" {
		t.Fatalf("erro inesperado: %v", resp)
	}
}

// Regressão do bug: os métodos de suporte/knowledge precisam estar registrados
// no dispatcher IPC. Sem SupportSvc a resposta é "serviço indisponível" — o
// que prova que o case existe (antes caía em "método desconhecido").
func TestHandleIPCRequest_SupportKnowledgeMethodsRegistered(t *testing.T) {
	methods := []string{
		"support:agent_info",
		"support:agent_info_json",
		"support:tickets",
		"support:ticket",
		"support:ticket_fields",
		"support:ticket_answers",
		"support:comments",
		"support:options",
		"support:department_fields",
		"support:workflow_states",
		"support:templates",
		"support:create",
		"support:comment_add",
		"support:comment_add_legacy",
		"support:close",
		"support:reopen",
		"support:rate",
		"support:agent_tickets",
		"support:agent_ticket",
		"support:agent_ticket_create",
		"support:agent_ticket_close",
		"support:agent_comment_add",
		"knowledge:list",
		"knowledge:articles",
		"knowledge:article",
		"knowledge:pages",
		"knowledge:refresh",
	}
	a := &App{}
	for _, method := range methods {
		// handleIPCRequest apaga "method" do payload: map novo por iteração.
		resp := a.handleIPCRequest(context.Background(), map[string]any{"method": method})
		errMsg, _ := resp["error"].(string)
		if strings.HasPrefix(errMsg, "método desconhecido") {
			t.Fatalf("%s não está registrado no dispatcher IPC", method)
		}
		if !strings.Contains(errMsg, "support service indisponível") {
			t.Fatalf("%s: esperava guard de serviço indisponível, veio: %v", method, resp)
		}
	}
}

// O cap do cliente IPC precisa acomodar o caminho offline do suporte (o teto
// antigo de 10s abortava antes do snapshot aparecer).
func TestIPCRequestTimeoutCoversSupportReads(t *testing.T) {
	if time.Duration(IPCRequestTimeout) < supportRPCReadTimeout {
		t.Fatalf("IPCRequestTimeout=%s menor que supportRPCReadTimeout=%s", IPCRequestTimeout, supportRPCReadTimeout)
	}
	if time.Duration(IPCRequestTimeout) < supportRPCWriteTimeout {
		t.Fatalf("IPCRequestTimeout=%s menor que supportRPCWriteTimeout=%s", IPCRequestTimeout, supportRPCWriteTimeout)
	}
}
