package support

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"discovery/app/core/tlsutil"
	"discovery/app/debug"
	"discovery/app/netutil"
)

const (
	knowledgeListCacheTTL       = 6 * time.Hour
	knowledgeDetailCacheTTL     = 6 * time.Hour
	knowledgeMinRefreshInterval = 5 * time.Minute
	// knowledgeBackupTTL: cópia de segurança usada quando o servidor está
	// inacessível (stale-if-error) — a página de conhecimento continua
	// funcionando offline/instável com o último conteúdo conhecido. Alinhado ao
	// longo prazo da identidade durável (era 30d e sumia antes dos chamados).
	knowledgeBackupTTL = 180 * 24 * time.Hour
	// knowledgeMaxBodyBytes: teto de leitura do corpo HTTP (anti-OOM para
	// respostas gigantes/malformadas do servidor).
	knowledgeMaxBodyBytes = 8 << 20
	// knowledgeDetailWorkers: paralelismo máximo no enriquecimento de conteúdo.
	knowledgeDetailWorkers = 4
	// Paginação keyset da listagem: página padrão da API (clampada 1–500 lá)
	// e teto de páginas por carga (50 × 200 = 10k artigos) contra loop/runaway.
	knowledgeListPageSize = 200
	knowledgeMaxPages     = 50
)

// kbHTTPClient é reutilizado entre requisições (reuso de conexões TLS em vez
// de um client novo por chamada, que multiplicava handshakes no N+1 antigo).
var (
	kbHTTPClientOnce sync.Once
	kbHTTPClient     *http.Client
)

func kbHTTP() *http.Client {
	kbHTTPClientOnce.Do(func() {
		kbHTTPClient = tlsutil.NewHTTPClient(15 * time.Second)
	})
	return kbHTTPClient
}

// CachePurger é implementado opcionalmente pelo CacheDB (database.DB) para
// permitir limpeza por prefixo no refresh da base de conhecimento.
type CachePurger interface {
	CacheDeletePrefix(prefix string) error
}

func toStringSlice(value any) []string {
	arr, ok := value.([]any)
	if !ok {
		if strArr, ok := value.([]string); ok {
			return strArr
		}
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		s := strings.TrimSpace(fmt.Sprint(item))
		if s != "" && s != "<nil>" {
			out = append(out, s)
		}
	}
	return out
}

func parseTagsFromJSON(value any) []string {
	s := strings.TrimSpace(fmt.Sprint(value))
	if s == "" || s == "<nil>" {
		return nil
	}
	var arr []string
	if err := json.Unmarshal([]byte(s), &arr); err != nil {
		return nil
	}
	return arr
}

func estimateReadTimeMin(markdown string) int {
	words := len(strings.Fields(strings.TrimSpace(markdown)))
	if words <= 0 {
		return 1
	}
	if m := (words + 179) / 180; m > 0 {
		return m
	}
	return 1
}

// truncateUTF8 corta a string em no maximo maxRunes runas, sem quebrar
// caracteres multibyte (acentos, emojis). Antes o corte era por bytes
// (line[:180]) e corrompia resumos com acentuacao PT-BR.
func truncateUTF8(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}

// knowledgeBackupKey converte uma chave de cache em chave de backup
// (knowledge:list:<scope>:<cat> -> knowledge:backup:list:<scope>:<cat>).
// Backups ficam FORA dos prefixos knowledge:list|detail|pages: e por isso
// sobrevivem ao purge do refresh — servem como fallback offline.
func knowledgeBackupKey(cacheKey string) string {
	return "knowledge:backup:" + strings.TrimPrefix(cacheKey, "knowledge:")
}

// saveKnowledgeBackup grava uma copia de longa duracao (best-effort).
func (s *Service) saveKnowledgeBackup(cacheKey string, value any) {
	if s.db == nil {
		return
	}
	if err := s.db.CacheSetJSON(knowledgeBackupKey(cacheKey), value, knowledgeBackupTTL); err != nil {
		log.Printf("[support] aviso: falha ao salvar backup de knowledge: %v", err)
	}
}

// readKnowledgeBackup le a copia de seguranca (stale-if-error). Retorna false
// quando nao ha backup utilizavel.
func (s *Service) readKnowledgeBackup(cacheKey string, out any) bool {
	if s.db == nil {
		return false
	}
	found, err := s.db.CacheGetJSON(knowledgeBackupKey(cacheKey), out)
	return err == nil && found
}

