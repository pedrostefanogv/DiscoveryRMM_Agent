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
