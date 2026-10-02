package support

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"discovery/app/debug"
)

// memCacheDB é um CacheDB em memória que imita a tabela "cache" do SQLite local.
type memCacheDB struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemCacheDB() *memCacheDB { return &memCacheDB{data: map[string][]byte{}} }

func (m *memCacheDB) CacheGetJSON(key string, out any) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, ok := m.data[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, out)
}

func (m *memCacheDB) CacheSetJSON(key string, value any, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	m.data[key] = raw
	return nil
}

func (m *memCacheDB) CacheDelete(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

// unreachableAPI devolve o host de um servidor já encerrado (conexão recusada
// imediata), simulando o agente offline sem esperar timeout de rede.
func unreachableAPI(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	host := strings.TrimPrefix(srv.URL, "http://")
	srv.Close()
	return host
}

func offlineConfig(apiServer string) debug.Config {
	return debug.Config{ApiScheme: "http", ApiServer: apiServer, AuthToken: "mdz_test", AgentID: lifecycleAgentID}
}

// Regressão do bug relatado: offline, o agente não podia consultar chamados nem
// a base de conhecimento porque /configuration falhava ANTES de chegar ao cache.
// A identidade durável (sem expiração) deve resolver o contexto localmente.
func TestFetchAgentContext_FallsBackToStaleIdentityOffline(t *testing.T) {
	db := newMemCacheDB()
	stale := AgentInfo{AgentID: lifecycleAgentID, ClientID: "client-1", SiteID: "site-1"}
	if err := db.CacheSetJSON(agentInfoStaleCacheKey, stale, 0); err != nil {
		t.Fatalf("preparar cache: %v", err)
	}

	svc := NewService(Options{DB: db, DebugConfig: func() debug.Config {
		return offlineConfig(unreachableAPI(t))
	}})

	info, err := svc.GetAgentInfo()
	if err != nil {
		t.Fatalf("esperado fallback offline para a identidade, erro: %v", err)
	}
	if info.ClientID != "client-1" || info.SiteID != "site-1" {
		t.Fatalf("identidade offline inesperada: %+v", info)
	}
}

// Sem identidade durável ainda (instalação antiga), o clientId/siteId
// persistidos na configuração do agente devem cobrir o offline.
func TestFetchAgentContext_FallsBackToPersistedConfigAndPersistsIdentity(t *testing.T) {
	db := newMemCacheDB()
	svc := NewService(Options{
		DB: db,
		DebugConfig: func() debug.Config {
			return offlineConfig(unreachableAPI(t))
		},
		AgentInfoFallback: func() (AgentInfo, bool) {
			return AgentInfo{ClientID: "client-9", SiteID: "site-9"}, true
		},
	})

	info, err := svc.GetAgentInfo()
	if err != nil {
		t.Fatalf("esperado fallback pela configuração persistida, erro: %v", err)
	}
	if info.ClientID != "client-9" {
		t.Fatalf("clientId inesperado: %+v", info)
	}

	var persisted AgentInfo
	found, err := db.CacheGetJSON(agentInfoStaleCacheKey, &persisted)
	if err != nil || !found || persisted.ClientID != "client-9" {
		t.Fatalf("identidade deveria ficar durável para as próximas aberturas: found=%v err=%v %+v", found, err, persisted)
	}
}

// Offline: a listagem de chamados deve cair no snapshot local.
func TestGetSupportTickets_UsesOfflineSnapshot(t *testing.T) {
	db := newMemCacheDB()
	cfg := offlineConfig(unreachableAPI(t))
	backup := []APITicket{{ID: "t-1", Title: "Chamado em cache"}, {ID: "t-2", Title: "Outro"}}
	if err := db.CacheSetJSON(ticketListBackupKey(ticketListCacheKey(cfg)), backup, 0); err != nil {
		t.Fatalf("preparar snapshot: %v", err)
	}
	if err := db.CacheSetJSON(agentInfoStaleCacheKey, AgentInfo{AgentID: lifecycleAgentID, ClientID: "client-1"}, 0); err != nil {
		t.Fatalf("preparar identidade: %v", err)
	}

	svc := NewService(Options{DB: db, DebugConfig: func() debug.Config { return cfg }})

	tickets, err := svc.GetSupportTickets()
	if err != nil {
		t.Fatalf("esperado snapshot offline de chamados, erro: %v", err)
	}
	if len(tickets) != 2 || tickets[0].ID != "t-1" {
		t.Fatalf("snapshot inesperado: %+v", tickets)
	}
}

// Toda listagem bem-sucedida deve gravar o snapshot que alimenta o offline.
func TestGetSupportTickets_PersistsOfflineSnapshot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/configuration"):
			_, _ = w.Write([]byte(`{"agentId":"` + lifecycleAgentID + `","clientId":"client-1","siteId":"site-1"}`))
		case strings.HasSuffix(r.URL.Path, "/tickets"):
			_, _ = w.Write([]byte(`[{"id":"t-1","title":"Chamado"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := offlineConfig(strings.TrimPrefix(srv.URL, "http://"))
	db := newMemCacheDB()
	svc := NewService(Options{DB: db, DebugConfig: func() debug.Config { return cfg }})

	if _, err := svc.GetSupportTickets(); err != nil {
		t.Fatalf("listagem online falhou: %v", err)
	}

	var stored []APITicket
	found, err := db.CacheGetJSON(ticketListBackupKey(ticketListCacheKey(cfg)), &stored)
	if err != nil || !found || len(stored) != 1 || stored[0].ID != "t-1" {
		t.Fatalf("snapshot não persistido: found=%v err=%v %+v", found, err, stored)
	}
}

// Offline: a base de conhecimento deve usar o backup local já existente, que
// antes era inalcançável porque o contexto do agente falhava primeiro.
func TestGetKnowledgeArticles_UsesOfflineBackupWithStaleIdentity(t *testing.T) {
	db := newMemCacheDB()
	cfg := offlineConfig(unreachableAPI(t))
	info := AgentInfo{AgentID: lifecycleAgentID, ClientID: "client-1", SiteID: "site-1"}
	if err := db.CacheSetJSON(agentInfoStaleCacheKey, info, 0); err != nil {
		t.Fatalf("preparar identidade: %v", err)
	}
	listKey := "knowledge:list:" + knowledgeCacheScope(cfg, info) + ":" + url.QueryEscape("")
	backup := []KnowledgeArticle{{ID: "k-1", Title: "Artigo offline", Content: "conteudo"}}
	if err := db.CacheSetJSON(knowledgeBackupKey(listKey), backup, 0); err != nil {
		t.Fatalf("preparar backup: %v", err)
	}

	svc := NewService(Options{DB: db, DebugConfig: func() debug.Config { return cfg }})

	articles, err := svc.GetKnowledgeArticles("")
	if err != nil {
		t.Fatalf("esperado backup offline de knowledge, erro: %v", err)
	}
	if len(articles) != 1 || articles[0].ID != "k-1" {
		t.Fatalf("backup de knowledge inesperado: %+v", articles)
	}
}

// 4xx é problema real (credencial/rota) e NÃO pode ser mascarado pelo snapshot.
func TestGetSupportTickets_DoesNotMaskAuthErrorWithSnapshot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/configuration") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"agentId":"` + lifecycleAgentID + `","clientId":"client-1","siteId":"site-1"}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv.Close()

	cfg := offlineConfig(strings.TrimPrefix(srv.URL, "http://"))
	db := newMemCacheDB()
	if err := db.CacheSetJSON(ticketListBackupKey(ticketListCacheKey(cfg)), []APITicket{{ID: "t-1", Title: "antigo"}}, 0); err != nil {
		t.Fatalf("preparar snapshot: %v", err)
	}
	svc := NewService(Options{DB: db, DebugConfig: func() debug.Config { return cfg }})

	if _, err := svc.GetSupportTickets(); err == nil {
		t.Fatal("HTTP 401 deveria propagar erro em vez de servir o snapshot")
	}
}

// 5xx (servidor fora do ar) justifica o snapshot offline.
func TestGetSupportTickets_UsesSnapshotOnServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/configuration") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"agentId":"` + lifecycleAgentID + `","clientId":"client-1","siteId":"site-1"}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := offlineConfig(strings.TrimPrefix(srv.URL, "http://"))
	db := newMemCacheDB()
	if err := db.CacheSetJSON(ticketListBackupKey(ticketListCacheKey(cfg)), []APITicket{{ID: "t-9", Title: "snapshot"}}, 0); err != nil {
		t.Fatalf("preparar snapshot: %v", err)
	}
	svc := NewService(Options{DB: db, DebugConfig: func() debug.Config { return cfg }})

	tickets, err := svc.GetSupportTickets()
	if err != nil {
		t.Fatalf("500 deveria usar o snapshot: %v", err)
	}
	if len(tickets) != 1 || tickets[0].ID != "t-9" {
		t.Fatalf("snapshot inesperado: %+v", tickets)
	}
}

// A listagem usada pela UI precisa sinalizar quando veio do snapshot offline.
func TestGetSupportTicketList_FlagsStaleFromSnapshot(t *testing.T) {
	db := newMemCacheDB()
	cfg := offlineConfig(unreachableAPI(t))
	if err := db.CacheSetJSON(ticketListBackupKey(ticketListCacheKey(cfg)), []APITicket{{ID: "t-1"}}, 0); err != nil {
		t.Fatalf("preparar snapshot: %v", err)
	}
	if err := db.CacheSetJSON(agentInfoStaleCacheKey, AgentInfo{AgentID: lifecycleAgentID, ClientID: "client-1"}, 0); err != nil {
		t.Fatalf("preparar identidade: %v", err)
	}
	svc := NewService(Options{DB: db, DebugConfig: func() debug.Config { return cfg }})

	res, err := svc.GetSupportTicketList()
	if err != nil {
		t.Fatalf("listagem offline falhou: %v", err)
	}
	if !res.Stale {
		t.Fatal("snapshot offline deveria ser marcado como stale")
	}
	if len(res.Tickets) != 1 || res.Tickets[0].ID != "t-1" {
		t.Fatalf("chamados inesperados: %+v", res.Tickets)
	}
}

// Comentários precisam continuar visíveis offline (antes a conversa mostrava
// erro cru de timeout).
func TestGetTicketComments_UsesOfflineSnapshot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/comments") {
			_, _ = w.Write([]byte(`[{"id":"c-1","content":"comentario local","author":"agente"}]`))
			return
		}
		http.NotFound(w, r)
	}))
	cfg := offlineConfig(strings.TrimPrefix(srv.URL, "http://"))
	db := newMemCacheDB()
	svc := NewService(Options{DB: db, DebugConfig: func() debug.Config { return cfg }})

	if _, err := svc.GetTicketComments(lifecycleTicketID); err != nil {
		t.Fatalf("comentários online falharam: %v", err)
	}
	srv.Close()

	comments, err := svc.GetTicketComments(lifecycleTicketID)
	if err != nil {
		t.Fatalf("comentários offline deveriam vir do snapshot: %v", err)
	}
	if len(comments) != 1 || comments[0].ID != "c-1" {
		t.Fatalf("comentários inesperados: %+v", comments)
	}
}

// O detalhe do chamado também precisa abrir offline.
func TestGetSupportTicketDetails_UsesOfflineSnapshot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, lifecycleTicketID) {
			_, _ = w.Write([]byte(`{"id":"` + lifecycleTicketID + `","title":"Detalhe local"}`))
			return
		}
		http.NotFound(w, r)
	}))
	cfg := offlineConfig(strings.TrimPrefix(srv.URL, "http://"))
	db := newMemCacheDB()
	svc := NewService(Options{DB: db, DebugConfig: func() debug.Config { return cfg }})

	if _, err := svc.GetSupportTicketDetails(lifecycleTicketID); err != nil {
		t.Fatalf("detalhe online falhou: %v", err)
	}
	srv.Close()

	ticket, err := svc.GetSupportTicketDetails(lifecycleTicketID)
	if err != nil {
		t.Fatalf("detalhe offline deveria vir do snapshot: %v", err)
	}
	if ticket.ID != lifecycleTicketID || ticket.Title != "Detalhe local" {
		t.Fatalf("detalhe inesperado: %+v", ticket)
	}
}

// A listagem de knowledge precisa sinalizar a origem offline.
func TestGetKnowledgeBaseArticleList_FlagsStaleFromBackup(t *testing.T) {
	db := newMemCacheDB()
	cfg := offlineConfig(unreachableAPI(t))
	info := AgentInfo{AgentID: lifecycleAgentID, ClientID: "client-1", SiteID: "site-1"}
	if err := db.CacheSetJSON(agentInfoStaleCacheKey, info, 0); err != nil {
		t.Fatalf("preparar identidade: %v", err)
	}
	listKey := "knowledge:list:" + knowledgeCacheScope(cfg, info) + ":" + url.QueryEscape("")
	backup := []KnowledgeArticle{{ID: "k-1", Title: "Artigo offline", Content: "conteudo"}}
	if err := db.CacheSetJSON(knowledgeBackupKey(listKey), backup, 0); err != nil {
		t.Fatalf("preparar backup: %v", err)
	}
	svc := NewService(Options{DB: db, DebugConfig: func() debug.Config { return cfg }})

	res, err := svc.GetKnowledgeBaseArticleList()
	if err != nil {
		t.Fatalf("knowledge offline falhou: %v", err)
	}
	if !res.Stale {
		t.Fatal("backup offline deveria ser marcado como stale")
	}
	if len(res.Articles) != 1 || res.Articles[0].ID != "k-1" {
		t.Fatalf("artigos inesperados: %+v", res.Articles)
	}
}