func buildSummary(content string) string {
	if strings.TrimSpace(content) == "" {
		return ""
	}
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimLeft(line, "#*-0123456789. "))
		if line != "" {
			// Corte por RUNAS (nao por bytes): cortar por byte quebrava
			// caracteres multibyte (acentos PT-BR) gerando resumos corrompidos.
			return truncateUTF8(line, 180)
		}
	}
	return ""
}

func parseKnowledgeArticle(raw map[string]any) KnowledgeArticle {
	tags := toStringSlice(raw["tags"])
	if len(tags) == 0 {
		tags = parseTagsFromJSON(raw["tagsJson"])
	}

	// Author: o endpoint do agent expõe createdBy/lastEditedBy — o campo
	// "author" nunca existiu no DTO (AgentKnowledgeArticleDto), então o
	// autor ficava sempre vazio na UI do agent.
	author := strings.TrimSpace(extractStr(raw, "author"))
	if author == "" {
		author = strings.TrimSpace(extractStr(raw, "createdBy"))
	}
	if author == "" {
		author = strings.TrimSpace(extractStr(raw, "lastEditedBy"))
	}

	// Scope: o DTO plano não tem "scope" — deriva de scopeOrigin (legado) ou,
	// na falta deste, de clientId/siteId (herança site > client > global).
	scope := strings.TrimSpace(extractStr(raw, "scope"))
	if scope == "" {
		switch strings.ToLower(strings.TrimSpace(extractStr(raw, "scopeOrigin"))) {
		case "client":
			scope = "Client"
		case "site":
			scope = "Site"
		case "global":
			scope = "Global"
		default:
			switch {
			case strings.TrimSpace(extractStr(raw, "siteId")) != "":
				scope = "Site"
			case strings.TrimSpace(extractStr(raw, "clientId")) != "":
				scope = "Client"
			default:
				scope = "Global"
			}
		}
	}

	article := KnowledgeArticle{
		ID:            extractStr(raw, "id"),
		Title:         extractStr(raw, "title"),
		Category:      extractStr(raw, "category"),
		Summary:       extractStr(raw, "summary"),
		Content:       extractStr(raw, "content"),
		Tags:          tags,
		Author:        author,
		Scope:         scope,
		PublishedAt:   extractStr(raw, "publishedAt"),
		Difficulty:    extractStr(raw, "difficulty"),
		UpdatedAt:     extractStr(raw, "updatedAt"),
		ParentID:      extractStr(raw, "parentId"),
		SortOrder:     toInt(raw["sortOrder"]),
		IsPage:        toBool(raw["isPage"]),
		Status:        extractStr(raw, "status"),
		VersionNumber: toInt(raw["currentVersionNumber"]),
	}

	if article.Summary == "" {
		article.Summary = buildSummary(article.Content)
	}
	if article.Difficulty == "" {
		switch strings.ToLower(article.Scope) {
		case "global":
			article.Difficulty = "Global"
		case "client":
			article.Difficulty = "Cliente"
		case "site":
			article.Difficulty = "Site"
		}
	}

	article.ReadTimeMin = toInt(raw["readTimeMin"], raw["readTime"])
	if article.ReadTimeMin <= 0 {
		article.ReadTimeMin = estimateReadTimeMin(article.Content)
	}
	if article.UpdatedAt == "" {
		article.UpdatedAt = article.PublishedAt
	}

	return article
}

// knowledgeListPage é o resultado parseado da listagem: artigos + metadados
// de paginação keyset (cursor opaco da API).
type knowledgeListPage struct {
	Articles   []KnowledgeArticle
	NextCursor string
	HasMore    bool
}

// parseKnowledgeListEnvelope aceita os formatos que a API pode devolver:
//   - array direto (respostas antigas, sem paginação);
//   - envelope {"items": [...], "nextCursor": "...", "hasMore": true}.
func parseKnowledgeListEnvelope(body []byte) (knowledgeListPage, error) {
	var direct []map[string]any
	if err := json.Unmarshal(body, &direct); err == nil {
		out := make([]KnowledgeArticle, 0, len(direct))
		for _, item := range direct {
			out = append(out, parseKnowledgeArticle(item))
		}
		return knowledgeListPage{Articles: out}, nil
	}

	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		return knowledgeListPage{}, err
	}

	page := knowledgeListPage{}
	for _, key := range []string{"items", "data", "articles", "knowledge", "result"} {
		arr, ok := envelope[key].([]any)
		if !ok {
			continue
		}
		out := make([]KnowledgeArticle, 0, len(arr))
		for _, entry := range arr {
			if m, ok := entry.(map[string]any); ok {
				out = append(out, parseKnowledgeArticle(m))
			}
		}
		page.Articles = out
		break
	}
	page.NextCursor = strings.TrimSpace(extractStr(envelope, "nextCursor"))
	if h, ok := envelope["hasMore"].(bool); ok {
		page.HasMore = h
	}
	return page, nil
}

