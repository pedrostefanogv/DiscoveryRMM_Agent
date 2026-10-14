package support

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"discovery/app/debug"
)

// GUIDs de anexo usados nos testes de imagem da base de conhecimento.
const (
	kbImageID1 = "0195c9e5-1111-2222-3333-444455556666"
	kbImageID2 = "0195c9e5-7777-8888-9999-aaaabbbbcccc"
)

// TestBuildSummary_IgnoreImageToken garante que o resumo não vira a referência
// da imagem (a linha só com imagem fica vazia; a imagem some da linha mista).
func TestBuildSummary_IgnoreImageToken(t *testing.T) {
	onlyImage := "![diagrama](kbimage:" + kbImageID1 + ")"
	if got := buildSummary(onlyImage); got != "" {
		t.Fatalf("linha só com imagem deveria resultar em resumo vazio: %q", got)
	}

	// Imagem ocupando a primeira linha: ela fica vazia e o resumo cai na próxima
	// linha útil (sem nunca devolver o token/imagem como resumo).
	firstLineImage := "![diagrama](kbimage:" + kbImageID1 + ")\nFalha ao instalar o agente no Windows"
	if got := buildSummary(firstLineImage); got != "Falha ao instalar o agente no Windows" {
		t.Fatalf("resumo deveria pular a linha de imagem: %q", got)
	}

	// Imagem no meio da linha: some, o texto em volta permanece.
	inlineImage := "Antes ![diagrama](kbimage:" + kbImageID1 + ") depois"
	if got := buildSummary(inlineImage); strings.Contains(got, "kbimage") ||
		!strings.Contains(got, "Antes") || !strings.Contains(got, "depois") {
		t.Fatalf("token de imagem deveria sair da linha mantendo o texto: %q", got)
	}

	httpOnly := "![print](https://example.com/print.png)"
	if got := buildSummary(httpOnly); got != "" {
		t.Fatalf("linha só com imagem http deveria ter resumo vazio: %q", got)
	}
}

func kbImageTestConfig(apiServer string) debug.Config {
	return debug.Config{ApiScheme: "http", ApiServer: apiServer, AuthToken: "mdz_test", AgentID: lifecycleAgentID}
}

// O download vai autenticado: sem Authorization/X-Agent-ID o endpoint devolve
// 401 e o teste pegaria um "drop de header" silencioso.
func TestGetKnowledgeImageDataUrl_SendsAgentAuthHeaders(t *testing.T) {
	var auth, agentID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		agentID = r.Header.Get("X-Agent-ID")
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png"))
	}))
	defer srv.Close()

	svc := NewService(Options{DB: newMemCacheDB(), DebugConfig: func() debug.Config {
		return kbImageTestConfig(strings.TrimPrefix(srv.URL, "http://"))
	}})

	if _, err := svc.GetKnowledgeImageDataUrl(kbImageID1); err != nil {
		t.Fatalf("download autenticado deveria funcionar: %v", err)
	}
	if !strings.HasPrefix(auth, "Bearer mdz_") {
		t.Fatalf("Authorization ausente/inesperada: %q", auth)
	}
	if agentID != lifecycleAgentID {
		t.Fatalf("X-Agent-ID inesperado: %q", agentID)
	}
}

// 200 com página de erro do proxy (text/html) não pode virar data URL nem ficar
// 180 dias no cache como "imagem" quebrada.
func TestGetKnowledgeImageDataUrl_NonImageResponseIsRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html>pagina de erro do proxy</html>"))
	}))
	defer srv.Close()

	db := newMemCacheDB()
	svc := NewService(Options{DB: db, DebugConfig: func() debug.Config {
		return kbImageTestConfig(strings.TrimPrefix(srv.URL, "http://"))
	}})

	if _, err := svc.GetKnowledgeImageDataUrl(kbImageID1); err == nil {
		t.Fatal("resposta text/html deveria devolver erro")
	}
	var stored string
	if found, _ := db.CacheGetJSON("knowledge:image:"+kbImageID1, &stored); found {
		t.Fatal("resposta fora de image/* não pode ser cacheada")
	}
}

