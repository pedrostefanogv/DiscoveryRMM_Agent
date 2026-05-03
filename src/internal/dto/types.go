// Package dto fornece Data Transfer Objects tipados para comunicação
// entre o backend Go e o frontend Wails, substituindo map[string]interface{}.
package dto

import "time"

// HealthCheckItem representa um componente no payload de saúde.
type HealthCheckItem struct {
	Component   string `json:"component"`
	Status      string `json:"status"`
	Message     string `json:"message"`
	LastBeat    string `json:"lastBeat"`
	CheckedAt   string `json:"checkedAt"`
	Recoverable bool   `json:"recoverable"`
}

// ─── Service Health ────────────────────────────────────────────────

// ServiceHealthPayload representa o payload de GetServiceHealth.
type ServiceHealthPayload struct {
	Error       *string           `json:"error,omitempty"`
	Running     bool              `json:"running"`
	ServiceOnly bool              `json:"service_only"`
	UserMessage string            `json:"user_message,omitempty"`
	Components  []HealthCheckItem `json:"components,omitempty"`
	Uptime      string            `json:"uptime,omitempty"`
	Version     string            `json:"version,omitempty"`
}

// ─── Service IPC ───────────────────────────────────────────────────

// IPCServiceStatus é retornado pelo named pipe do service.
type IPCServiceStatus struct {
	Running    bool              `json:"running"`
	Uptime     string            `json:"uptime"`
	Version    string            `json:"version"`
	Components []HealthCheckItem `json:"components"`
}

// ─── Notifications ─────────────────────────────────────────────────

// NotificationEvent representa um evento de notificação enviado ao frontend.
type NotificationEvent struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Title     string    `json:"title"`
	Message   string    `json:"message"`
	Severity  string    `json:"severity"`
	Timestamp time.Time `json:"timestamp"`
	Read      bool      `json:"read"`
	ActionURL string    `json:"actionUrl,omitempty"`
}

// ─── Chat ──────────────────────────────────────────────────────────

// ChatErrorEvent é emitido via "chat:error".
type ChatErrorEvent struct {
	Message string `json:"message"`
}
