package app

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ── Power Action Commands ──

type powerCommandPayload struct {
	DelaySeconds int    `json:"delaySeconds"`
	Force        bool   `json:"force"`
	Message      string `json:"message"`
	DeferMinutes int    `json:"deferMinutes"` // minutos para adiar (default 60)
	MaxDefers    int    `json:"maxDefers"`    // máximo de adiamentos permitidos (default 3)
}

// isPowerActionCommandType checks whether cmdType is a restart/reboot/shutdown command.
func isPowerActionCommandType(cmdType string) bool {
	switch strings.ToLower(strings.TrimSpace(cmdType)) {
	case "restart", "reboot", "shutdown":
		return true
	default:
		return false
	}
}

// parsePowerCommandPayload extracts delaySeconds, force, and message from the raw payload.
// Aceita tanto map[string]any (JSON ja parseado) quanto string (JSON raw).
func parsePowerCommandPayload(payload any) powerCommandPayload {
	if payload == nil {
		return powerCommandPayload{}
	}

	toString := func(v any) string {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
		return ""
	}
	toBool := func(v any) bool {
		// Aceita bool nativo e string "true"/"false" (JSON strings comuns).
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
			// Cenario comum: numero serializado como string no JSON (ex: "30").
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

	// Primeiro, tenta payload como map[string]any (JSON ja parseado).
	m, ok := payload.(map[string]any)
	if !ok {
		// Fallback: payload pode ser uma string JSON (comum quando o servidor
		// envia o payload como JSON-encoded string dentro do envelope NATS).
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
			return powerCommandPayload{}
		}
	}

	return powerCommandPayload{
		DelaySeconds: toInt(m["delaySeconds"]),
		Force:        toBool(m["force"]),
		Message:      toString(m["message"]),
		DeferMinutes: toInt(m["deferMinutes"]),
		MaxDefers:    toInt(m["maxDefers"]),
	}
}