// 200 com corpo vazio não é imagem: data URL vazia nunca deve ser cacheada.
func TestGetKnowledgeImageDataUrl_EmptyBodyIsRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	db := newMemCacheDB()
	svc := NewService(Options{DB: db, DebugConfig: func() debug.Config {
		return kbImageTestConfig(strings.TrimPrefix(srv.URL, "http://"))
	}})

	if _, err := svc.GetKnowledgeImageDataUrl(kbImageID1); err == nil {
		t.Fatal("corpo vazio deveria devolver erro")
	}
	var stored string
	if found, _ := db.CacheGetJSON("knowledge:image:"+kbImageID1, &stored); found {
		t.Fatal("corpo vazio não pode ser cacheado")
	}
}

// 503 (storage desconfigurado) devolve erro e não envenena o cache.
func TestGetKnowledgeImageDataUrl_503ReturnsErrorAndDoesNotCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("{\"error\":\"object storage nao configurado\"}"))
	}))
	defer srv.Close()

	db := newMemCacheDB()
	svc := NewService(Options{DB: db, DebugConfig: func() debug.Config {
		return kbImageTestConfig(strings.TrimPrefix(srv.URL, "http://"))
	}})

	if _, err := svc.GetKnowledgeImageDataUrl(kbImageID1); err == nil {
		t.Fatal("503 deveria devolver erro")
	}
	var stored string
	if found, _ := db.CacheGetJSON("knowledge:image:"+kbImageID1, &stored); found {
		t.Fatal("503 não pode ser cacheado")
	}
}

// GUID em caixa alta usa a MESMA chave de cache (minúscula): evita baixar duas
// vezes o mesmo anexo quando o token do Markdown vem com caixa diferente.
func TestGetKnowledgeImageDataUrl_CacheKeyIsLowercase(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png"))
	}))
	defer srv.Close()

	db := newMemCacheDB()
	svc := NewService(Options{DB: db, DebugConfig: func() debug.Config {
		return kbImageTestConfig(strings.TrimPrefix(srv.URL, "http://"))
	}})

	if _, err := svc.GetKnowledgeImageDataUrl(strings.ToUpper(kbImageID1)); err != nil {
		t.Fatalf("id em caixa alta deveria funcionar: %v", err)
	}

	var stored string
	if found, err := db.CacheGetJSON("knowledge:image:"+kbImageID1, &stored); err != nil || !found {
		t.Fatalf("cache deveria estar na chave minúscula: found=%v err=%v", found, err)
	}
}

// cacheHit: o cache SQLite responde primeiro e nenhum HTTP é disparado.
func TestGetKnowledgeImageDataUrl_CacheHitNoHTTP(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png"))
	}))
	defer srv.Close()

	db := newMemCacheDB()
	cached := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("em cache"))
	if err := db.CacheSetJSON("knowledge:image:"+kbImageID1, cached, 0); err != nil {
		t.Fatalf("preparar cache: %v", err)
	}
	svc := NewService(Options{DB: db, DebugConfig: func() debug.Config {
		return kbImageTestConfig(strings.TrimPrefix(srv.URL, "http://"))
	}})

	got, err := svc.GetKnowledgeImageDataUrl(kbImageID1)
	if err != nil {
		t.Fatalf("cache hit deveria funcionar offline: %v", err)
	}
	if got != cached {
		t.Fatalf("data URL inesperada: %q", got)
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("cache hit não deveria fazer HTTP (chamadas=%d)", n)
	}
}

// miss: baixa do endpoint autenticado e grava a data URL no cache (imagem pequena).
func TestGetKnowledgeImageDataUrl_MissFetchesAndCaches(t *testing.T) {
	payload := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a}
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if !strings.Contains(r.URL.Path, "/api/v1/agent-auth/knowledge/images/"+kbImageID1) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	db := newMemCacheDB()
	svc := NewService(Options{DB: db, DebugConfig: func() debug.Config {
		return kbImageTestConfig(strings.TrimPrefix(srv.URL, "http://"))
	}})

	got, err := svc.GetKnowledgeImageDataUrl(kbImageID1)
	if err != nil {
		t.Fatalf("miss deveria baixar a imagem: %v", err)
	}
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(payload)
	if got != want {
		t.Fatalf("data URL inesperada: %q", got)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("esperado 1 GET, obtido %d", n)
	}

	var stored string
	found, err := db.CacheGetJSON("knowledge:image:"+kbImageID1, &stored)
	if err != nil || !found || stored != want {
		t.Fatalf("imagem pequena deveria ficar no cache: found=%v err=%v stored=%q", found, err, stored)
	}
}

