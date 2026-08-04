// Package agentcommands encapsula o parsing e a classificação de comandos
// do agente (update, notification, power, remote-session, systeminfo),
// separado do App.
package agentcommands

import (
	"encoding/json"
	"fmt"
	"strings"

	"discovery/app/services/notifications"
)

// NotificationDispatchRequest é o payload de uma notificação.
type NotificationDispatchRequest = notifications.DispatchRequest

// ── Agent Update Commands ──

// IsAgentUpdateCommandType verifica se o cmdType é um comando de update.
func IsAgentUpdateCommandType(cmdType string) bool {
	switch strings.ToLower(strings.TrimSpace(cmdType)) {
	case "10", "update", "agentupdate", "selfupdate", "self-update":
		return true
	default:
		return false
	}
}

// ParseAgentUpdatePayload extrai action/url/version do payload de update.
func ParseAgentUpdatePayload(payload any) (string, string, string) {
	if payload == nil {
		return "check-update", "", ""
	}
	switch typed := payload.(type) {
	case string:
		action := strings.ToLower(strings.TrimSpace(typed))
		if action == "" {
			return "check-update", "", ""
		}
		return action, "", ""
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return "", "", fmt.Sprintf("falha ao serializar payload de update: %v", err)
		}
		var parsed struct {
			Action  string `json:"action"`
			URL     string `json:"url"`     // URL direta do instalador (opcional)
			Version string `json:"version"` // versão alvo (opcional)
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return "", "", fmt.Sprintf("payload de update invalido: %v", err)
		}
		action := strings.ToLower(strings.TrimSpace(parsed.Action))
		if action == "" {
			action = "check-update"
		}
		return action, strings.TrimSpace(parsed.URL), strings.TrimSpace(parsed.Version)
	}
}

// UpdateCommand representa o payload completo de um comando de update.
type UpdateCommand struct {
	Action  string  `json:"action"`
	Version *string `json:"version"`
	URL     *string `json:"url"`
}

// VersionValue retorna a versão alvo.
func (c UpdateCommand) VersionValue() string {
	if c.Version != nil {
		return strings.TrimSpace(*c.Version)
	}
	return ""
}

// DownloadURL retorna a URL de download.
func (c UpdateCommand) DownloadURL() string {
	if c.URL != nil {
		return strings.TrimSpace(*c.URL)
	}
	return ""
}

// ParseAgentUpdateCommand parseia o payload de um comando de update.
func ParseAgentUpdateCommand(payload any) (UpdateCommand, error) {
	if payload == nil {
		return UpdateCommand{Action: "check-update"}, nil
	}
	switch typed := payload.(type) {
	case string:
		var cmd UpdateCommand
		if err := json.Unmarshal([]byte(typed), &cmd); err != nil {
			return UpdateCommand{Action: "check-update"}, nil
		}
		cmd.Action = strings.ToLower(strings.TrimSpace(cmd.Action))
		if cmd.Action == "" {
			cmd.Action = "check-update"
		}
		return cmd, nil
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return UpdateCommand{}, fmt.Errorf("falha ao serializar payload de update: %w", err)
		}
		var cmd UpdateCommand
		if err := json.Unmarshal(raw, &cmd); err != nil {
			return UpdateCommand{}, fmt.Errorf("payload de update invalido: %w", err)
		}
		cmd.Action = strings.ToLower(strings.TrimSpace(cmd.Action))
		if cmd.Action == "" {
			cmd.Action = "check-update"
		}
		return cmd, nil
	}
}

// ── Notification Dispatch Commands ──

// IsNotificationDispatchCommandType verifica se o cmdType é de notificação.
func IsNotificationDispatchCommandType(cmdType string) bool {
	switch strings.ToLower(strings.TrimSpace(cmdType)) {
	case "notification", "notify", "notification_dispatch", "notification-dispatch":
		return true
	default:
		return false
	}
}

// ParseNotificationDispatchPayload parseia o payload de notificação.
func ParseNotificationDispatchPayload(payload any) (NotificationDispatchRequest, error) {
	if payload == nil {
		return NotificationDispatchRequest{}, nil
	}
	switch typed := payload.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return NotificationDispatchRequest{}, nil
		}
		var req NotificationDispatchRequest
		if err := json.Unmarshal([]byte(typed), &req); err != nil {
			return NotificationDispatchRequest{}, fmt.Errorf("payload de notificacao invalido: %w", err)
		}
		return req, nil
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return NotificationDispatchRequest{}, fmt.Errorf("falha ao serializar payload de notificação: %w", err)
		}
		var req NotificationDispatchRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return NotificationDispatchRequest{}, fmt.Errorf("payload de notificacao invalido: %w", err)
		}
		return req, nil
	}
}

