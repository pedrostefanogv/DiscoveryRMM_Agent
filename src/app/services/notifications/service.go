package notifications

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"discovery/app/core/database"
)

// idempotencyEntry armazena o notificationID associado a um idempotencyKey
// junto com o timestamp de criação, para permitir limpeza periódica.
type idempotencyEntry struct {
	NotificationID string
	CreatedAt      time.Time
}

// idempotencyTTL define quanto tempo uma entrada de idempotência permanece
// no cache antes de ser elegível para limpeza.
const idempotencyTTL = 24 * time.Hour

// idempotencyPruneLimit é o número máximo de entradas removidas por chamada.
const idempotencyPruneLimit = 200

// DispatchRequest é o payload de uma notificação.
type DispatchRequest struct {
	NotificationID string         `json:"notificationId"`
	IdempotencyKey string         `json:"idempotencyKey"`
	Title          string         `json:"title"`
	Message        string         `json:"message"`
	Mode           string         `json:"mode"`
	Severity       string         `json:"severity"`
	EventType      string         `json:"eventType"`
	Layout         string         `json:"layout"`
	TimeoutSeconds int            `json:"timeoutSeconds"`
	Metadata       map[string]any `json:"metadata"`
}

// DispatchResponse é a resposta de uma notificação.
type DispatchResponse struct {
	Accepted       bool   `json:"accepted"`
	NotificationID string `json:"notificationId"`
	AgentAction    string `json:"agentAction"`
	Result         string `json:"result,omitempty"`
	Message        string `json:"message,omitempty"`
}

// AgentConfiguration é uma visão mínima da configuração do agente usada
// pelas políticas de notificação.
type AgentConfiguration struct {
	Rollout              AgentRolloutConfig
	NotificationPolicies []AgentNotificationPolicy
	NotificationBranding AgentNotificationBrandingConfig
}

// AgentRolloutConfig é a configuração de rollout.
type AgentRolloutConfig struct {
	EnableNotifications           *bool
	BlockedNotificationEventTypes []string
	AllowedNotificationEventTypes []string
	EnableRequireConfirmation     *bool
}

// AgentNotificationPolicy é uma política de notificação por evento.
type AgentNotificationPolicy struct {
	EventType      string
	Mode           string
	Severity       string
	TimeoutSeconds *int
	Actions        []AgentNotificationAction
	StyleOverride  AgentNotificationStyleOverride
}

// AgentNotificationAction é uma ação de notificação.
type AgentNotificationAction struct {
	ID         string
	Label      string
	ActionType string
}

// AgentNotificationStyleOverride é o override de estilo.
type AgentNotificationStyleOverride struct {
	Layout     string
	Background string
	Text       string
}

// AgentNotificationBrandingConfig é a configuração de branding.
type AgentNotificationBrandingConfig struct {
	CompanyName string
	LogoURL     string
	BannerURL   string
}

// Deps são as dependências injetadas no Service.
type Deps struct {
	// Logf appends a log line.
	Logf func(string)
	// Ctx retorna o contexto da aplicação (pode ser nil).
	Ctx func() interface{ Done() <-chan struct{} }
	// DB retorna o banco de dados (pode ser nil).
	DB func() *database.DB
	// EmitEvent emite um evento Wails.
	EmitEvent func(string, ...any)
	// GetAgentConfiguration retorna a configuração do agente.
	GetAgentConfiguration func() AgentConfiguration
}

// Service encapsula o centro de notificações.
type Service struct {
	logf                  func(string)
	ctx                   func() interface{ Done() <-chan struct{} }
	db                    func() *database.DB
	emitEvent             func(string, ...any)
	getAgentConfiguration func() AgentConfiguration

	mu                  sync.Mutex
	notificationByKey   map[string]idempotencyEntry
	pendingNotifyResult map[string]chan string
}

// New cria um NotificationService.
func New(deps Deps) *Service {
	logf := deps.Logf
	if logf == nil {
		logf = func(string) {}
	}
	return &Service{
		logf:                  logf,
		ctx:                   deps.Ctx,
		db:                    deps.DB,
		emitEvent:             deps.EmitEvent,
		getAgentConfiguration: deps.GetAgentConfiguration,
		notificationByKey:     make(map[string]idempotencyEntry),
		pendingNotifyResult:   make(map[string]chan string),
	}
}