func parseKnowledgeListBody(body []byte) ([]KnowledgeArticle, error) {
	page, err := parseKnowledgeListEnvelope(body)
	return page.Articles, err
}

func parseKnowledgeDetailBody(body []byte) (KnowledgeArticle, error) {
	var direct map[string]any
	if err := json.Unmarshal(body, &direct); err != nil {
		return KnowledgeArticle{}, err
	}

	for _, key := range []string{"item", "data", "article", "result"} {
		if inner, ok := direct[key].(map[string]any); ok {
			return parseKnowledgeArticle(inner), nil
		}
	}

	return parseKnowledgeArticle(direct), nil
}

func parseKnowledgePage(raw map[string]any) KnowledgePage {
	page := KnowledgePage{
		ID:           extractStr(raw, "id"),
		ArticleID:    extractStr(raw, "articleId"),
		ParentPageID: extractStr(raw, "parentPageId"),
		Title:        extractStr(raw, "title"),
		Content:      extractStr(raw, "content"),
		SortOrder:    toInt(raw["sortOrder"]),
		ChildCount:   toInt(raw["childCount"]),
	}

	if children, ok := raw["children"].([]any); ok {
		page.Children = make([]KnowledgePage, 0, len(children))
		for _, entry := range children {
			if m, ok := entry.(map[string]any); ok {
				page.Children = append(page.Children, parseKnowledgePage(m))
			}
		}
	}

	return page
}

func parseKnowledgePagesBody(body []byte) ([]KnowledgePage, error) {
	var direct []map[string]any
	if err := json.Unmarshal(body, &direct); err == nil {
		out := make([]KnowledgePage, 0, len(direct))
		for _, item := range direct {
			out = append(out, parseKnowledgePage(item))
		}
		return out, nil
	}

	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}

	for _, key := range []string{"items", "data", "pages", "result"} {
		arr, ok := envelope[key].([]any)
		if !ok {
			continue
		}
		out := make([]KnowledgePage, 0, len(arr))
		for _, entry := range arr {
			if m, ok := entry.(map[string]any); ok {
				out = append(out, parseKnowledgePage(m))
			}
		}
		return out, nil
	}

	return []KnowledgePage{}, nil
}

func knowledgeCacheScope(cfg debug.Config, info AgentInfo) string {
	parts := []string{
		apiScheme(cfg),
		strings.TrimSpace(strings.ToLower(cfg.ApiServer)),
		strings.TrimSpace(strings.ToLower(info.ClientID)),
		strings.TrimSpace(strings.ToLower(info.SiteID)),
		strings.TrimSpace(strings.ToLower(info.AgentID)),
	}
	for i, p := range parts {
		parts[i] = url.QueryEscape(p)
	}
	return strings.Join(parts, ":")
}

// knowledgeScopeControlKey guarda o escopo ativo para limpar caches órfãos
// quando o agente troca de cliente/site/ID (transferência/reprovisionamento).
const knowledgeScopeControlKey = "knowledge:active_scope"

// cleanupOldKnowledgeScope remove caches (quentes e backups) do escopo anterior
// quando o escopo muda — antes eles ficavam no SQLite para sempre.
func (s *Service) cleanupOldKnowledgeScope(scope string) {
	if s.db == nil || strings.TrimSpace(scope) == "" {
		return
	}
	purger, ok := s.db.(CachePurger)
	if !ok {
		return
	}
	var previous string
	if found, err := s.db.CacheGetJSON(knowledgeScopeControlKey, &previous); err == nil && found {
		previous = strings.TrimSpace(previous)
		if previous != "" && previous != scope {
			for _, prefix := range []string{
				"knowledge:list:", "knowledge:detail:", "knowledge:pages:",
				"knowledge:backup:list:", "knowledge:backup:detail:", "knowledge:backup:pages:",
				// Datas dos backups também são específicas do escopo.
				"knowledge:backup:meta:",
			} {
				if err := purger.CacheDeletePrefix(prefix + previous + ":"); err != nil {
					log.Printf("[support] aviso: falha ao limpar cache %s<escopo>: %v", prefix, err)
				}
			}
			s.supportLogf("escopo de knowledge mudou; caches antigos removidos")
		}
	}
	_ = s.db.CacheSetJSON(knowledgeScopeControlKey, scope, 0)
}

