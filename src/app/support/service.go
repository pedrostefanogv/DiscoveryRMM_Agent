package support

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"discovery/app/debug"
	"discovery/app/netutil"
	"discovery/app/supportmeta"
	"discovery/internal/tlsutil"
)

var guidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type AgentInfo = supportmeta.AgentInfo

type APIWorkflowState = supportmeta.APIWorkflowState

type TicketPriority = supportmeta.TicketPriority

type APITicket = supportmeta.APITicket

type TicketComment = supportmeta.TicketComment

type CreateTicketInput = supportmeta.CreateTicketInput

type CloseTicketInput = supportmeta.CloseTicketInput

type KnowledgeArticle = supportmeta.KnowledgeArticle

// AgentInfoCache handles cached agent identity values.
type AgentInfoCache interface {
	Get() (AgentInfo, bool)
	Set(AgentInfo)
	Invalidate()
}

// CacheDB exposes the cache operations needed by support.
type CacheDB interface {
	CacheGetJSON(key string, out any) (bool, error)
	CacheSetJSON(key string, value any, ttl time.Duration) error
	CacheDelete(key string) error
}

// Options wires the support service.
type Options struct {
	Logf             func(string)
	Ctx              func() context.Context
	DB               CacheDB
	AgentInfo        AgentInfoCache
	DebugConfig      func() debug.Config
	FeatureEnabled   func(*bool) bool
	SupportEnabled   func() *bool
	KnowledgeEnabled func() *bool
}

// Service handles support and knowledge base APIs.
type Service struct {
	logf             func(string)
	ctx              func() context.Context
	db               CacheDB
	agentInfo        AgentInfoCache
	debugConfig      func() debug.Config
	featureEnabled   func(*bool) bool
	supportEnabled   func() *bool
	knowledgeEnabled func() *bool
}

// NewService builds a support service.
func NewService(opts Options) *Service {
	logf := opts.Logf
	if logf == nil {
		logf = func(string) {}
	}
	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background
	}
	debugConfig := opts.DebugConfig
	if debugConfig == nil {
		debugConfig = func() debug.Config { return debug.Config{} }
	}
	featureEnabled := opts.FeatureEnabled
	if featureEnabled == nil {
		featureEnabled = func(flag *bool) bool { return flag == nil || *flag }
	}
	supportEnabled := opts.SupportEnabled
	if supportEnabled == nil {
		supportEnabled = func() *bool { return nil }
	}
	knowledgeEnabled := opts.KnowledgeEnabled
	if knowledgeEnabled == nil {
		knowledgeEnabled = func() *bool { return nil }
	}
	return &Service{
		logf:             logf,
		ctx:              ctx,
		db:               opts.DB,
		agentInfo:        opts.AgentInfo,
		debugConfig:      debugConfig,
		featureEnabled:   featureEnabled,
		supportEnabled:   supportEnabled,
		knowledgeEnabled: knowledgeEnabled,
	}
}

func (s *Service) supportLogf(format string, args ...any) {
	s.logf("[support] " + fmt.Sprintf(format, args...))
}

func shortBodyForLog(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 400 {
		return s[:400] + "..."
	}
	return s
}

func normalizePriority(v int) int {
	if v < 1 || v > 4 {
		return 2
	}
	return v
}

func priorityIntToLabel(v int) string {
	switch normalizePriority(v) {
	case 1:
		return "Low"
	case 3:
		return "High"
	case 4:
		return "Critical"
	default:
		return "Medium"
	}
}

func priorityLabelToInt(label string) int {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "1", "low", "baixa":
		return 1
	case "3", "high", "alta":
		return 3
	case "4", "critical", "critica", "crítica":
		return 4
	case "2", "medium", "media", "média":
		fallthrough
	default:
		return 2
	}
}