// 404 (anexo não é imagem visível) devolve erro e NÃO é cacheado.
func TestGetKnowledgeImageDataUrl_404ReturnsErrorAndDoesNotCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"nao e imagem de artigo"}`))
	}))
	defer srv.Close()

	db := newMemCacheDB()
	svc := NewService(Options{DB: db, DebugConfig: func() debug.Config {
		return kbImageTestConfig(strings.TrimPrefix(srv.URL, "http://"))
	}})

	if _, err := svc.GetKnowledgeImageDataUrl(kbImageID1); err == nil {
		t.Fatal("404 deveria devolver erro")
	}
	var stored string
	if found, _ := db.CacheGetJSON("knowledge:image:"+kbImageID1, &stored); found {
		t.Fatal("erro HTTP não pode ser cacheado")
	}
}

// GUID inválido falha antes de qualquer HTTP.
func TestGetKnowledgeImageDataUrl_InvalidGuidNoHTTP(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png"))
	}))
	defer srv.Close()

	svc := NewService(Options{DB: newMemCacheDB(), DebugConfig: func() debug.Config {
		return kbImageTestConfig(strings.TrimPrefix(srv.URL, "http://"))
	}})

	for _, id := range []string{"", "  ", "nao-e-guid", "kbimage:" + kbImageID1} {
		if _, err := svc.GetKnowledgeImageDataUrl(id); err == nil {
			t.Fatalf("id inválido %q deveria devolver erro", id)
		}
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("id inválido não deveria fazer HTTP (chamadas=%d)", n)
	}
}

// Binário acima do teto (2 MB) ainda é exibido, mas não entra no cache SQLite.
func TestGetKnowledgeImageDataUrl_LargeBinaryNotCached(t *testing.T) {
	payload := bytes.Repeat([]byte{0xAB}, knowledgeImageMaxCacheBytes+1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	db := newMemCacheDB()
	svc := NewService(Options{DB: db, DebugConfig: func() debug.Config {
		return kbImageTestConfig(strings.TrimPrefix(srv.URL, "http://"))
	}})

	got, err := svc.GetKnowledgeImageDataUrl(kbImageID1)
	if err != nil {
		t.Fatalf("imagem grande deveria ser exibida: %v", err)
	}
	if !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Fatalf("data URL inesperada: %.40q", got)
	}

	var stored string
	if found, _ := db.CacheGetJSON("knowledge:image:"+kbImageID1, &stored); found {
		t.Fatal("binário acima do teto não pode ser cacheado")
	}
}

// Troca de escopo descarta as imagens da KB (chave sem escopo) — evita servir
// anexo do cliente/site anterior após transferência/reprovisionamento.
func TestCleanupOldKnowledgeScope_RemovesImages(t *testing.T) {
	db := newMemCacheDB()
	if err := db.CacheSetJSON("knowledge:image:"+kbImageID1, "data:image/png;base64,AA==", 0); err != nil {
		t.Fatalf("preparar imagem: %v", err)
	}
	if err := db.CacheSetJSON(knowledgeScopeControlKey, "http:old:1:client:site:agent", 0); err != nil {
		t.Fatalf("preparar escopo ativo: %v", err)
	}

	svc := NewService(Options{DB: db})
	svc.cleanupOldKnowledgeScope("http:new:2:client:site:agent")

	var stored string
	if found, _ := db.CacheGetJSON("knowledge:image:"+kbImageID1, &stored); found {
		t.Fatal("imagem do escopo anterior deveria ter sido removida")
	}
}
