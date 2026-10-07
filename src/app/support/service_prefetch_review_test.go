package support

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"discovery/app/debug"
)

// Regressão: chamados encerrados/avaliados TAMBÉM precisam entrar no prefetch —
// o usuário consulta o histórico de comentários offline neles. A seleção só
// descarta itens sem ID.
func TestSelectTicketsForPrefetch_KeepsRatedAndClosed(t *testing.T) {
	rating := 5
	closed := "2026-09-28T01:08:03Z"
	tickets := []APITicket{
		{ID: "a"},
		{ID: "b", Rating: &rating, ClosedAt: &closed},
		{ID: "  "},
	}
	got := selectTicketsForPrefetch(tickets)
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("seleção inesperada: %+v", got)
	}
}

// B3: se o endpoint de comentários falhar, o chamado NÃO é marcado como
// prefetchado (senão o histórico nunca seria cacheado sem retry).
func TestTicketPrefetch_CommentsFailureDoesNotMarkDone(t *testing.T) {
	db := newMemCacheDB()
	if err := db.CacheSetJSON("agent_info", AgentInfo{AgentID: lifecycleAgentID, ClientID: "client-1", SiteID: "site-1"}, 0); err != nil {
		t.Fatalf("seed agent_info: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case p == "/api/v1/agent-auth/me/tickets":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, "[{\"id\":\""+lifecycleTicketID+"\",\"title\":\"T\",\"updatedAt\":\"2026-01-01T00:00:00Z\"}]")
		case strings.HasSuffix(p, "/comments"):
			http.Error(w, "boom", http.StatusInternalServerError)
		case strings.HasPrefix(p, "/api/v1/agent-auth/me/tickets/"):
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, lifecycleTicketJSON(nil))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	cfg := debug.Config{
		ApiScheme: "http",
		ApiServer: strings.TrimPrefix(srv.URL, "http://"),
		AuthToken: "mdz_test",
		AgentID:   lifecycleAgentID,
	}
	svc := NewService(Options{DB: db, DebugConfig: func() debug.Config { return cfg }})

	if _, err := svc.GetSupportTicketList(); err != nil {
		t.Fatalf("listagem falhou: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		svc.ticketsPrefetchMu.Lock()
		fails := svc.ticketsPrefetchFails[lifecycleTicketID]
		done := svc.ticketsPrefetched[lifecycleTicketID]
		svc.ticketsPrefetchMu.Unlock()
		if done != "" {
			t.Fatalf("chamado não pode ser marcado como prefetchado quando /comments falha")
		}
		if fails > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("prefetch não registrou a falha de comentários")
}

// B2: troca de contexto/escopo limpa o bookkeeping do prefetch.
func TestInvalidateAgentContext_ResetsPrefetchState(t *testing.T) {
	svc := NewService(Options{DB: newMemCacheDB()})
	svc.ticketsPrefetchMu.Lock()
	svc.ticketsPrefetched = map[string]string{"t": "1"}
	svc.ticketsPrefetchFails = map[string]int{"t": 1}
	svc.ticketsPrefetchMu.Unlock()
	svc.pagesPrefetchMu.Lock()
	svc.pagesPrefetched = map[string]string{"a": "1"}
	svc.pagesPrefetchFails = map[string]int{"a": 1}
	svc.pagesPrefetchMu.Unlock()

	svc.InvalidateAgentContext()

	svc.ticketsPrefetchMu.Lock()
	if svc.ticketsPrefetched != nil || svc.ticketsPrefetchFails != nil {
		svc.ticketsPrefetchMu.Unlock()
		t.Fatal("bookkeeping de chamados deveria ser zerado")
	}
	svc.ticketsPrefetchMu.Unlock()
	svc.pagesPrefetchMu.Lock()
	defer svc.pagesPrefetchMu.Unlock()
	if svc.pagesPrefetched != nil || svc.pagesPrefetchFails != nil {
		t.Fatal("bookkeeping de páginas deveria ser zerado")
	}
}

// M1: o prefetch reusa um único client HTTP.
func TestPrefetchKnowledgeHTTP_ReusesClient(t *testing.T) {
	svc := NewService(Options{DB: newMemCacheDB()})
	c1 := svc.prefetchKnowledgeHTTP()
	c2 := svc.prefetchKnowledgeHTTP()
	if c1 == nil || c1 != c2 {
		t.Fatal("prefetch deveria reusar o mesmo client HTTP")
	}
}