// Dispatch processa e despacha uma notificação.
func (s *Service) Dispatch(req DispatchRequest) DispatchResponse {
	if s.getAgentConfiguration != nil {
		cfg := s.getAgentConfiguration()
		req = applyPolicyByEventType(req, cfg)
		if !isEnabledForRollout(cfg.Rollout, req.EventType) {
			s.logf("[notification] dispatch bloqueado por rollout")
			s.persist(database.NotificationEventEntry{
				NotificationID: strings.TrimSpace(req.NotificationID),
				Mode:           req.Mode,
				Severity:       req.Severity,
				EventType:      req.EventType,
				Title:          req.Title,
				Result:         "denied",
				AgentAction:    "disabled_by_rollout",
				MetadataJSON:   mustMarshalJSON(req.Metadata),
			})
			return DispatchResponse{
				Accepted:       false,
				NotificationID: strings.TrimSpace(req.NotificationID),
				AgentAction:    "disabled_by_rollout",
				Result:         "denied",
				Message:        "notificação bloqueada por rollout",
			}
		}
	}

	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if req.IdempotencyKey != "" {
		s.mu.Lock()
		s.pruneByKeyLocked(time.Now())
		if existing, ok := s.notificationByKey[req.IdempotencyKey]; ok {
			s.mu.Unlock()
			s.persist(database.NotificationEventEntry{
				NotificationID: existing.NotificationID,
				Mode:           req.Mode,
				Severity:       req.Severity,
				EventType:      req.EventType,
				Title:          req.Title,
				Result:         "approved",
				AgentAction:    "deduplicated",
				MetadataJSON:   mustMarshalJSON(req.Metadata),
			})
			return DispatchResponse{
				Accepted:       true,
				NotificationID: existing.NotificationID,
				AgentAction:    "deduplicated",
				Result:         "approved",
			}
		}
		s.mu.Unlock()
	}

	if strings.TrimSpace(req.NotificationID) == "" {
		req.NotificationID = strings.TrimSpace(req.IdempotencyKey)
	}
	if strings.TrimSpace(req.NotificationID) == "" {
		req.NotificationID = fmt.Sprintf("notification-%d", time.Now().UnixNano())
	}
	if strings.TrimSpace(req.Title) == "" {
		req.Title = "Notificacao"
	}
	if strings.TrimSpace(req.Mode) == "" {
		req.Mode = "notify_only"
	}
	if strings.TrimSpace(req.Severity) == "" {
		req.Severity = "medium"
	}
	if strings.TrimSpace(req.Layout) == "" {
		req.Layout = "toast"
	}
	if req.TimeoutSeconds <= 0 {
		req.TimeoutSeconds = 45
	}

	req.Mode = normalizeMode(req.Mode)
	req.Severity = normalizeSeverity(req.Severity)
	req.Layout = normalizeLayout(req.Layout)

	if s.getAgentConfiguration != nil {
		cfg := s.getAgentConfiguration()
		if req.Mode == "require_confirmation" && cfg.Rollout.EnableRequireConfirmation != nil && !*cfg.Rollout.EnableRequireConfirmation {
			req.Mode = "notify_only"
			if req.Metadata == nil {
				req.Metadata = map[string]any{}
			}
			req.Metadata["rolloutDowngradedMode"] = true
		}
	}

	if req.IdempotencyKey != "" {
		s.mu.Lock()
		s.notificationByKey[req.IdempotencyKey] = idempotencyEntry{
			NotificationID: req.NotificationID,
			CreatedAt:      time.Now(),
		}
		s.mu.Unlock()
	}

	payload := map[string]any{
		"id":             req.NotificationID,
		"source":         "api",
		"eventType":      req.EventType,
		"title":          req.Title,
		"message":        req.Message,
		"mode":           req.Mode,
		"severity":       req.Severity,
		"layout":         req.Layout,
		"timeoutSeconds": req.TimeoutSeconds,
		"metadata":       req.Metadata,
		"createdAt":      time.Now().UTC().Format(time.RFC3339),
	}

	if s.ctx == nil || s.ctx() == nil {
		result := "timeout_policy_applied"
		agentAction := "headless_no_context"
		if req.Mode != "require_confirmation" {
			result = "approved"
			agentAction = "headless_logged"
		}
		s.persist(database.NotificationEventEntry{
			NotificationID: req.NotificationID,
			Mode:           req.Mode,
			Severity:       req.Severity,
			EventType:      req.EventType,
			Title:          req.Title,
			Result:         result,
			AgentAction:    agentAction,
			MetadataJSON:   mustMarshalJSON(req.Metadata),
		})
		return DispatchResponse{
			Accepted:       true,
			NotificationID: req.NotificationID,
			AgentAction:    agentAction,
			Result:         result,
			Message:        "contexto UI indisponivel",
		}
	}

	if runtime.GOOS != "windows" && req.Mode == "require_confirmation" {
		req.Mode = "notify_only"
		payload["mode"] = req.Mode
	}

	s.emitEvent("notification:new", payload)
	s.logf("[notification] dispatched id=" + req.NotificationID + " mode=" + req.Mode + " severity=" + req.Severity)
	s.persist(database.NotificationEventEntry{
		NotificationID: req.NotificationID,
		Mode:           req.Mode,
		Severity:       req.Severity,
		EventType:      req.EventType,
		Title:          req.Title,
		AgentAction:    "rendered",
		MetadataJSON:   mustMarshalJSON(req.Metadata),
	})

	if req.Mode != "require_confirmation" {
		s.persist(database.NotificationEventEntry{
			NotificationID: req.NotificationID,
			Mode:           req.Mode,
			Severity:       req.Severity,
			EventType:      req.EventType,
			Title:          req.Title,
			Result:         "approved",
			AgentAction:    "rendered",
			MetadataJSON:   mustMarshalJSON(req.Metadata),
		})
		return DispatchResponse{
			Accepted:       true,
			NotificationID: req.NotificationID,
			AgentAction:    "rendered",
			Result:         "approved",
		}
	}

	resultCh := make(chan string, 1)
	s.mu.Lock()
	s.pendingNotifyResult[req.NotificationID] = resultCh
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pendingNotifyResult, req.NotificationID)
		s.mu.Unlock()
	}()

	select {
	case result := <-resultCh:
		result = normalizeResult(result)
		s.logf("[notification] confirmation id=" + req.NotificationID + " result=" + result)
		s.persist(database.NotificationEventEntry{
			NotificationID: req.NotificationID,
			Mode:           req.Mode,
			Severity:       req.Severity,
			EventType:      req.EventType,
			Title:          req.Title,
			Result:         result,
			AgentAction:    "user_decision",
			MetadataJSON:   mustMarshalJSON(req.Metadata),
		})
		return DispatchResponse{
			Accepted:       true,
			NotificationID: req.NotificationID,
			AgentAction:    "user_decision",
			Result:         result,
		}
	case <-time.After(time.Duration(req.TimeoutSeconds) * time.Second):
		s.logf("[notification] confirmation timeout id=" + req.NotificationID)
		s.persist(database.NotificationEventEntry{
			NotificationID: req.NotificationID,
			Mode:           req.Mode,
			Severity:       req.Severity,
			EventType:      req.EventType,
			Title:          req.Title,
			Result:         "timeout_policy_applied",
			AgentAction:    "timeout",
			MetadataJSON:   mustMarshalJSON(req.Metadata),
		})
		return DispatchResponse{
			Accepted:       true,
			NotificationID: req.NotificationID,
			AgentAction:    "timeout",
			Result:         "timeout_policy_applied",
		}
	}
}