// fetchKnowledgeList devolve artigos e se a origem foi o backup offline
// (stale), para a UI avisar e para o enriquecimento não tentar rede.
func (s *Service) fetchKnowledgeList(info AgentInfo, category string) ([]KnowledgeArticle, bool, error) {
	return s.fetchKnowledgeListWithCache(info, category, true)
}

func (s *Service) fetchKnowledgeListWithCache(info AgentInfo, category string, useCache bool) ([]KnowledgeArticle, bool, error) {
	cfg := s.debugConfig()
	base := apiScheme(cfg) + "://" + strings.TrimSpace(cfg.ApiServer)
	if strings.TrimSpace(cfg.ApiServer) == "" || strings.TrimSpace(cfg.AuthToken) == "" {
		return nil, false, fmt.Errorf("configuração de servidor API incompleta: preencha apiServer e token no Debug")
	}
	scope := knowledgeCacheScope(cfg, info)
	s.cleanupOldKnowledgeScope(scope)
	cacheKey := "knowledge:list:" + scope + ":" + url.QueryEscape(strings.TrimSpace(strings.ToLower(category)))

	if useCache && s.db != nil {
		var cached []KnowledgeArticle
		if found, err := s.db.CacheGetJSON(cacheKey, &cached); err == nil && found {
			if cached == nil {
				return []KnowledgeArticle{}, false, nil
			}
			return cached, false, nil
		}
	}

	// Paginação keyset: a API devolve {"items", "nextCursor", "hasMore"} ordenado
	// por UpdatedAt desc + Id. O cursor é opaco — basta repassá-lo. Isso substitui
	// o teto fixo de 500 artigos que truncava tenants maiores silenciosamente.
	articles := make([]KnowledgeArticle, 0, knowledgeListPageSize)
	cursor := ""
	for pageIdx := 0; pageIdx < knowledgeMaxPages; pageIdx++ {
		params := url.Values{}
		if c := strings.TrimSpace(category); c != "" {
			params.Set("category", c)
		}
		if cursor != "" {
			params.Set("cursor", cursor)
		}
		params.Set("limit", strconv.Itoa(knowledgeListPageSize))
		target := base + "/api/v1/agent-auth/knowledge?" + params.Encode()

		ctx := s.ctxOrBackground()

		resp, err := doGetWithRetry(ctx, kbHTTP(), func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
			if err != nil {
				return nil, err
			}
			if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.AuthToken, info.AgentID); err != nil {
				return nil, err
			}
			return req, nil
		})
		if err != nil {
			// Primeira página: stale-if-error — usa o backup local da última
			// carga bem-sucedida para a página continuar utilizável offline.
			if pageIdx == 0 {
				var backup []KnowledgeArticle
				if s.readKnowledgeBackup(cacheKey, &backup) {
					s.supportLogf("servidor inacessivel (%v) — usando backup local da base de conhecimento (%d artigo(s))", err, len(backup))
					return backup, true, nil
				}
				return nil, false, fmt.Errorf("falha ao buscar artigos da base de conhecimento: %w", err)
			}
			// Página intermediária falhou: segue com o parcial (best-effort, logado).
			s.supportLogf("página %d da base de conhecimento falhou (%v) — seguindo com %d artigo(s) parciais", pageIdx+1, err, len(articles))
			break
		}

		// Teto de leitura: respostas gigantes/malformadas nao podem estourar memoria.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, knowledgeMaxBodyBytes))
		resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			if pageIdx == 0 {
				if resp.StatusCode >= 500 {
					var backup []KnowledgeArticle
					if s.readKnowledgeBackup(cacheKey, &backup) {
						s.supportLogf("HTTP %d do servidor — usando backup local da base de conhecimento (%d artigo(s))", resp.StatusCode, len(backup))
						return backup, true, nil
					}
				}
				return nil, false, fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
			}
			s.supportLogf("HTTP %s na página %d da base de conhecimento — seguindo com %d artigo(s) parciais", resp.Status, pageIdx+1, len(articles))
			break
		}

		pg, err := parseKnowledgeListEnvelope(body)
		if err != nil {
			if pageIdx == 0 {
				return nil, false, fmt.Errorf("resposta invalida ao listar artigos: %w", err)
			}
			s.supportLogf("resposta invalida na página %d da base de conhecimento — seguindo com %d artigo(s) parciais", pageIdx+1, len(articles))
			break
		}

		articles = append(articles, pg.Articles...)

		if !pg.HasMore || pg.NextCursor == "" {
			break
		}
		cursor = pg.NextCursor
	}

	// Dedupe por ID: segurança contra sobreposição de páginas (cursor afetado
	// por atualizações concorrentes de UpdatedAt entre páginas).
	seen := make(map[string]struct{}, len(articles))
	deduped := articles[:0]
	for _, a := range articles {
		if a.ID == "" {
			continue
		}
		if _, dup := seen[a.ID]; dup {
			continue
		}
		seen[a.ID] = struct{}{}
		deduped = append(deduped, a)
	}
	articles = deduped
	if articles == nil {
		articles = []KnowledgeArticle{}
	}

	if s.db != nil {
		if err := s.db.CacheSetJSON(cacheKey, articles, knowledgeListCacheTTL); err != nil {
			log.Printf("[support] aviso: falha ao salvar cache de knowledge list: %v", err)
		}
	}
	s.saveKnowledgeBackup(cacheKey, articles)
	s.saveKnowledgeListSavedAt(cacheKey)

	return articles, false, nil
}