func toInt(values ...any) int {
	for _, v := range values {
		switch n := v.(type) {
		case float64:
			return int(n)
		case float32:
			return int(n)
		case int:
			return n
		case int64:
			return int(n)
		case json.Number:
			if i, err := n.Int64(); err == nil {
				return int(i)
			}
		case string:
			s := strings.TrimSpace(n)
			if s == "" {
				continue
			}
			var parsed int
			if _, err := fmt.Sscanf(s, "%d", &parsed); err == nil {
				return parsed
			}
		}
	}
	return 0
}

func toBool(values ...any) bool {
	for _, v := range values {
		switch b := v.(type) {
		case bool:
			return b
		case string:
			s := strings.ToLower(strings.TrimSpace(b))
			if s == "true" || s == "1" || s == "yes" || s == "sim" {
				return true
			}
			if s == "false" || s == "0" || s == "no" || s == "nao" || s == "não" {
				return false
			}
		case float64:
			return b != 0
		case int:
			return b != 0
		}
	}
	return false
}

func extractAgentInfoFromJSON(body []byte, cfg debug.Config) (AgentInfo, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return AgentInfo{}, fmt.Errorf("resposta inválida de /api/agent-auth/me: %w", err)
	}

	asMap := func(v any) map[string]any {
		m, _ := v.(map[string]any)
		return m
	}
	getStr := func(m map[string]any, keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k]; ok {
				s := strings.TrimSpace(fmt.Sprint(v))
				if s != "" && s != "<nil>" {
					return s
				}
			}
		}
		return ""
	}

	candidates := []map[string]any{raw}
	for _, key := range []string{"data", "agent", "result", "payload"} {
		if m := asMap(raw[key]); m != nil {
			candidates = append(candidates, m)
		}
	}

	info := AgentInfo{}
	for _, c := range candidates {
		if info.AgentID == "" {
			info.AgentID = getStr(c, "agentId", "agentID", "id")
		}
		if info.ClientID == "" {
			info.ClientID = getStr(c, "clientId", "clientID")
		}
		if info.ClientID == "" {
			if client := asMap(c["client"]); client != nil {
				info.ClientID = getStr(client, "id", "clientId", "clientID")
			}
		}
		if info.SiteID == "" {
			info.SiteID = getStr(c, "siteId", "siteID")
		}
		if info.SiteID == "" {
			if site := asMap(c["site"]); site != nil {
				info.SiteID = getStr(site, "id", "siteId", "siteID")
			}
		}
		if info.Hostname == "" {
			info.Hostname = getStr(c, "hostname", "hostName")
		}
		if info.Name == "" {
			info.Name = getStr(c, "displayName", "name")
		}
	}

	if s := strings.TrimSpace(cfg.AgentID); s != "" {
		info.AgentID = s
	}

	info.AgentID = strings.TrimSpace(info.AgentID)
	info.ClientID = strings.TrimSpace(info.ClientID)
	info.SiteID = strings.TrimSpace(info.SiteID)
	info.Hostname = strings.TrimSpace(info.Hostname)
	info.Name = strings.TrimSpace(info.Name)

	return info, nil
}

