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

// memCacheHasPrefix informa se existe alguma chave com o prefixo (usa o fake
// memCacheDB do service_offline_cache_test.go).
func memCacheHasPrefix(db *memCacheDB, prefix string) bool {
	db.mu.Lock()
	defer db.mu.Unlock()
	for k := range db.data {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

func waitForCachePrefix(t *testing.T, db *memCacheDB, prefix string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if memCacheHasPrefix(db, prefix) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("prefetch não gravou nenhuma chave com prefixo %q", prefix)
}

// O prefetch pós-listagem precisa cachear detalhe + histórico de comentários
// (+ campos/respostas) de cada chamado. Sem isso, offline o usuário abria o
// chamado e não via os comentários já existentes.
func TestTicketList_PrefetchesDetailAndComments(t *testing.T) {
	db := newMemCacheDB()
	if err := db.CacheSetJSON("agent_info", AgentInfo{AgentID: lifecycleAgentID, ClientID: "client-1", SiteID: "site-1"}, 0); err != nil {
		t.Fatalf("seed agent_info: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Path
		switch {
		case p == "/api/v1/agent-auth/me/tickets":
			fmt.Fprint(w, "[{\"id\":\""+lifecycleTicketID+"\",\"title\":\"T\",\"updatedAt\":\"2026-01-01T00:00:00Z\"}]")
		case strings.HasSuffix(p, "/comments"):
			fmt.Fprint(w, "[{\"id\":\"c1\",\"author\":\"u\",\"content\":\"comentario publico\",\"isInternal\":false,\"createdAt\":\"2026-01-01T00:00:00Z\"}]")
		case strings.HasSuffix(p, "/fields"):
			fmt.Fprint(w, "[{\"definitionId\":\"d1\",\"label\":\"Campo\",\"dataType\":\"text\",\"valueJson\":\"v\"}]")
		case strings.HasSuffix(p, "/answers"):
			fmt.Fprint(w, "[{\"id\":\"a1\",\"questionKey\":\"q\",\"questionLabel\":\"Q\",\"valueText\":\"v\",\"createdAt\":\"2026-01-01T00:00:00Z\"}]")
		case strings.HasPrefix(p, "/api/v1/agent-auth/me/tickets/"):
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

	waitForCachePrefix(t, db, "tickets:backup:detail:")
	waitForCachePrefix(t, db, "tickets:backup:comments:")
	waitForCachePrefix(t, db, "tickets:backup:fields:")
	waitForCachePrefix(t, db, "tickets:backup:answers:")

	var comments []TicketComment
	found, err := db.CacheGetJSON(ticketCommentsBackupKey(cfg, lifecycleTicketID), &comments)
	if err != nil || !found || len(comments) != 1 || comments[0].Content != "comentario publico" {
		t.Fatalf("backup de comentários incorreto: found=%t err=%v %+v", found, err, comments)
	}
}

// Cobre o prefetch da árvore de sub-páginas de artigos multipágina — antes a
// árvore só era cacheada depois de abrir o artigo ONLINE.
func TestKnowledgeList_PrefetchesArticlePages(t *testing.T) {
	db := newMemCacheDB()
	if err := db.CacheSetJSON("agent_info", AgentInfo{AgentID: lifecycleAgentID, ClientID: "client-1", SiteID: "site-1"}, 0); err != nil {
		t.Fatalf("seed agent_info: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Path
		switch {
		case p == "/api/v1/agent-auth/knowledge":
			fmt.Fprint(w, "[{\"id\":\"art-1\",\"title\":\"Artigo\",\"content\":\"# topo\",\"updatedAt\":\"2026-01-01T00:00:00Z\"}]")
		case strings.HasSuffix(p, "/pages"):
			fmt.Fprint(w, "[{\"id\":\"p1\",\"articleId\":\"art-1\",\"title\":\"Sub-pagina\",\"content\":\"# sub\",\"childCount\":0}]")
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

	if _, err := svc.GetKnowledgeBaseArticleList(); err != nil {
		t.Fatalf("listagem de knowledge falhou: %v", err)
	}

	waitForCachePrefix(t, db, "knowledge:backup:pages:")

	scope := knowledgeCacheScope(cfg, AgentInfo{AgentID: lifecycleAgentID, ClientID: "client-1", SiteID: "site-1"})
	cacheKey := "knowledge:pages:" + scope + ":art-1"
	var pages []KnowledgePage
	found, err := db.CacheGetJSON(knowledgeBackupKey(cacheKey), &pages)
	if err != nil || !found || len(pages) != 1 || pages[0].Title != "Sub-pagina" {
		t.Fatalf("backup de páginas incorreto: found=%t err=%v %+v", found, err, pages)
	}
}