// ── Power Action Commands ──

// PowerCommandPayload representa o payload de um comando de power.
type PowerCommandPayload struct {
	DelaySeconds int    `json:"delaySeconds"`
	Force        bool   `json:"force"`
	Message      string `json:"message"`
	DeferMinutes int    `json:"deferMinutes"` // minutos para adiar (default 60)
	MaxDefers    int    `json:"maxDefers"`    // máximo de adiamentos permitidos (default 3)
}

// IsPowerActionCommandType verifica se o cmdType é restart/reboot/shutdown.
func IsPowerActionCommandType(cmdType string) bool {
	switch strings.ToLower(strings.TrimSpace(cmdType)) {
	case "restart", "reboot", "shutdown":
		return true
	default:
		return false
	}
}

// ParsePowerCommandPayload extrai delaySeconds, force e message do payload.
func ParsePowerCommandPayload(payload any) PowerCommandPayload {
	if payload == nil {
		return PowerCommandPayload{}
	}

	toString := func(v any) string {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
		return ""
	}
	toBool := func(v any) bool {
		if b, ok := v.(bool); ok {
			return b
		}
		if s, ok := v.(string); ok {
			s = strings.ToLower(strings.TrimSpace(s))
			return s == "true" || s == "1"
		}
		return false
	}
	toInt := func(v any) int {
		switch t := v.(type) {
		case float64:
			return int(t)
		case int:
			return t
		case int64:
			return int(t)
		case json.Number:
			n, err := t.Int64()
			if err != nil {
				return 0
			}
			return int(n)
		case string:
			trimmed := strings.TrimSpace(t)
			if trimmed == "" {
				return 0
			}
			var n int
			if _, err := fmt.Sscanf(trimmed, "%d", &n); err == nil {
				return n
			}
			return 0
		}
		return 0
	}

	m, ok := payload.(map[string]any)
	if !ok {
		if s, ok := payload.(string); ok {
			s = strings.TrimSpace(s)
			if s != "" {
				var fallback map[string]any
				if err := json.Unmarshal([]byte(s), &fallback); err == nil {
					m = fallback
				}
			}
		}
		if m == nil {
			return PowerCommandPayload{}
		}
	}

	return PowerCommandPayload{
		DelaySeconds: toInt(m["delaySeconds"]),
		Force:        toBool(m["force"]),
		Message:      toString(m["message"]),
		DeferMinutes: toInt(m["deferMinutes"]),
		MaxDefers:    toInt(m["maxDefers"]),
	}
}

// ── Remote Session Helpers ──

// IsRemoteSessionCommandType verifica se o cmdType é de remote session.
func IsRemoteSessionCommandType(cmdType string) bool {
	switch strings.ToLower(strings.TrimSpace(cmdType)) {
	case "remotesessionstart", "remote_session_start", "remotesessionstop", "remote_session_stop",
		"remotesessionquality", "remote_session_quality",
		"recordingstart", "recording_start", "recordingstop", "recording_stop":
		return true
	default:
		return false
	}
}

// ParseAnyMap converte um payload em map[string]any, tratando double-encoding.
func ParseAnyMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	if s, ok := v.(string); ok {
		s = strings.TrimSpace(s)
		if len(s) > 0 && s[0] == '{' {
			var m map[string]any
			if err := json.Unmarshal([]byte(s), &m); err == nil {
				return m
			}
		}
	}
	return nil
}

// ── SystemInfo Helpers ──

// NormalizePayloadJSON normaliza um payload para map[string]any.
func NormalizePayloadJSON(payload any) (map[string]any, error) {
	switch v := payload.(type) {
	case map[string]any:
		return v, nil
	case string:
		var m map[string]any
		if err := json.Unmarshal([]byte(v), &m); err != nil {
			return nil, err
		}
		return m, nil
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, err
		}
		return m, nil
	}
}

// GetStringField extrai um campo string de um map.
func GetStringField(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprintf("%v", v)
	}
	return ""
}

// GetBoolField extrai um campo bool de um map.
func GetBoolField(m map[string]any, key string) bool {
	if v, ok := m[key]; ok {
		switch b := v.(type) {
		case bool:
			return b
		case string:
			return strings.EqualFold(b, "true") || b == "1"
		case float64:
			return b != 0
		}
	}
	return false
}
