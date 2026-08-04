package remotedebug

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// DefaultSessionCap é o teto máximo de duração de uma sessão de remote debug.
const DefaultSessionCap = time.Hour

// QueueSize é o tamanho da fila de logs em memória por sessão.
const QueueSize = 2048

// ComputeDeadline calcula o deadline da sessão, respeitando o teto de 1h.
func ComputeDeadline(expiresAt string, now time.Time) time.Time {
	defaultDeadline := now.Add(DefaultSessionCap)
	expiresAt = strings.TrimSpace(expiresAt)
	if expiresAt == "" {
		return defaultDeadline
	}
	t, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return defaultDeadline
	}
	t = t.UTC()
	if t.Before(now.UTC()) {
		return now.UTC()
	}
	if t.Before(defaultDeadline) {
		return t
	}
	return defaultDeadline
}

// IsCanonicalSubject verifica se o subject NATS segue o padrão .remote-debug.log.
func IsCanonicalSubject(subject string) bool {
	subject = strings.TrimSpace(strings.ToLower(subject))
	if subject == "" {
		return false
	}
	if strings.ContainsAny(subject, " *\t\r\n") {
		return false
	}
	return strings.HasSuffix(subject, ".remote-debug.log")
}

// ResolveSubject substitui placeholders <clientId>/<siteId>/<agentId> no subject.
func ResolveSubject(subject, clientID, siteID, agentID string) string {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return ""
	}
	subject = strings.ReplaceAll(subject, "<clientId>", strings.TrimSpace(clientID))
	subject = strings.ReplaceAll(subject, "<siteId>", strings.TrimSpace(siteID))
	subject = strings.ReplaceAll(subject, "<agentId>", strings.TrimSpace(agentID))
	return subject
}

// TruncateMessage limita o tamanho de uma mensagem de log.
func TruncateMessage(s string) string {
	s = strings.TrimSpace(s)
	const maxLen = 4096
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

// FormatMessageWithOrigin prefixa a mensagem com a origem ([origin] ...).
func FormatMessageWithOrigin(origin, message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	origin = strings.ToLower(strings.TrimSpace(origin))
	if origin == "" {
		return message
	}
	prefix := "[" + origin + "]"
	lowerMsg := strings.ToLower(message)
	if lowerMsg == prefix || strings.HasPrefix(lowerMsg, prefix+" ") {
		return message
	}
	return prefix + " " + message
}

// NormalizeLevel normaliza um nível de log para um dos valores canônicos.
func NormalizeLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "trace", "debug", "info", "warn", "error":
		return strings.ToLower(strings.TrimSpace(level))
	default:
		return "info"
	}
}

// NormalizeStreamLevel normaliza o nível para o stream (mesmo que NormalizeLevel).
func NormalizeStreamLevel(level string) string {
	return NormalizeLevel(level)
}

// LevelValue converte um nível em um valor numérico para filtragem.
func LevelValue(level string) int {
	switch NormalizeLevel(level) {
	case "trace":
		return 0
	case "debug":
		return 1
	case "info":
		return 2
	case "warn":
		return 3
	case "error":
		return 4
	default:
		return 2
	}
}

// DetectLevel infere o nível de uma linha de log por heurística.
func DetectLevel(line string) string {
	l := strings.ToLower(strings.TrimSpace(line))

	if strings.Contains(l, "[error]") {
		return "error"
	}
	if strings.Contains(l, "[warn]") {
		return "warn"
	}
	if strings.Contains(l, "[debug]") {
		return "debug"
	}
	if strings.Contains(l, "[trace]") {
		return "trace"
	}
	if strings.Contains(l, "[info]") {
		return "info"
	}

	if strings.Contains(l, "falha") || strings.Contains(l, "falhou") {
		return "error"
	}
	if strings.Contains(l, "panic") {
		return "error"
	}
	if strings.Contains(l, "negado") {
		return "error"
	}
	if strings.Contains(l, "violation") {
		return "error"
	}
	if strings.Contains(l, " erro ") || strings.Contains(l, " erro:") || strings.HasPrefix(l, "erro ") {
		return "error"
	}
	if strings.Contains(l, " error ") || strings.Contains(l, " error:") || strings.HasPrefix(l, "error ") {
		return "error"
	}
	if strings.Contains(l, " fail") || strings.Contains(l, "fail ") || strings.Contains(l, "failed") {
		return "error"
	}

	if strings.Contains(l, "aviso") || strings.Contains(l, "avisado") {
		return "warn"
	}
	if strings.Contains(l, " warning") || strings.Contains(l, " warn ") || strings.HasPrefix(l, "warn ") {
		return "warn"
	}
	if strings.Contains(l, "descartando") {
		return "warn"
	}
	if strings.Contains(l, "ignorado") {
		return "warn"
	}
	if strings.Contains(l, "rejeitad") {
		return "warn"
	}
	if strings.Contains(l, "adiado") {
		return "warn"
	}
	if strings.Contains(l, "cancelado") {
		return "warn"
	}
	if strings.Contains(l, "timeout") {
		return "warn"
	}
	if strings.Contains(l, "ausente") {
		return "warn"
	}
	if strings.Contains(l, "perdida") {
		return "warn"
	}
	if strings.Contains(l, "atingido") {
		return "warn"
	}
	if strings.Contains(l, "indisponivel") {
		return "warn"
	}
	if strings.Contains(l, "inválido") || strings.Contains(l, "invalido") {
		return "warn"
	}

	return "info"
}

// SplitLines divide um texto em linhas não-vazias normalizadas.
func SplitLines(raw string) []string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	parts := strings.Split(raw, "\n")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

// TruncatePayloadForLog limita o payload para exibição em logs.
func TruncatePayloadForLog(payload any) string {
	if payload == nil {
		return "<nil>"
	}
	switch v := payload.(type) {
	case string:
		if len(v) > 200 {
			return v[:200] + "..."
		}
		return v
	case []byte:
		s := string(v)
		if len(s) > 200 {
			return s[:200] + "..."
		}
		return s
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("<marshal error: %v>", err)
		}
		if len(b) > 300 {
			return string(b[:300]) + "..."
		}
		return string(b)
	}
}