// Respond processa a resposta do usuário a uma notificação.
func (s *Service) Respond(notificationID, result string) bool {
	notificationID = strings.TrimSpace(notificationID)
	if notificationID == "" {
		return false
	}
	s.mu.Lock()
	ch, ok := s.pendingNotifyResult[notificationID]
	s.mu.Unlock()
	if !ok {
		return false
	}
	result = normalizeResult(result)
	select {
	case ch <- result:
		return true
	default:
		return false
	}
}

func (s *Service) persist(entry database.NotificationEventEntry) {
	if s.db == nil || s.db() == nil {
		return
	}
	if err := s.db().SaveNotificationEvent(entry); err != nil {
		s.logf("[notification] falha ao persistir evento: " + err.Error())
	}
}

func (s *Service) pruneByKeyLocked(now time.Time) {
	if len(s.notificationByKey) == 0 {
		return
	}
	removed := 0
	for key, entry := range s.notificationByKey {
		if now.Sub(entry.CreatedAt) > idempotencyTTL {
			delete(s.notificationByKey, key)
			removed++
			if removed >= idempotencyPruneLimit {
				break
			}
		}
	}
}

func normalizeResult(result string) string {
	switch strings.ToLower(strings.TrimSpace(result)) {
	case "approved":
		return "approved"
	case "denied":
		return "denied"
	case "deferred", "adiado", "adiar", "postpone", "snooze":
		return "deferred"
	default:
		return "timeout_policy_applied"
	}
}

func normalizeMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "silent", "silencioso":
		return "silent"
	case "require_confirmation", "confirm", "confirmacao", "confirmacao_obrigatoria":
		return "require_confirmation"
	default:
		return "notify_only"
	}
}

func normalizeSeverity(severity string) string {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "info", "informativo", "low", "baixo":
		return "low"
	case "warning", "warn", "alerta", "medium", "medio", "médio":
		return "medium"
	case "error", "erro", "high", "alto":
		return "high"
	case "critical", "critico", "crítico":
		return "critical"
	default:
		return "medium"
	}
}

func normalizeLayout(layout string) string {
	switch strings.ToLower(strings.TrimSpace(layout)) {
	case "banner", "modal", "toast":
		return strings.ToLower(strings.TrimSpace(layout))
	default:
		return "toast"
	}
}

func mustMarshalJSON(value any) string {
	if value == nil {
		return ""
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(raw)
}

func isEnabledForRollout(rollout AgentRolloutConfig, eventType string) bool {
	if rollout.EnableNotifications != nil && !*rollout.EnableNotifications {
		return false
	}
	normalizedEvent := strings.ToLower(strings.TrimSpace(eventType))
	if normalizedEvent == "" {
		return true
	}
	if containsNormalizedString(rollout.BlockedNotificationEventTypes, normalizedEvent) {
		return false
	}
	if len(rollout.AllowedNotificationEventTypes) > 0 {
		return containsNormalizedString(rollout.AllowedNotificationEventTypes, normalizedEvent)
	}
	return true
}

func containsNormalizedString(values []string, target string) bool {
	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" {
		return false
	}
	for _, value := range values {
		if strings.ToLower(strings.TrimSpace(value)) == target {
			return true
		}
	}
	return false
}

func applyPolicyByEventType(req DispatchRequest, cfg AgentConfiguration) DispatchRequest {
	eventType := strings.ToLower(strings.TrimSpace(req.EventType))
	if eventType == "" {
		return req
	}
	policy, ok := findPolicy(cfg.NotificationPolicies, eventType)
	if !ok {
		return req
	}
	if strings.TrimSpace(policy.Mode) != "" {
		req.Mode = strings.TrimSpace(policy.Mode)
	}
	if strings.TrimSpace(policy.Severity) != "" {
		req.Severity = strings.TrimSpace(policy.Severity)
	}
	if policy.TimeoutSeconds != nil && *policy.TimeoutSeconds > 0 {
		req.TimeoutSeconds = *policy.TimeoutSeconds
	}
	if strings.TrimSpace(policy.StyleOverride.Layout) != "" {
		req.Layout = strings.TrimSpace(policy.StyleOverride.Layout)
	}
	if req.Metadata == nil {
		req.Metadata = map[string]any{}
	}
	if len(policy.Actions) > 0 {
		req.Metadata["actions"] = policy.Actions
	}
	if strings.TrimSpace(policy.StyleOverride.Background) != "" || strings.TrimSpace(policy.StyleOverride.Text) != "" {
		req.Metadata["styleOverride"] = map[string]any{
			"background": strings.TrimSpace(policy.StyleOverride.Background),
			"text":       strings.TrimSpace(policy.StyleOverride.Text),
		}
	}
	if strings.TrimSpace(cfg.NotificationBranding.CompanyName) != "" || strings.TrimSpace(cfg.NotificationBranding.LogoURL) != "" || strings.TrimSpace(cfg.NotificationBranding.BannerURL) != "" {
		req.Metadata["branding"] = cfg.NotificationBranding
	}
	return req
}

func findPolicy(policies []AgentNotificationPolicy, eventType string) (AgentNotificationPolicy, bool) {
	normalizedEvent := strings.ToLower(strings.TrimSpace(eventType))
	if normalizedEvent == "" {
		return AgentNotificationPolicy{}, false
	}
	for _, policy := range policies {
		if strings.ToLower(strings.TrimSpace(policy.EventType)) == normalizedEvent {
			return policy, true
		}
	}
	return AgentNotificationPolicy{}, false
}
