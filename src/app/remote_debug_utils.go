package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"discovery/app/netutil"

	"github.com/nats-io/nats.go"
)

func parseRemoteDebugCommand(payload any) (remoteDebugCommand, error) {
	if payload == nil {
		return remoteDebugCommand{}, fmt.Errorf("payload ausente")
	}
	b, err := decodeRemoteDebugPayloadBytes(payload)
	if err != nil {
		return remoteDebugCommand{}, err
	}
	var raw remoteDebugCommandPayload
	if err := json.Unmarshal(b, &raw); err != nil {
		return remoteDebugCommand{}, err
	}
	cmd := remoteDebugCommand{
		Action:       strings.TrimSpace(ptrStringOrEmpty(raw.Action)),
		SessionID:    strings.TrimSpace(ptrStringOrEmpty(raw.SessionID)),
		LogLevel:     strings.TrimSpace(ptrStringOrEmpty(raw.LogLevel)),
		StartedAtUTC: strings.TrimSpace(ptrStringOrEmpty(raw.StartedAtUTC)),
		ExpiresAtUTC: strings.TrimSpace(ptrStringOrEmpty(raw.ExpiresAtUTC)),
		StoppedAtUTC: strings.TrimSpace(ptrStringOrEmpty(raw.StoppedAtUTC)),
	}
	if raw.Stream != nil {
		cmd.Stream.NatsSubject = strings.TrimSpace(ptrStringOrEmpty(raw.Stream.NatsSubject))
		cmd.Stream.NatsWssURL = strings.TrimSpace(ptrStringOrEmpty(raw.Stream.NatsWssURL))
	}
	cmd.LogLevel = normalizeRemoteDebugLevel(cmd.LogLevel)
	return cmd, nil
}

func decodeRemoteDebugPayloadBytes(payload any) ([]byte, error) {
	switch typed := payload.(type) {
	case string:
		raw := strings.TrimSpace(typed)
		if raw == "" {
			return nil, fmt.Errorf("payload ausente")
		}
		return []byte(raw), nil
	case []byte:
		raw := bytes.TrimSpace(typed)
		if len(raw) == 0 {
			return nil, fmt.Errorf("payload ausente")
		}
		return raw, nil
	case json.RawMessage:
		raw := bytes.TrimSpace(typed)
		if len(raw) == 0 {
			return nil, fmt.Errorf("payload ausente")
		}
		return raw, nil
	default:
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		raw := bytes.TrimSpace(b)
		if len(raw) == 0 || strings.EqualFold(string(raw), "null") {
			return nil, fmt.Errorf("payload ausente")
		}
		return raw, nil
	}
}

func ptrStringOrEmpty(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func computeRemoteDebugDeadline(expiresAt string, now time.Time) time.Time {
	defaultDeadline := now.Add(remoteDebugDefaultSessionCap)
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

func buildRemoteDebugPublishers(cfg DebugConfig, stream remoteDebugStreamConfig, token string, clientID, siteID string) ([]remoteDebugPublisher, error) {
	var publishers []remoteDebugPublisher
	subject := resolveRemoteDebugSubject(strings.TrimSpace(stream.NatsSubject), clientID, siteID, strings.TrimSpace(cfg.AgentID))
	if subject == "" {
		return nil, fmt.Errorf("subject NATS ausente no comando de remote debug")
	}
	if !isCanonicalRemoteDebugSubject(subject) {
		return nil, fmt.Errorf("subject NATS inválido para remote debug: esperado sufixo .remote-debug.log, recebido=%q", subject)
	}

	if p, err := newNATSRemoteDebugPublisher(strings.TrimSpace(cfg.NatsServer), token, subject, "nats"); err == nil {
		publishers = append(publishers, p)
	}

	wss := strings.TrimSpace(stream.NatsWssURL)
	if wss == "" {
		wss = strings.TrimSpace(cfg.NatsWsServer)
	}
	if p, err := newNATSRemoteDebugPublisher(wss, token, subject, "nats-wss"); err == nil {
		publishers = append(publishers, p)
	}

	if len(publishers) == 0 {
		return nil, fmt.Errorf("nenhum transporte remoto disponivel")
	}
	return publishers, nil
}

func isCanonicalRemoteDebugSubject(subject string) bool {
	subject = strings.TrimSpace(strings.ToLower(subject))
	if subject == "" {
		return false
	}
	if strings.ContainsAny(subject, " *\t\r\n") {
		return false
	}
	return strings.HasSuffix(subject, ".remote-debug.log")
}

func resolveRemoteDebugSubject(subject, clientID, siteID, agentID string) string {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return ""
	}
	subject = strings.ReplaceAll(subject, "<clientId>", strings.TrimSpace(clientID))
	subject = strings.ReplaceAll(subject, "<siteId>", strings.TrimSpace(siteID))
	subject = strings.ReplaceAll(subject, "<agentId>", strings.TrimSpace(agentID))
	return subject
}

func truncatePayloadForLog(payload any) string {
	if payload == nil {
		return "<nil>"
	}
	switch v := payload.(type) {
	case string:
		if len(v) > 200 {
			return v[:200] + "..."
		}
		return v
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

func newNATSRemoteDebugPublisher(server, token, subject, name string) (remoteDebugPublisher, error) {
	server = strings.TrimSpace(server)
	token = strings.TrimSpace(token)
	subject = strings.TrimSpace(subject)
	if server == "" || token == "" || subject == "" {
		return nil, fmt.Errorf("config NATS incompleta")
	}
	normalizedToken, err := netutil.NormalizeAgentToken(token)
	if err != nil {
		return nil, err
	}

	nc, err := nats.Connect(server,
		nats.Name("discovery-remote-debug"),
		nats.Token(normalizedToken),
		nats.Timeout(5*time.Second),
		nats.ReconnectWait(2*time.Second),
		nats.MaxReconnects(1),
	)
	if err != nil {
		return nil, err
	}
	return &natsRemoteDebugPublisher{name: name, subject: subject, conn: nc}, nil
}

func splitLines(raw string) []string {
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

func truncateRemoteDebugMessage(s string) string {
	s = strings.TrimSpace(s)
	const maxLen = 4096
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

func formatRemoteDebugMessageWithOrigin(origin, message string) string {
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

func normalizeRemoteDebugLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "trace", "debug", "info", "warn", "error":
		return strings.ToLower(strings.TrimSpace(level))
	default:
		return "info"
	}
}

func normalizeRemoteDebugStreamLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "trace", "debug", "info", "warn", "error":
		return strings.ToLower(strings.TrimSpace(level))
	default:
		return "info"
	}
}

func remoteDebugLevelValue(level string) int {
	switch normalizeRemoteDebugLevel(level) {
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

func detectRemoteDebugLevel(line string) string {
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

func isRemoteDebugCommandType(cmdType string) bool {
	switch strings.ToLower(strings.TrimSpace(cmdType)) {
	case "8", "remotedebug", "remote-debug":
		return true
	default:
		return false
	}
}
