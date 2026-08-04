package app

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ── Notification Dispatch Commands ──

func isNotificationDispatchCommandType(cmdType string) bool {
	switch strings.ToLower(strings.TrimSpace(cmdType)) {
	case "notification", "notify", "notification_dispatch", "notification-dispatch":
		return true
	default:
		return false
	}
}

func parseNotificationDispatchPayload(payload any) (NotificationDispatchRequest, error) {
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
