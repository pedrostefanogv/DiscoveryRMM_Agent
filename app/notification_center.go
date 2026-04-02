package app

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"time"

	"discovery/internal/database"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type NotificationDispatchRequest struct {
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

type NotificationDispatchResponse struct {
	Accepted       bool   `json:"accepted"`
	NotificationID string `json:"notificationId"`
	AgentAction    string `json:"agentAction"`
	Result         string `json:"result,omitempty"`
	Message        string `json:"message,omitempty"`
}

func (a *App) DispatchNotification(req NotificationDispatchRequest) NotificationDispatchResponse {
	if a != nil {
		cfg := a.GetAgentConfiguration()
		req = applyNotificationPolicyByEventType(req, cfg)
		if !isNotificationEnabledForRollout(cfg.Rollout, req.EventType) {
			a.logs.append("[notification] dispatch bloqueado por rollout")
			a.persistNotificationEvent(database.NotificationEventEntry{
				NotificationID: strings.TrimSpace(req.NotificationID),
				Mode:           req.Mode,
				Severity:       req.Severity,
				EventType:      req.EventType,
				Title:          req.Title,
				Result:         "denied",
				AgentAction:    "disabled_by_rollout",
				MetadataJSON:   mustMarshalJSON(req.Metadata),
			})
			return NotificationDispatchResponse{
				Accepted:       false,
				NotificationID: strings.TrimSpace(req.NotificationID),
				AgentAction:    "disabled_by_rollout",
				Result:         "denied",
				Message:        "notificacao bloqueada por rollout",
			}
		}
	}

	if a != nil {
		req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
		if req.IdempotencyKey != "" {
			a.notificationMu.Lock()
			if existingID, ok := a.notificationByKey[req.IdempotencyKey]; ok {
				a.notificationMu.Unlock()
				a.persistNotificationEvent(database.NotificationEventEntry{
					NotificationID: existingID,
					Mode:           req.Mode,
					Severity:       req.Severity,
					EventType:      req.EventType,
					Title:          req.Title,
					Result:         "approved",
					AgentAction:    "deduplicated",
					MetadataJSON:   mustMarshalJSON(req.Metadata),
				})
				return NotificationDispatchResponse{
					Accepted:       true,
					NotificationID: existingID,
					AgentAction:    "deduplicated",
					Result:         "approved",
				}
			}
			a.notificationMu.Unlock()
		}
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

	req.Mode = normalizeNotificationMode(req.Mode)
	req.Severity = normalizeNotificationSeverity(req.Severity)
	req.Layout = normalizeNotificationLayout(req.Layout)

	if a != nil {
		cfg := a.GetAgentConfiguration()
		if req.Mode == "require_confirmation" && cfg.Rollout.EnableRequireConfirmation != nil && !*cfg.Rollout.EnableRequireConfirmation {
			req.Mode = "notify_only"
			if req.Metadata == nil {
				req.Metadata = map[string]any{}
			}
			req.Metadata["rolloutDowngradedMode"] = true
		}
	}

	if a != nil && req.IdempotencyKey != "" {
		a.notificationMu.Lock()
		a.notificationByKey[req.IdempotencyKey] = req.NotificationID
		a.notificationMu.Unlock()
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

	if a == nil || a.ctx == nil {
		result := "timeout_policy_applied"
		agentAction := "headless_no_context"
		if req.Mode != "require_confirmation" {
			result = "approved"
			agentAction = "headless_logged"
		}
		if a != nil {
			a.persistNotificationEvent(database.NotificationEventEntry{
				NotificationID: req.NotificationID,
				Mode:           req.Mode,
				Severity:       req.Severity,
				EventType:      req.EventType,
				Title:          req.Title,
				Result:         result,
				AgentAction:    agentAction,
				MetadataJSON:   mustMarshalJSON(req.Metadata),
			})
		}
		return NotificationDispatchResponse{
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

	wailsRuntime.EventsEmit(a.ctx, "notification:new", payload)
	a.logs.append("[notification] dispatched id=" + req.NotificationID + " mode=" + req.Mode + " severity=" + req.Severity)
	a.persistNotificationEvent(database.NotificationEventEntry{
		NotificationID: req.NotificationID,
		Mode:           req.Mode,
		Severity:       req.Severity,
		EventType:      req.EventType,
		Title:          req.Title,
		AgentAction:    "rendered",
		MetadataJSON:   mustMarshalJSON(req.Metadata),
	})

	if req.Mode != "require_confirmation" {
		a.persistNotificationEvent(database.NotificationEventEntry{
			NotificationID: req.NotificationID,
			Mode:           req.Mode,
			Severity:       req.Severity,
			EventType:      req.EventType,
			Title:          req.Title,
			Result:         "approved",
			AgentAction:    "rendered",
			MetadataJSON:   mustMarshalJSON(req.Metadata),
		})
		return NotificationDispatchResponse{
			Accepted:       true,
			NotificationID: req.NotificationID,
			AgentAction:    "rendered",
			Result:         "approved",
		}
	}

	resultCh := make(chan string, 1)
	a.notificationMu.Lock()
	a.pendingNotifyResult[req.NotificationID] = resultCh
	a.notificationMu.Unlock()
	defer func() {
		a.notificationMu.Lock()
		delete(a.pendingNotifyResult, req.NotificationID)
		a.notificationMu.Unlock()
	}()

	select {
	case result := <-resultCh:
		result = normalizeNotificationResult(result)
		a.logs.append("[notification] confirmation id=" + req.NotificationID + " result=" + result)
		a.persistNotificationEvent(database.NotificationEventEntry{
			NotificationID: req.NotificationID,
			Mode:           req.Mode,
			Severity:       req.Severity,
			EventType:      req.EventType,
			Title:          req.Title,
			Result:         result,
			AgentAction:    "user_decision",
			MetadataJSON:   mustMarshalJSON(req.Metadata),
		})
		return NotificationDispatchResponse{
			Accepted:       true,
			NotificationID: req.NotificationID,
			AgentAction:    "user_decision",
			Result:         result,
		}
	case <-time.After(time.Duration(req.TimeoutSeconds) * time.Second):
		a.logs.append("[notification] confirmation timeout id=" + req.NotificationID)
		a.persistNotificationEvent(database.NotificationEventEntry{
			NotificationID: req.NotificationID,
			Mode:           req.Mode,
			Severity:       req.Severity,
			EventType:      req.EventType,
			Title:          req.Title,
			Result:         "timeout_policy_applied",
			AgentAction:    "timeout",
			MetadataJSON:   mustMarshalJSON(req.Metadata),
		})
		return NotificationDispatchResponse{
			Accepted:       true,
			NotificationID: req.NotificationID,
			AgentAction:    "timeout",
			Result:         "timeout_policy_applied",
		}
	}
}

func (a *App) RespondToNotification(notificationID, result string) bool {
	notificationID = strings.TrimSpace(notificationID)
	if notificationID == "" {
		return false
	}

	a.notificationMu.Lock()
	ch, ok := a.pendingNotifyResult[notificationID]
	a.notificationMu.Unlock()
	if !ok {
		return false
	}

	result = normalizeNotificationResult(result)
	select {
	case ch <- result:
		return true
	default:
		return false
	}
}

func normalizeNotificationResult(result string) string {
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

func normalizeNotificationMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "silent", "silencioso":
		return "silent"
	case "require_confirmation", "confirm", "confirmacao", "confirmacao_obrigatoria":
		return "require_confirmation"
	default:
		return "notify_only"
	}
}

func normalizeNotificationSeverity(severity string) string {
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

func normalizeNotificationLayout(layout string) string {
	switch strings.ToLower(strings.TrimSpace(layout)) {
	case "banner", "modal", "toast":
		return strings.ToLower(strings.TrimSpace(layout))
	default:
		return "toast"
	}
}

func (a *App) persistNotificationEvent(entry database.NotificationEventEntry) {
	if a == nil || a.db == nil {
		return
	}
	if err := a.db.SaveNotificationEvent(entry); err != nil {
		a.logs.append("[notification] falha ao persistir evento: " + err.Error())
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

func isNotificationEnabledForRollout(rollout AgentRolloutConfig, eventType string) bool {
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

func applyNotificationPolicyByEventType(req NotificationDispatchRequest, cfg AgentConfiguration) NotificationDispatchRequest {
	eventType := strings.ToLower(strings.TrimSpace(req.EventType))
	if eventType == "" {
		return req
	}

	policy, ok := findNotificationPolicy(cfg.NotificationPolicies, eventType)
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

func findNotificationPolicy(policies []AgentNotificationPolicy, eventType string) (AgentNotificationPolicy, bool) {
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