func (s *Service) RefreshKnowledgeBase() error {
	if !s.featureEnabled(s.knowledgeEnabled()) {
		return nil
	}

	s.knowledgeMu.Lock()
	// Flag in-flight: o lock era liberado antes do fetch e dois refreshes
	// concorrentes passavam pela checagem de intervalo mínimo.
	if s.knowledgeRefreshing {
		s.knowledgeMu.Unlock()
		s.supportLogf("refresh da knowledge base ignorado: já existe um refresh em andamento")
		return nil
	}
	if time.Since(s.lastKnowledgeRefresh) < knowledgeMinRefreshInterval {
		s.knowledgeMu.Unlock()
		s.supportLogf("refresh da knowledge base ignorado: intervalo mínimo de %s não decorrido (último refresh há %s)",
			knowledgeMinRefreshInterval, time.Since(s.lastKnowledgeRefresh).Round(time.Second))
		return nil
	}
	s.knowledgeRefreshing = true
	s.knowledgeMu.Unlock()
	defer func() {
		s.knowledgeMu.Lock()
		s.knowledgeRefreshing = false
		s.knowledgeMu.Unlock()
	}()

	info, err := s.fetchAgentContext()
	if err != nil {
		s.supportLogf("falha ao resolver contexto para refresh da knowledge base: %v", err)
		return err
	}

	cfg := s.debugConfig()
	scope := knowledgeCacheScope(cfg, info)
	s.cleanupOldKnowledgeScope(scope)
	// Purge COMPLETO do escopo: antes só a lista sem categoria era limpa e
	// listas filtradas/detalhes/páginas ficavam defasados até 6h após o refresh.
	// Backups (knowledge:backup:...) são preservados para o fallback offline.
	if s.db != nil {
		if purger, ok := s.db.(CachePurger); ok {
			for _, prefix := range []string{"knowledge:list:", "knowledge:detail:", "knowledge:pages:"} {
				if err := purger.CacheDeletePrefix(prefix + scope + ":"); err != nil {
					log.Printf("[support] aviso: falha ao limpar cache %s<scope>: %v", prefix, err)
				}
			}
		} else {
			// Fallback: limpa apenas a chave conhecida (sem interface de purge).
			cacheKey := "knowledge:list:" + scope + ":" + url.QueryEscape("")
			if err := s.db.CacheDelete(cacheKey); err != nil {
				log.Printf("[support] aviso: falha ao limpar cache de knowledge list: %v", err)
			}
		}
	}

	articles, _, err := s.fetchKnowledgeListWithCache(info, "", false)
	if err != nil {
		s.supportLogf("falha ao recarregar base de conhecimento: %v", err)
		return err
	}

	s.knowledgeMu.Lock()
	s.lastKnowledgeRefresh = time.Now()
	s.knowledgeMu.Unlock()

	s.supportLogf("base de conhecimento recarregada: %d artigo(s)", len(articles))
	return nil
}