// fetchAgentContext resolves clientId/siteId from /api/agent-auth/me (cached).
func (s *Service) fetchAgentContext() (AgentInfo, error) {
	if info, ok := s.agentInfo.Get(); ok {
		if strings.TrimSpace(info.ClientID) != "" {
			return info, nil
		}
		s.supportLogf("cache em memória sem clientId; ignorando e recarregando do servidor")
		s.agentInfo.Invalidate()
	}

	if s.db != nil {
		var cached AgentInfo
		found, err := s.db.CacheGetJSON("agent_info", &cached)
		if err == nil && found {
			if strings.TrimSpace(cached.ClientID) != "" {
				s.agentInfo.Set(cached)
				return cached, nil
			}
			s.supportLogf("cache SQLite sem clientId; removendo entrada e atualizando do servidor")
			if delErr := s.db.CacheDelete("agent_info"); delErr != nil {
				log.Printf("[support] aviso: falha ao limpar cache SQLite agent_info inválido: %v", delErr)
			}
		}
	}

	cfg := s.debugConfig()
	cfg.ApiScheme = strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
	cfg.ApiServer = strings.TrimSpace(cfg.ApiServer)
	if cfg.ApiServer == "" || strings.TrimSpace(cfg.AuthToken) == "" {
		err := fmt.Errorf("configuração de servidor API incompleta: preencha apiServer e token no Debug")
		s.supportLogf("falha ao resolver contexto do agente: %v", err)
		return AgentInfo{}, err
	}
	if cfg.ApiScheme != "http" && cfg.ApiScheme != "https" {
		err := fmt.Errorf("apiScheme inválido: use http ou https")
		s.supportLogf("falha ao resolver contexto do agente: %v", err)
		return AgentInfo{}, err
	}

	ctx := s.ctxOrBackground()
	target := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/agent-auth/me"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		wrapped := fmt.Errorf("URL inválida: %w", err)
		s.supportLogf("falha ao montar request de contexto do agente: %v", wrapped)
		return AgentInfo{}, wrapped
	}
	netutil.SetAgentAuthHeaders(req, cfg.AuthToken)

	resp, err := tlsutil.NewHTTPClient(10 * time.Second).Do(req)
	if err != nil {
		wrapped := fmt.Errorf("falha ao conectar em %s: %w", target, err)
		s.supportLogf("erro HTTP ao resolver contexto do agente: %v", wrapped)
		return AgentInfo{}, wrapped
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		wrapped := fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
		s.supportLogf("/api/agent-auth/me retornou erro: %v", wrapped)
		return AgentInfo{}, wrapped
	}

	info, err := extractAgentInfoFromJSON(body, cfg)
	if err != nil {
		s.supportLogf("falha ao decodificar /api/agent-auth/me: %v", err)
		return AgentInfo{}, err
	}
	if info.ClientID == "" {
		err := fmt.Errorf("clientId não retornado por /api/agent-auth/me: verifique token/escopo do agente")
		s.supportLogf("%v | resposta=%s", err, shortBodyForLog(body))
		return AgentInfo{}, err
	}

	s.agentInfo.Set(info)
	if s.db != nil {
		if err := s.db.CacheSetJSON("agent_info", info, 24*time.Hour); err != nil {
			log.Printf("[support] aviso: falha ao salvar no cache SQLite (agent_info): %v", err)
		}
	}
	s.supportLogf("contexto do agente resolvido: agentId=%s clientId=%s siteId=%s", info.AgentID, info.ClientID, info.SiteID)

	return info, nil
}

// GetAgentInfo resolves and returns the current agent identifiers from the server.
func (s *Service) GetAgentInfo() (AgentInfo, error) {
	return s.fetchAgentContext()
}

// GetSupportTickets returns tickets linked to this agent (filtered by agentId).
func (s *Service) GetSupportTickets() ([]APITicket, error) {
	if !s.featureEnabled(s.supportEnabled()) {
		s.supportLogf("suporte desabilitado pela configuração do agente")
		return []APITicket{}, nil
	}

	s.supportLogf("listando chamados vinculados ao agente")
	info, err := s.fetchAgentContext()
	if err != nil {
		s.supportLogf("falha ao obter contexto para listagem de chamados: %v", err)
		return nil, err
	}
	if strings.TrimSpace(info.ClientID) == "" {
		err := fmt.Errorf("clientId não resolvido: verifique a configuração do agente")
		s.supportLogf("%v (agentId=%s)", err, info.AgentID)
		return nil, err
	}

	cfg := s.debugConfig()
	ctx := s.ctxOrBackground()
	target := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/agent-auth/me/tickets"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		wrapped := fmt.Errorf("URL inválida: %w", err)
		s.supportLogf("falha ao montar request de listagem: %v", wrapped)
		return nil, wrapped
	}
	netutil.SetAgentAuthHeaders(req, cfg.AuthToken)

	resp, err := tlsutil.NewHTTPClient(15 * time.Second).Do(req)
	if err != nil {
		wrapped := fmt.Errorf("falha ao buscar chamados: %w", err)
		s.supportLogf("erro HTTP ao listar chamados: %v", wrapped)
		return nil, wrapped
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		wrapped := fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
		s.supportLogf("erro na listagem de chamados: %v", wrapped)
		return nil, wrapped
	}

	var tickets []APITicket
	if err := json.Unmarshal(body, &tickets); err != nil {
		var envelope struct {
			Items []APITicket `json:"items"`
			Data  []APITicket `json:"data"`
		}
		if err2 := json.Unmarshal(body, &envelope); err2 == nil {
			if envelope.Items != nil {
				tickets = envelope.Items
			} else {
				tickets = envelope.Data
			}
		} else {
			return nil, fmt.Errorf("resposta inválida ao listar chamados: %w", err)
		}
	}
	if tickets == nil {
		tickets = []APITicket{}
	}

	s.supportLogf("listagem concluída: %d chamado(s) retornado(s)", len(tickets))
	return tickets, nil
}

