package support

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"discovery/app/debug"
)

const (
	lifecycleTicketID = "22222222-2222-2222-2222-222222222222"
	lifecycleStateID  = "33333333-3333-3333-3333-333333333333"
	lifecycleAgentID  = "11111111-1111-1111-1111-111111111111"
)

func lifecycleTicketJSON(rating any) string {
	payload := map[string]any{
		"id":              lifecycleTicketID,
		"title":           "Chamado do agent",
		"workflowStateId": lifecycleStateID,
		"rating":          rating,
	}
	body, _ := json.Marshal(payload)
	return string(body)
}

func newLifecycleService(t *testing.T, handler http.HandlerFunc) *Service {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewService(Options{
		DebugConfig: func() debug.Config {
			return debug.Config{
				ApiScheme: "http",
				ApiServer: strings.TrimPrefix(srv.URL, "http://"),
				AuthToken: "mdz_test",
				AgentID:   lifecycleAgentID,
			}
		},
	})
}

// Reopen faz POST no /reopen (com idempotencia) e rele os detalhes.
func TestReopenSupportTicket_PostsAndRefetches(t *testing.T) {
	var posts, gets int
	var gotPath, gotKey string
	var gotBody map[string]any

	svc := newLifecycleService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/reopen"):
			posts++
			gotPath = r.URL.Path
			gotKey = r.Header.Get("Idempotency-Key")
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &gotBody)
			_, _ = w.Write([]byte(`{"ticketId":"` + lifecycleTicketID + `","closed":false}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tickets/"+lifecycleTicketID):
			gets++
			_, _ = w.Write([]byte(lifecycleTicketJSON(nil)))
		default:
			http.NotFound(w, r)
		}
	})

	ticket, err := svc.ReopenSupportTicket(lifecycleTicketID, "cliente voltou")
	if err != nil {
		t.Fatalf("reabertura falhou: %v", err)
	}
	if posts != 1 || gets != 1 {
		t.Fatalf("chamadas inesperadas: POST=%d GET=%d", posts, gets)
	}
	if !strings.HasSuffix(gotPath, "/api/v1/agent-auth/me/tickets/"+lifecycleTicketID+"/reopen") {
		t.Fatalf("rota inesperada: %q", gotPath)
	}
	if gotKey == "" {
		t.Fatal("Idempotency-Key nao foi enviado")
	}
	if gotBody["reason"] != "cliente voltou" {
		t.Fatalf("motivo da reabertura nao enviado: %+v", gotBody)
	}
	if ticket.ID != lifecycleTicketID {
		t.Fatalf("ticket deve ser relido completo, id=%q", ticket.ID)
	}
	if ticket.WorkflowStateID != lifecycleStateID {
		t.Fatalf("workflowStateId nao decodificado: %q", ticket.WorkflowStateID)
	}
}

// Reabrir chamado ja aberto devolve erro com a mensagem do servidor.
func TestReopenSupportTicket_PropagatesServerError(t *testing.T) {
	svc := newLifecycleService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"Somente chamados encerrados podem ser reabertos."}`))
	})

	_, err := svc.ReopenSupportTicket(lifecycleTicketID, "")
	if err == nil || !strings.Contains(err.Error(), "Somente chamados encerrados") {
		t.Fatalf("erro deve propagar a mensagem do servidor, got=%v", err)
	}
}

// Nota fora de 1..5 falha antes de qualquer chamada HTTP.
func TestRateSupportTicket_RejectsInvalidRatingLocally(t *testing.T) {
	var calls int
	svc := newLifecycleService(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.NotFound(w, r)
	})

	for _, rating := range []int{0, 6, -1} {
		if _, err := svc.RateSupportTicket(lifecycleTicketID, rating, ""); err == nil {
			t.Fatalf("rating %d deveria falhar", rating)
		}
	}
	if calls != 0 {
		t.Fatalf("nenhuma chamada HTTP esperada, got=%d", calls)
	}
}