func (s *Service) fetchKnowledgeDetail(info AgentInfo, articleID string) (KnowledgeArticle, error) {
	articleID = strings.TrimSpace(articleID)
	if articleID == "" {
		return KnowledgeArticle{}, fmt.Errorf("articleId inválido")
	}

	cfg := s.debugConfig()
	if strings.TrimSpace(cfg.ApiServer) == "" || strings.TrimSpace(cfg.AuthToken) == "" {
		return KnowledgeArticle{}, fmt.Errorf("configuração de servidor API incompleta: preencha apiServer e token no Debug")
	}
	cacheKey := "knowledge:detail:" + knowledgeCacheScope(cfg, info) + ":" + url.QueryEscape(strings.ToLower(articleID))

	if s.db != nil {
		var cached KnowledgeArticle
		if found, err := s.db.CacheGetJSON(cacheKey, &cached); err == nil && found {
			if strings.TrimSpace(cached.ID) != "" {
				return cached, nil
			}
		}
	}

	target := apiScheme(cfg) + "://" + strings.TrimSpace(cfg.ApiServer) + "/api/v1/agent-auth/knowledge/" + url.PathEscape(articleID)

	ctx := s.ctxOrBackground()

	resp, err := doGetWithRetry(ctx, kbHTTP(), func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.AuthToken, info.AgentID); err != nil {
			return nil, err
		}
		return req, nil
	})
	if err != nil {
		var backup KnowledgeArticle
		if s.readKnowledgeBackup(cacheKey, &backup) && strings.TrimSpace(backup.ID) != "" {
			s.supportLogf("servidor inacessivel (%v) — usando backup local do artigo %s", err, articleID)
			return backup, nil
		}
		return KnowledgeArticle{}, fmt.Errorf("falha ao buscar detalhe do artigo: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, knowledgeMaxBodyBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode >= 500 {
			var backup KnowledgeArticle
			if s.readKnowledgeBackup(cacheKey, &backup) && strings.TrimSpace(backup.ID) != "" {
				s.supportLogf("HTTP %d do servidor — usando backup local do artigo %s", resp.StatusCode, articleID)
				return backup, nil
			}
		}
		return KnowledgeArticle{}, fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	article, err := parseKnowledgeDetailBody(body)
	if err != nil {
		return KnowledgeArticle{}, fmt.Errorf("resposta invalida no detalhe do artigo: %w", err)
	}

	if s.db != nil && strings.TrimSpace(article.ID) != "" {
		if err := s.db.CacheSetJSON(cacheKey, article, knowledgeDetailCacheTTL); err != nil {
			log.Printf("[support] aviso: falha ao salvar cache de knowledge detail: %v", err)
		}
	}
	if strings.TrimSpace(article.ID) != "" {
		s.saveKnowledgeBackup(cacheKey, article)
	}

	return article, nil
}

func (s *Service) fetchKnowledgePages(info AgentInfo, articleID string) ([]KnowledgePage, error) {
	articleID = strings.TrimSpace(articleID)
	if articleID == "" {
		return nil, fmt.Errorf("articleId inválido")
	}

	cfg := s.debugConfig()
	if strings.TrimSpace(cfg.ApiServer) == "" || strings.TrimSpace(cfg.AuthToken) == "" {
		return nil, fmt.Errorf("configuração de servidor API incompleta: preencha apiServer e token no Debug")
	}
	cacheKey := "knowledge:pages:" + knowledgeCacheScope(cfg, info) + ":" + url.QueryEscape(strings.ToLower(articleID))

	if s.db != nil {
		var cached []KnowledgePage
		if found, err := s.db.CacheGetJSON(cacheKey, &cached); err == nil && found {
			if cached == nil {
				return []KnowledgePage{}, nil
			}
			return cached, nil
		}
	}

	target := apiScheme(cfg) + "://" + strings.TrimSpace(cfg.ApiServer) + "/api/v1/agent-auth/knowledge/" + url.PathEscape(articleID) + "/pages"

	ctx := s.ctxOrBackground()

	resp, err := doGetWithRetry(ctx, kbHTTP(), func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.AuthToken, info.AgentID); err != nil {
			return nil, err
		}
		return req, nil
	})
	if err != nil {
		var backup []KnowledgePage
		if s.readKnowledgeBackup(cacheKey, &backup) {
			s.supportLogf("servidor inacessivel (%v) — usando backup local das páginas do artigo %s", err, articleID)
			return backup, nil
		}
		return nil, fmt.Errorf("falha ao buscar páginas do artigo: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, knowledgeMaxBodyBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode >= 500 {
			var backup []KnowledgePage
			if s.readKnowledgeBackup(cacheKey, &backup) {
				s.supportLogf("HTTP %d do servidor — usando backup local das páginas do artigo %s", resp.StatusCode, articleID)
				return backup, nil
			}
		}
		return nil, fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	pages, err := parseKnowledgePagesBody(body)
	if err != nil {
		return nil, fmt.Errorf("resposta invalida ao listar páginas do artigo: %w", err)
	}
	if pages == nil {
		pages = []KnowledgePage{}
	}

	if s.db != nil {
		if err := s.db.CacheSetJSON(cacheKey, pages, knowledgeDetailCacheTTL); err != nil {
			log.Printf("[support] aviso: falha ao salvar cache de knowledge pages: %v", err)
		}
	}
	s.saveKnowledgeBackup(cacheKey, pages)

	return pages, nil
}

