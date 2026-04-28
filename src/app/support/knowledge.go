package support

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"discovery/app/debug"
	"discovery/app/netutil"
	"discovery/internal/tlsutil"
)

const (
	knowledgeListCacheTTL   = 5 * time.Minute
	knowledgeDetailCacheTTL = 30 * time.Minute
)

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

func buildSummary(content string) string {
	if strings.TrimSpace(content) == "" {
		return ""
	}
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimLeft(line, "#*-0123456789. "))
		if line != "" {
			if len(line) > 180 {
				return line[:180] + "..."
			}
			return line
		}
	}
	return ""
}

func parseKnowledgeArticle(raw map[string]any) KnowledgeArticle {
	article := KnowledgeArticle{
		ID:          extractStr(raw, "id"),
		Title:       extractStr(raw, "title"),
		Category:    extractStr(raw, "category"),
		Summary:     extractStr(raw, "summary"),
		Content:     extractStr(raw, "content"),
		Tags:        toStringSlice(raw["tags"]),
		Author:      extractStr(raw, "author"),
		Scope:       extractStr(raw, "scope"),
		PublishedAt: extractStr(raw, "publishedAt"),
		Difficulty:  extractStr(raw, "difficulty"),
		UpdatedAt:   extractStr(raw, "updatedAt"),
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

func parseKnowledgeListBody(body []byte) ([]KnowledgeArticle, error) {
	var direct []map[string]any
	if err := json.Unmarshal(body, &direct); err == nil {
		out := make([]KnowledgeArticle, 0, len(direct))
		for _, item := range direct {
			out = append(out, parseKnowledgeArticle(item))
		}
		return out, nil
	}

	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}

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
		return out, nil
	}

	return []KnowledgeArticle{}, nil
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

func knowledgeCacheScope(cfg debug.Config, info AgentInfo) string {
	parts := []string{
		strings.TrimSpace(strings.ToLower(cfg.ApiScheme)),
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

func (s *Service) fetchKnowledgeList(info AgentInfo, category string) ([]KnowledgeArticle, error) {
	return s.fetchKnowledgeListWithCache(info, category, true)
}

func (s *Service) fetchKnowledgeListWithCache(info AgentInfo, category string, useCache bool) ([]KnowledgeArticle, error) {
	cfg := s.debugConfig()
	base := strings.TrimSpace(strings.ToLower(cfg.ApiScheme)) + "://" + strings.TrimSpace(cfg.ApiServer)
	if strings.TrimSpace(cfg.ApiServer) == "" || strings.TrimSpace(cfg.AuthToken) == "" {
		return nil, fmt.Errorf("configuracao de servidor API incompleta: preencha apiServer e token no Debug")
	}
	cacheKey := "knowledge:list:" + knowledgeCacheScope(cfg, info) + ":" + url.QueryEscape(strings.TrimSpace(strings.ToLower(category)))

	if useCache && s.db != nil {
		var cached []KnowledgeArticle
		if found, err := s.db.CacheGetJSON(cacheKey, &cached); err == nil && found {
			if cached == nil {
				return []KnowledgeArticle{}, nil
			}
			return cached, nil
		}
	}

	path := "/api/agent-auth/knowledge"
	if c := strings.TrimSpace(category); c != "" {
		path += "?category=" + url.QueryEscape(c)
	}
	target := base + path

	ctx := s.ctxOrBackground()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("URL invalida: %w", err)
	}
	netutil.SetAgentAuthHeaders(req, cfg.AuthToken)

	resp, err := tlsutil.NewHTTPClient(15 * time.Second).Do(req)
	if err != nil {
		return nil, fmt.Errorf("falha ao buscar artigos da base de conhecimento: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	articles, err := parseKnowledgeListBody(body)
	if err != nil {
		return nil, fmt.Errorf("resposta invalida ao listar artigos: %w", err)
	}
	if articles == nil {
		articles = []KnowledgeArticle{}
	}

	if s.db != nil {
		if err := s.db.CacheSetJSON(cacheKey, articles, knowledgeListCacheTTL); err != nil {
			log.Printf("[support] aviso: falha ao salvar cache de knowledge list: %v", err)
		}
	}

	return articles, nil
}

func (s *Service) RefreshKnowledgeBase() error {
	if !s.featureEnabled(s.knowledgeEnabled()) {
		return nil
	}

	info, err := s.fetchAgentContext()
	if err != nil {
		s.supportLogf("falha ao resolver contexto para refresh da knowledge base: %v", err)
		return err
	}

	cfg := s.debugConfig()
	cacheKey := "knowledge:list:" + knowledgeCacheScope(cfg, info) + ":" + url.QueryEscape("")
	if s.db != nil {
		if err := s.db.CacheDelete(cacheKey); err != nil {
			log.Printf("[support] aviso: falha ao limpar cache de knowledge list: %v", err)
		}
	}

	articles, err := s.fetchKnowledgeListWithCache(info, "", false)
	if err != nil {
		s.supportLogf("falha ao recarregar base de conhecimento: %v", err)
		return err
	}

	s.supportLogf("base de conhecimento recarregada: %d artigo(s)", len(articles))
	return nil
}

func (s *Service) fetchKnowledgeDetail(info AgentInfo, articleID string) (KnowledgeArticle, error) {
	articleID = strings.TrimSpace(articleID)
	if articleID == "" {
		return KnowledgeArticle{}, fmt.Errorf("articleId invalido")
	}

	cfg := s.debugConfig()
	if strings.TrimSpace(cfg.ApiServer) == "" || strings.TrimSpace(cfg.AuthToken) == "" {
		return KnowledgeArticle{}, fmt.Errorf("configuracao de servidor API incompleta: preencha apiServer e token no Debug")
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

	target := strings.TrimSpace(strings.ToLower(cfg.ApiScheme)) + "://" + strings.TrimSpace(cfg.ApiServer) + "/api/agent-auth/knowledge/" + url.PathEscape(articleID)

	ctx := s.ctxOrBackground()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return KnowledgeArticle{}, fmt.Errorf("URL invalida: %w", err)
	}
	netutil.SetAgentAuthHeaders(req, cfg.AuthToken)

	resp, err := tlsutil.NewHTTPClient(15 * time.Second).Do(req)
	if err != nil {
		return KnowledgeArticle{}, fmt.Errorf("falha ao buscar detalhe do artigo: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
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

	return article, nil
}

// GetKnowledgeBaseArticles returns knowledge-base articles available to the authenticated agent.
func (s *Service) GetKnowledgeBaseArticles() []KnowledgeArticle {
	if !s.featureEnabled(s.knowledgeEnabled()) {
		s.supportLogf("base de conhecimento desabilitada pela configuracao do agente")
		return []KnowledgeArticle{}
	}

	info, err := s.fetchAgentContext()
	if err != nil {
		s.supportLogf("falha ao resolver contexto para knowledge base: %v", err)
		return []KnowledgeArticle{}
	}

	articles, err := s.fetchKnowledgeList(info, "")
	if err != nil {
		s.supportLogf("falha ao listar base de conhecimento: %v", err)
		return []KnowledgeArticle{}
	}

	for i := range articles {
		if strings.TrimSpace(articles[i].Content) != "" || strings.TrimSpace(articles[i].ID) == "" {
			continue
		}
		detail, err := s.fetchKnowledgeDetail(info, articles[i].ID)
		if err != nil {
			s.supportLogf("falha ao carregar markdown do artigo %s: %v", articles[i].ID, err)
			continue
		}
		if strings.TrimSpace(detail.Content) != "" {
			articles[i].Content = detail.Content
		}
		if strings.TrimSpace(articles[i].Summary) == "" {
			articles[i].Summary = detail.Summary
		}
		if len(articles[i].Tags) == 0 {
			articles[i].Tags = detail.Tags
		}
	}

	return articles
}

// GetKnowledgeArticles returns articles optionally filtered by category.
func (s *Service) GetKnowledgeArticles(category string) ([]KnowledgeArticle, error) {
	if !s.featureEnabled(s.knowledgeEnabled()) {
		s.supportLogf("base de conhecimento desabilitada pela configuracao do agente")
		return []KnowledgeArticle{}, nil
	}
	info, err := s.fetchAgentContext()
	if err != nil {
		s.supportLogf("falha ao resolver contexto para knowledge base: %v", err)
		return nil, err
	}
	return s.fetchKnowledgeList(info, category)
}

// GetKnowledgeArticleDetails returns a single article by ID.
func (s *Service) GetKnowledgeArticleDetails(articleID string) (KnowledgeArticle, error) {
	info, err := s.fetchAgentContext()
	if err != nil {
		s.supportLogf("falha ao resolver contexto para knowledge detail: %v", err)
		return KnowledgeArticle{}, err
	}
	return s.fetchKnowledgeDetail(info, articleID)
}

// SearchKnowledgeBaseArticles filters articles by title/category/tags/content.
func (s *Service) SearchKnowledgeBaseArticles(query string) []KnowledgeArticle {
	articles := s.GetKnowledgeBaseArticles()
	q := strings.TrimSpace(strings.ToLower(query))
	if q == "" {
		return articles
	}

	matches := make([]KnowledgeArticle, 0, len(articles))
	for _, article := range articles {
		if strings.Contains(strings.ToLower(article.Title), q) ||
			strings.Contains(strings.ToLower(article.Category), q) ||
			strings.Contains(strings.ToLower(article.Summary), q) ||
			strings.Contains(strings.ToLower(article.Content), q) ||
			strings.Contains(strings.ToLower(article.Author), q) ||
			strings.Contains(strings.ToLower(article.Scope), q) {
			matches = append(matches, article)
			continue
		}

		for _, tag := range article.Tags {
			if strings.Contains(strings.ToLower(tag), q) {
				matches = append(matches, article)
				break
			}
		}
	}

	return matches
}