// Rate envia rating+feedback no corpo e rele os detalhes.
func TestRateSupportTicket_SendsRatingAndRefetches(t *testing.T) {
	var gotBody map[string]any
	var ratePosts, gets int

	svc := newLifecycleService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/rating"):
			ratePosts++
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &gotBody)
			_, _ = w.Write([]byte(`{"ticketId":"` + lifecycleTicketID + `","rating":5}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tickets/"+lifecycleTicketID):
			gets++
			_, _ = w.Write([]byte(lifecycleTicketJSON(5)))
		default:
			http.NotFound(w, r)
		}
	})

	ticket, err := svc.RateSupportTicket(lifecycleTicketID, 5, "otimo atendimento")
	if err != nil {
		t.Fatalf("avaliacao falhou: %v", err)
	}
	if ratePosts != 1 || gets != 1 {
		t.Fatalf("chamadas inesperadas: POST=%d GET=%d", ratePosts, gets)
	}
	if gotBody["rating"] != float64(5) || gotBody["feedback"] != "otimo atendimento" {
		t.Fatalf("corpo inesperado: %+v", gotBody)
	}
	if ticket.Rating == nil || *ticket.Rating != 5 {
		t.Fatalf("rating nao relido: %+v", ticket.Rating)
	}
}

// Regressao: /close devolve apenas um resumo; o servico precisa reler o ticket
// completo (antes decodificava o resumo como APITicket e o id vinha vazio).
func TestCloseSupportTicket_RefetchesFullTicket(t *testing.T) {
	var closePosts, gets int

	svc := newLifecycleService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/close"):
			closePosts++
			_, _ = w.Write([]byte(`{"ticketId":"` + lifecycleTicketID + `","closed":true,"workflowStateId":"` + lifecycleStateID + `","rating":null}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tickets/"+lifecycleTicketID):
			gets++
			_, _ = w.Write([]byte(`{"id":"` + lifecycleTicketID + `","title":"Chamado do agent","workflowStateId":"` + lifecycleStateID + `","closedAt":"2026-10-01T12:00:00Z"}`))
		default:
			http.NotFound(w, r)
		}
	})

	ticket, err := svc.CloseSupportTicket(lifecycleTicketID, CloseTicketInput{})
	if err != nil {
		t.Fatalf("fechamento falhou: %v", err)
	}
	if closePosts != 1 || gets != 1 {
		t.Fatalf("chamadas inesperadas: POST=%d GET=%d", closePosts, gets)
	}
	if ticket.ID != lifecycleTicketID {
		t.Fatalf("ticket fechado deve ter id completo, got=%q", ticket.ID)
	}
	if ticket.ClosedAt == nil {
		t.Fatal("closedAt deveria vir do detalhe relido")
	}
}

// GetTicketAnswers faz GET no /answers, com auth do agent, e decodifica a lista.
func TestGetTicketAnswers_FetchesAndParses(t *testing.T) {
	var gotPath string
	var gotAgentID string

	svc := newLifecycleService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tickets/"+lifecycleTicketID+"/answers") {
			gotPath = r.URL.Path
			gotAgentID = r.Header.Get("X-Agent-ID")
			_, _ = w.Write([]byte(`[{"id":"a1","questionKey":"tipo","questionLabel":"Tipo de equipamento","valueText":"Notebook","createdAt":"2026-10-01T10:00:00Z"}]`))
			return
		}
		http.NotFound(w, r)
	})

	answers, err := svc.GetTicketAnswers(lifecycleTicketID)
	if err != nil {
		t.Fatalf("busca de respostas falhou: %v", err)
	}
	if len(answers) != 1 {
		t.Fatalf("esperava 1 resposta, got=%d", len(answers))
	}
	if answers[0].QuestionLabel != "Tipo de equipamento" || answers[0].ValueText != "Notebook" {
		t.Fatalf("resposta inesperada: %+v", answers[0])
	}
	if !strings.HasSuffix(gotPath, "/api/v1/agent-auth/me/tickets/"+lifecycleTicketID+"/answers") {
		t.Fatalf("rota inesperada: %q", gotPath)
	}
	if gotAgentID != lifecycleAgentID {
		t.Fatalf("X-Agent-ID ausente/incorreto: %q", gotAgentID)
	}

	// ticketId invalido falha antes de qualquer HTTP.
	if _, err := svc.GetTicketAnswers("nao-e-guid"); err == nil {
		t.Fatal("ticketId invalido deveria falhar")
	}
}