// KnowledgeArticleList é o retorno da listagem para o binding da UI, com o flag
// de cache offline (stale) usado para avisar que os dados podem estar antigos.
type KnowledgeArticleList struct {
	Articles []KnowledgeArticle `json:"articles"`
	Stale    bool               `json:"stale"`
	CachedAt string             `json:"cachedAt,omitempty"`
}

// GetKnowledgeBaseArticles mantém a assinatura antiga (sem erro) para
// compatibilidade — a UI usa GetKnowledgeBaseArticleList, que propaga erro.
func (s *Service) GetKnowledgeBaseArticles() []KnowledgeArticle {
	result, _ := s.getAllKnowledgeArticles("")
	if result.Articles == nil {
		// Compat: a assinatura antiga nunca devolvia nil (virava "null" no JSON).
		return []KnowledgeArticle{}
	}
	return result.Articles
}

// GetKnowledgeBaseArticleList devolve os artigos + indicador de cache offline.
func (s *Service) GetKnowledgeBaseArticleList() (KnowledgeArticleList, error) {
	return s.getAllKnowledgeArticles("")
}

func (s *Service) getAllKnowledgeArticles(category string) (KnowledgeArticleList, error) {
	if !s.featureEnabled(s.knowledgeEnabled()) {
		s.supportLogf("base de conhecimento desabilitada pela configuracao do agente")
		return KnowledgeArticleList{Articles: []KnowledgeArticle{}}, nil
	}

	info, err := s.fetchAgentContext()
	if err != nil {
		s.supportLogf("falha ao resolver contexto para knowledge base: %v", err)
		return KnowledgeArticleList{}, err
	}

	articles, fromBackup, err := s.fetchKnowledgeList(info, category)
	if err != nil {
		s.supportLogf("falha ao listar base de conhecimento: %v", err)
		return KnowledgeArticleList{}, err
	}

	// Data do backup: exibida no aviso de modo somente consulta.
	cachedAt := ""
	if fromBackup {
		cfg := s.debugConfig()
		cacheKey := "knowledge:list:" + knowledgeCacheScope(cfg, info) + ":" + url.QueryEscape(strings.TrimSpace(strings.ToLower(category)))
		cachedAt = s.readKnowledgeListSavedAt(cacheKey)
	}

	return KnowledgeArticleList{
		Articles: s.enrichKnowledgeArticles(info, articles, !fromBackup),
		Stale:    fromBackup,
		CachedAt: cachedAt,
	}, nil
}

// knowledgeBackupSavedAtKey guarda a data do backup FORA dos prefixos que o
// refresh limpa, para sobreviver junto com o próprio backup.
func knowledgeBackupSavedAtKey(cacheKey string) string {
	if !strings.HasPrefix(cacheKey, "knowledge:list:") {
		return ""
	}
	return strings.Replace(cacheKey, "knowledge:list:", "knowledge:backup:meta:", 1)
}

func (s *Service) saveKnowledgeListSavedAt(cacheKey string) {
	metaKey := knowledgeBackupSavedAtKey(cacheKey)
	if s.db == nil || metaKey == "" {
		return
	}
	if err := s.db.CacheSetJSON(metaKey, time.Now().UTC().Format(time.RFC3339), knowledgeBackupTTL); err != nil {
		log.Printf("[support] aviso: falha ao salvar data do backup de knowledge: %v", err)
	}
}

func (s *Service) readKnowledgeListSavedAt(cacheKey string) string {
	metaKey := knowledgeBackupSavedAtKey(cacheKey)
	if s.db == nil || metaKey == "" {
		return ""
	}
	var savedAt string
	found, err := s.db.CacheGetJSON(metaKey, &savedAt)
	if err != nil || !found {
		return ""
	}
	return strings.TrimSpace(savedAt)
}