// CreateSupportTicket opens a new ticket linked to this agent.
func (s *Service) CreateSupportTicket(input CreateTicketInput) (APITicket, error) {
	s.supportLogf("criando chamado: title=%q priority=%d category=%q", strings.TrimSpace(input.Title), input.Priority, strings.TrimSpace(input.Category))
	info, err := s.fetchAgentContext()
	if err != nil {
		s.supportLogf("falha ao obter contexto para criação de chamado: %v", err)
		return APITicket{}, err
	}
	if strings.TrimSpace(info.ClientID) == "" {
		err := fmt.Errorf("clientId não resolvido: verifique a configuração do agente")
		s.supportLogf("%v (agentId=%s)", err, info.AgentID)
		return APITicket{}, err
	}

	cfg := s.debugConfig()
	ctx := s.ctxOrBackground()

	type createReq struct {
		DepartmentID      *string `json:"departmentId,omitempty"`
		WorkflowProfileID *string `json:"workflowProfileId,omitempty"`
		Title             string  `json:"title"`
		Description       string  `json:"description"`
		Priority          *string `json:"priority,omitempty"`
		Category          *string `json:"category,omitempty"`
	}

	payload := createReq{
		Title:       strings.TrimSpace(input.Title),
		Description: strings.TrimSpace(input.Description),
	}
	if c := strings.TrimSpace(input.Category); c != "" {
		payload.Category = &c
	}
	if input.Priority > 0 {
		pri := priorityIntToLabel(input.Priority)
		payload.Priority = &pri
	}

	reqBody, err := json.Marshal(payload)
	if err != nil {
		wrapped := fmt.Errorf("erro ao serializar chamado: %w", err)
		s.supportLogf("falha ao serializar payload de chamado: %v", wrapped)
		return APITicket{}, wrapped
	}

	target := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/agent-auth/me/tickets"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(reqBody))
	if err != nil {
		wrapped := fmt.Errorf("URL inválida: %w", err)
		s.supportLogf("falha ao montar request de criação: %v", wrapped)
		return APITicket{}, wrapped
	}
	req.Header.Set("Content-Type", "application/json")
	netutil.SetAgentAuthHeaders(req, cfg.AuthToken)

	resp, err := tlsutil.NewHTTPClient(15 * time.Second).Do(req)
	if err != nil {
		wrapped := fmt.Errorf("falha ao criar chamado: %w", err)
		s.supportLogf("erro HTTP ao criar chamado: %v", wrapped)
		return APITicket{}, wrapped
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		wrapped := fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
		s.supportLogf("erro na criação do chamado: %v | payload=%s | resposta=%s", wrapped, shortBodyForLog(reqBody), shortBodyForLog(respBody))
		return APITicket{}, wrapped
	}

	var ticket APITicket
	if err := json.Unmarshal(respBody, &ticket); err != nil {
		wrapped := fmt.Errorf("resposta inválida ao criar chamado: %w", err)
		s.supportLogf("falha ao decodificar resposta da criação: %v | resposta=%s", wrapped, shortBodyForLog(respBody))
		return APITicket{}, wrapped
	}
	s.supportLogf("chamado criado com sucesso: ticketId=%s", ticket.ID)
	return ticket, nil
}

