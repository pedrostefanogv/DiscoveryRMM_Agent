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
//
// Correção A1: também guarda o resultado FINAL da notificação original
// (approved/denied/deferred/timeout_policy_applied) e um canal done fechado
// quando o resultado fica conhecido — retries com a mesma idempotencyKey
// recebem o MESMO resultado, nunca um "approved" implícito (que fazia bypass
// de require_confirmation em comandos remotos).
type idempotencyEntry struct {
	NotificationID string
	CreatedAt      time.Time
	Result         string
	done           chan struct{}
	closeOnce      sync.Once
}

// finish registra o resultado final e libera retries que estejam aguardando.
func (e *idempotencyEntry) finish(result string) {
	e.Result = result
	e.closeOnce.Do(func() {
		if e.done != nil {
			close(e.done)
		}
	})
}

// waitResult aguarda até timeout pelo resultado final da notificação
// original. Retorna "" se não concluída dentro do prazo.
func (e *idempotencyEntry) waitResult(timeout time.Duration) string {
	if e.done == nil {
		return ""
	}
	select {
	case <-e.done:
		return e.Result
	case <-time.After(timeout):
		return ""
	}
}

// idempotencyTTL define quanto tempo uma entrada de idempotência permanece
// no cache antes de ser elegível para limpeza.
const idempotencyTTL = 24 * time.Hour

// maxDedupResultWait é o tempo máximo que um retry aguarda a decisão do
// usuário sobre a notificação original antes de responder "pending".
var maxDedupResultWait = 90 * time.Second

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
	// NativeFallback é chamado quando não há UI para renderizar a notificação
	// (headless). Usado pelo serviço para disparar toast nativo do Windows
	// (PLANO_SEPARACAO_SERVICO_UI.md, Fase C2/D4). Pode ser nil.
	NativeFallback func(req DispatchRequest)
}

// Service encapsula o centro de notificações.
type Service struct {
	logf                  func(string)
	ctx                   func() interface{ Done() <-chan struct{} }
	db                    func() *database.DB
	emitEvent             func(string, ...any)
	getAgentConfiguration func() AgentConfiguration
	nativeFallback        func(req DispatchRequest)

	mu                  sync.Mutex
	notificationByKey   map[string]*idempotencyEntry
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
		nativeFallback:        deps.NativeFallback,
		notificationByKey:     make(map[string]*idempotencyEntry),
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

			// Correção A1: repassa o resultado REAL da notificação original.
			//   - Resultado já conhecido → reflete exatamente (approved,
			//     denied, deferred, timeout_policy_applied).
			//   - Original ainda aguardando decisão do usuário → aguarda
			//     (limitado por maxDedupResultWait) e, se não concluir,
			//     responde "pending" — NUNCA "approved" implícito.
			result := existing.waitResult(maxDedupResultWait)
			if result == "" {
				result = "pending"
			}
			accepted := result == "approved"
			s.persist(database.NotificationEventEntry{
				NotificationID: existing.NotificationID,
				Mode:           req.Mode,
				Severity:       req.Severity,
				EventType:      req.EventType,
				Title:          req.Title,
				Result:         result,
				AgentAction:    "deduplicated",
				MetadataJSON:   mustMarshalJSON(req.Metadata),
			})
			return DispatchResponse{
				Accepted:       accepted,
				NotificationID: existing.NotificationID,
				AgentAction:    "deduplicated",
				Result:         result,
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
		s.notificationByKey[req.IdempotencyKey] = &idempotencyEntry{
			NotificationID: req.NotificationID,
			CreatedAt:      time.Now(),
			done:           make(chan struct{}),
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
		// Sem UI para renderizar (nem Wails nem companion via IPC): dispara
		// o fallback nativo (toast do Windows no serviço — Fase C2/D4) e, para
		// require_confirmation, aguarda a resposta do usuário no toast pelo
		// mesmo timeout do caminho com UI (revisão 2 — bug B5: antes o
		// retorno era imediato, sem janela para o clique).
		if s.nativeFallback != nil {
			s.nativeFallback(req)
		}
		if req.Mode == "require_confirmation" {
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
				s.logf("[notification] confirmação (toast) id=" + req.NotificationID + " result=" + result)
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
				s.recordDispatchResult(req.IdempotencyKey, req.NotificationID, result)
				return DispatchResponse{
					Accepted:       true,
					NotificationID: req.NotificationID,
					AgentAction:    "user_decision",
					Result:         result,
				}
			case <-time.After(time.Duration(req.TimeoutSeconds) * time.Second):
				s.logf("[notification] confirmação (toast) timeout id=" + req.NotificationID)
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
				s.recordDispatchResult(req.IdempotencyKey, req.NotificationID, "timeout_policy_applied")
				return DispatchResponse{
					Accepted:       true,
					NotificationID: req.NotificationID,
					AgentAction:    "timeout",
					Result:         "timeout_policy_applied",
				}
			}
		}
		s.persist(database.NotificationEventEntry{
			NotificationID: req.NotificationID,
			Mode:           req.Mode,
			Severity:       req.Severity,
			EventType:      req.EventType,
			Title:          req.Title,
			Result:         "approved",
			AgentAction:    "headless_logged",
			MetadataJSON:   mustMarshalJSON(req.Metadata),
		})
		s.recordDispatchResult(req.IdempotencyKey, req.NotificationID, "approved")
		return DispatchResponse{
			Accepted:       true,
			NotificationID: req.NotificationID,
			AgentAction:    "headless_logged",
			Result:         "approved",
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
		s.recordDispatchResult(req.IdempotencyKey, req.NotificationID, "approved")
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

// recordDispatchResult registra o resultado FINAL da notificação original
// (correção A1) para que retries com a mesma idempotencyKey recebam o mesmo
// resultado em vez de um "approved" implícito. Best-effort e no-op sem key.
func (s *Service) recordDispatchResult(idempotencyKey, notificationID, result string) {
	key := strings.TrimSpace(idempotencyKey)
	if key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.notificationByKey[key]
	if !ok {
		return
	}
	if strings.TrimSpace(entry.NotificationID) != "" &&
		!strings.EqualFold(entry.NotificationID, strings.TrimSpace(notificationID)) {
		return
	}
	entry.finish(normalizeResult(result))
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
		// M2: serializa como []any de maps — o toast nativo headless faz
		// .([]any) no metadata e o tipo concreto []AgentNotificationAction
		// nunca casa (os botões do toast headless nunca apareciam).
		// value = ID da ação, que volta via Respond(notificationID, value).
		actions := make([]any, 0, len(policy.Actions))
		for _, a := range policy.Actions {
			actions = append(actions, map[string]any{
				"id":    a.ID,
				"label": a.Label,
				"value": a.ID,
			})
		}
		req.Metadata["actions"] = actions
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