// readCachedKnowledgeDetail lê um detalhe SEM rede (cache quente ou backup),
// usado quando a listagem veio do backup offline.
func (s *Service) readCachedKnowledgeDetail(info AgentInfo, articleID string) (KnowledgeArticle, error) {
	cfg := s.debugConfig()
	cacheKey := "knowledge:detail:" + knowledgeCacheScope(cfg, info) + ":" + url.QueryEscape(strings.ToLower(articleID))
	if s.db != nil {
		var cached KnowledgeArticle
		if found, err := s.db.CacheGetJSON(cacheKey, &cached); err == nil && found && strings.TrimSpace(cached.ID) != "" {
			return cached, nil
		}
	}
	var backup KnowledgeArticle
	if s.readKnowledgeBackup(cacheKey, &backup) && strings.TrimSpace(backup.ID) != "" {
		return backup, nil
	}
	return KnowledgeArticle{}, fmt.Errorf("detalhe do artigo %s não está em cache", articleID)
}

// enrichKnowledgeArticles completa conteúdo/resumo/tags dos artigos que vieram
// sem 'content' na listagem (N+1 controlado), em PARALELO com teto de
// concorrência. Com allowNetwork=false (lista veio do backup offline) usa
// APENAS o cache local: sem isso cada artigo dispararia 2×15s de timeout.
func (s *Service) enrichKnowledgeArticles(info AgentInfo, articles []KnowledgeArticle, allowNetwork bool) []KnowledgeArticle {
	type detailJob struct {
		idx int
		id  string
	}
	jobs := make([]detailJob, 0, len(articles))
	for i := range articles {
		if strings.TrimSpace(articles[i].Content) != "" || strings.TrimSpace(articles[i].ID) == "" {
			continue
		}
		jobs = append(jobs, detailJob{idx: i, id: articles[i].ID})
	}
	if len(jobs) == 0 {
		return articles
	}

	sem := make(chan struct{}, knowledgeDetailWorkers)
	var wg sync.WaitGroup
	for _, job := range jobs {
		wg.Add(1)
		go func(j detailJob) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var detail KnowledgeArticle
			var err error
			if allowNetwork {
				detail, err = s.fetchKnowledgeDetail(info, j.id)
			} else {
				detail, err = s.readCachedKnowledgeDetail(info, j.id)
			}
			if err != nil {
				s.supportLogf("falha ao carregar markdown do artigo %s: %v", j.id, err)
				return
			}
			// Escrita em índices distintos do slice: seguro sem lock.
			if strings.TrimSpace(detail.Content) != "" {
				articles[j.idx].Content = detail.Content
			}
			if strings.TrimSpace(articles[j.idx].Summary) == "" {
				articles[j.idx].Summary = detail.Summary
			}
			if len(articles[j.idx].Tags) == 0 {
				articles[j.idx].Tags = detail.Tags
			}
			if articles[j.idx].VersionNumber == 0 {
				articles[j.idx].VersionNumber = detail.VersionNumber
			}
		}(job)
	}
	wg.Wait()

	return articles
}

// GetKnowledgeArticles returns articles optionally filtered by category.
// Usa o MESMO enriquecimento da listagem principal — antes o conteúdo/summary
// ficavam vazios no caminho com filtro de categoria.
func (s *Service) GetKnowledgeArticles(category string) ([]KnowledgeArticle, error) {
	result, err := s.getAllKnowledgeArticles(category)
	return result.Articles, err
}

// GetKnowledgeArticleDetails returns a single article by ID.
func (s *Service) GetKnowledgeArticleDetails(articleID string) (KnowledgeArticle, error) {
	if !s.featureEnabled(s.knowledgeEnabled()) {
		s.supportLogf("base de conhecimento desabilitada pela configuracao do agente")
		return KnowledgeArticle{}, fmt.Errorf("base de conhecimento desabilitada pela configuração do agente")
	}
	info, err := s.fetchAgentContext()
	if err != nil {
		s.supportLogf("falha ao resolver contexto para knowledge detail: %v", err)
		return KnowledgeArticle{}, err
	}
	return s.fetchKnowledgeDetail(info, articleID)
}

// GetKnowledgeArticlePages returns the nested sub-pages tree of an article (Notion-style).
func (s *Service) GetKnowledgeArticlePages(articleID string) ([]KnowledgePage, error) {
	if !s.featureEnabled(s.knowledgeEnabled()) {
		s.supportLogf("base de conhecimento desabilitada pela configuracao do agente")
		return []KnowledgePage{}, nil
	}
	info, err := s.fetchAgentContext()
	if err != nil {
		s.supportLogf("falha ao resolver contexto para knowledge pages: %v", err)
		return nil, err
	}
	return s.fetchKnowledgePages(info, articleID)
}