// GetSupportTicketDetails returns a single ticket if it belongs to the authenticated agent.
func (s *Service) GetSupportTicketDetails(ticketID string) (APITicket, error) {
	ticketID = strings.TrimSpace(ticketID)
	if !guidPattern.MatchString(ticketID) {
		return APITicket{}, fmt.Errorf("ticketId inválido")
	}

	cfg := s.debugConfig()
	ctx := s.ctxOrBackground()

	target := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/agent-auth/me/tickets/" + ticketID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return APITicket{}, err
	}
	netutil.SetAgentAuthHeaders(req, cfg.AuthToken)

	resp, err := tlsutil.NewHTTPClient(10 * time.Second).Do(req)
	if err != nil {
		return APITicket{}, fmt.Errorf("falha ao buscar ticket: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return APITicket{}, fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var ticket APITicket
	if err := json.Unmarshal(body, &ticket); err != nil {
		var envelope struct {
			Ticket *APITicket `json:"ticket"`
			Data   *APITicket `json:"data"`
			Item   *APITicket `json:"item"`
		}
		if err2 := json.Unmarshal(body, &envelope); err2 == nil {
			switch {
			case envelope.Ticket != nil:
				ticket = *envelope.Ticket
			case envelope.Data != nil:
				ticket = *envelope.Data
			case envelope.Item != nil:
				ticket = *envelope.Item
			default:
				return APITicket{}, fmt.Errorf("resposta inválida: ticket não encontrado no payload")
			}
		} else {
			return APITicket{}, fmt.Errorf("resposta inválida: %w", err)
		}
	}

	return ticket, nil
}

func parseWorkflowStatesFromBody(body []byte) ([]APIWorkflowState, error) {
	var states []APIWorkflowState
	if err := json.Unmarshal(body, &states); err == nil {
		return states, nil
	}

	var envelope struct {
		Items []APIWorkflowState `json:"items"`
		Data  []APIWorkflowState `json:"data"`
		State []APIWorkflowState `json:"states"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}

	switch {
	case envelope.Items != nil:
		return envelope.Items, nil
	case envelope.Data != nil:
		return envelope.Data, nil
	case envelope.State != nil:
		return envelope.State, nil
	default:
		return []APIWorkflowState{}, nil
	}
}

// GetTicketWorkflowStates returns available workflow states for tickets.
func (s *Service) GetTicketWorkflowStates() ([]APIWorkflowState, error) {
	cfg := s.debugConfig()
	ctx := s.ctxOrBackground()

	base := strings.TrimSpace(cfg.ApiScheme) + "://" + strings.TrimSpace(cfg.ApiServer)
	paths := []string{
		"/api/agent-auth/me/tickets/workflow-states",
		"/api/agent-auth/me/workflow-states",
		"/api/agent-auth/workflow-states",
	}

	var lastErr error
	for _, p := range paths {
		target := base + p
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			lastErr = fmt.Errorf("URL inválida: %w", err)
			continue
		}
		netutil.SetAgentAuthHeaders(req, cfg.AuthToken)

		resp, err := tlsutil.NewHTTPClient(10 * time.Second).Do(req)
		if err != nil {
			lastErr = fmt.Errorf("falha ao buscar estados de workflow: %w", err)
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			lastErr = fmt.Errorf("endpoint não encontrado em %s", p)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
			continue
		}

		states, err := parseWorkflowStatesFromBody(body)
		if err != nil {
			lastErr = fmt.Errorf("resposta inválida de estados de workflow: %w", err)
			continue
		}

		if states == nil {
			states = []APIWorkflowState{}
		}

		sort.SliceStable(states, func(i, j int) bool {
			if states[i].DisplayOrder == states[j].DisplayOrder {
				return strings.ToLower(states[i].Name) < strings.ToLower(states[j].Name)
			}
			return states[i].DisplayOrder < states[j].DisplayOrder
		})

		s.supportLogf("workflow states carregados: %d estado(s) via %s", len(states), p)
		return states, nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("não foi possível carregar estados de workflow")
	}
	return nil, lastErr
}

// GetTicketComments returns comments for a given ticket.
func (s *Service) GetTicketComments(ticketID string) ([]TicketComment, error) {
	ticketID = strings.TrimSpace(ticketID)
	if !guidPattern.MatchString(ticketID) {
		return nil, fmt.Errorf("ticketId inválido")
	}
	cfg := s.debugConfig()
	ctx := s.ctxOrBackground()

	target := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/agent-auth/me/tickets/" + ticketID + "/comments"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	netutil.SetAgentAuthHeaders(req, cfg.AuthToken)

	resp, err := tlsutil.NewHTTPClient(10 * time.Second).Do(req)
	if err != nil {
		return nil, fmt.Errorf("falha ao buscar comentários: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var comments []TicketComment
	if err := json.Unmarshal(body, &comments); err != nil {
		var envelope struct {
			Items []TicketComment `json:"items"`
			Data  []TicketComment `json:"data"`
		}
		if err2 := json.Unmarshal(body, &envelope); err2 == nil {
			if envelope.Items != nil {
				comments = envelope.Items
			} else {
				comments = envelope.Data
			}
		} else {
			return nil, fmt.Errorf("resposta inválida: %w", err)
		}
	}
	if comments == nil {
		comments = []TicketComment{}
	}
	return comments, nil
}

// AddTicketCommentWithOptions adds a comment and returns the created comment.
func (s *Service) AddTicketCommentWithOptions(ticketID, content string, isInternal bool) (TicketComment, error) {
	ticketID = strings.TrimSpace(ticketID)
	if !guidPattern.MatchString(ticketID) {
		return TicketComment{}, fmt.Errorf("ticketId inválido")
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return TicketComment{}, fmt.Errorf("content não pode ser vazio")
	}

	cfg := s.debugConfig()
	ctx := s.ctxOrBackground()

	payload := map[string]any{
		"content":    content,
		"isInternal": isInternal,
	}
	body, _ := json.Marshal(payload)

	target := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/agent-auth/me/tickets/" + ticketID + "/comments"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return TicketComment{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	netutil.SetAgentAuthHeaders(req, cfg.AuthToken)

	resp, err := tlsutil.NewHTTPClient(10 * time.Second).Do(req)
	if err != nil {
		return TicketComment{}, fmt.Errorf("falha ao enviar comentário: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return TicketComment{}, fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	var created TicketComment
	if len(respBody) == 0 {
		return created, nil
	}
	if err := json.Unmarshal(respBody, &created); err != nil {
		return TicketComment{}, fmt.Errorf("resposta inválida ao criar comentário: %w", err)
	}
	return created, nil
}

// AddTicketComment adds a comment to a ticket.
func (s *Service) AddTicketComment(ticketID, author, content string) error {
	_ = author
	_, err := s.AddTicketCommentWithOptions(ticketID, content, false)
	if err != nil {
		return err
	}
	return nil
}

// CloseSupportTicket closes a ticket with optional rating/comment/final workflow state.
func (s *Service) CloseSupportTicket(ticketID string, input CloseTicketInput) (APITicket, error) {
	ticketID = strings.TrimSpace(ticketID)
	if !guidPattern.MatchString(ticketID) {
		return APITicket{}, fmt.Errorf("ticketId inválido")
	}

	workflowStateID := strings.TrimSpace(input.WorkflowStateID)
	if workflowStateID != "" && !guidPattern.MatchString(workflowStateID) {
		return APITicket{}, fmt.Errorf("workflowStateId inválido")
	}

	if input.Rating != nil {
		if *input.Rating < 0 || *input.Rating > 5 {
			return APITicket{}, fmt.Errorf("rating inválido: informe valor entre 0 e 5")
		}
	}

	cfg := s.debugConfig()
	ctx := s.ctxOrBackground()

	payload := map[string]any{}
	if input.Rating != nil {
		payload["rating"] = *input.Rating
	}
	if c := strings.TrimSpace(input.Comment); c != "" {
		payload["comment"] = c
	}
	if workflowStateID != "" {
		payload["workflowStateId"] = workflowStateID
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return APITicket{}, fmt.Errorf("erro ao serializar payload de fechamento: %w", err)
	}

	target := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/agent-auth/me/tickets/" + ticketID + "/close"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return APITicket{}, fmt.Errorf("URL inválida: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	netutil.SetAgentAuthHeaders(req, cfg.AuthToken)

	s.supportLogf("fechando chamado %s", ticketID)
	resp, err := tlsutil.NewHTTPClient(15 * time.Second).Do(req)
	if err != nil {
		return APITicket{}, fmt.Errorf("falha ao fechar chamado: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return APITicket{}, fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	var ticket APITicket
	if len(respBody) == 0 {
		s.supportLogf("chamado %s fechado (resposta vazia); buscando detalhes atualizados", ticketID)
		return s.GetSupportTicketDetails(ticketID)
	}
	if err := json.Unmarshal(respBody, &ticket); err != nil {
		var envelope struct {
			Ticket *APITicket `json:"ticket"`
			Data   *APITicket `json:"data"`
			Item   *APITicket `json:"item"`
		}
		if err2 := json.Unmarshal(respBody, &envelope); err2 == nil {
			switch {
			case envelope.Ticket != nil:
				ticket = *envelope.Ticket
			case envelope.Data != nil:
				ticket = *envelope.Data
			case envelope.Item != nil:
				ticket = *envelope.Item
			default:
				return APITicket{}, fmt.Errorf("resposta inválida ao fechar chamado")
			}
		} else {
			return APITicket{}, fmt.Errorf("resposta inválida ao fechar chamado: %w", err)
		}
	}

	s.supportLogf("chamado fechado com sucesso: ticketId=%s", ticket.ID)
	return ticket, nil
}

// CloseAgentTicket closes an agent ticket via MCP tool.
func (s *Service) CloseAgentTicket(ticketID string, rating *int, comment, workflowStateID string) (json.RawMessage, error) {
	ticket, err := s.CloseSupportTicket(ticketID, CloseTicketInput{
		Rating:          rating,
		Comment:         comment,
		WorkflowStateID: workflowStateID,
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(ticket)
}

// GetAgentInfoJSON returns the agent info as JSON (for MCP tools).
func (s *Service) GetAgentInfoJSON() (json.RawMessage, error) {
	info, err := s.fetchAgentContext()
	if err != nil {
		return nil, err
	}
	return json.Marshal(info)
}

// ListAgentTickets returns agent tickets as JSON (for MCP tools).
func (s *Service) ListAgentTickets() (json.RawMessage, error) {
	tickets, err := s.GetSupportTickets()
	if err != nil {
		return nil, err
	}
	return json.Marshal(tickets)
}

// GetAgentTicketDetails returns one agent ticket as JSON (for MCP tools).
func (s *Service) GetAgentTicketDetails(ticketID string) (json.RawMessage, error) {
	ticket, err := s.GetSupportTicketDetails(ticketID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(ticket)
}

// AddAgentTicketComment adds a comment to an agent ticket via MCP tool.
func (s *Service) AddAgentTicketComment(ticketID, content string, isInternal bool) (json.RawMessage, error) {
	comment, err := s.AddTicketCommentWithOptions(ticketID, content, isInternal)
	if err != nil {
		return nil, err
	}
	return json.Marshal(comment)
}

// CreateAgentTicket creates a ticket via MCP tool.
func (s *Service) CreateAgentTicket(title, description string, priority int, category string) (json.RawMessage, error) {
	ticket, err := s.CreateSupportTicket(CreateTicketInput{
		Title:       title,
		Description: description,
		Priority:    priority,
		Category:    category,
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(ticket)
}

func extractStr(raw map[string]any, key string) string {
	s := strings.TrimSpace(fmt.Sprint(raw[key]))
	if s == "<nil>" {
		return ""
	}
	return s
}

func (s *Service) ctxOrBackground() context.Context {
	if ctx := s.ctx(); ctx != nil {
		return ctx
	}
	return context.Background()
}
