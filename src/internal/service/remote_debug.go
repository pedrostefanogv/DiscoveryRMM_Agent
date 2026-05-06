package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"discovery/app/netutil"

	"github.com/nats-io/nats.go"

	"discovery/internal/agentconn"
)

const (
	serviceRemoteDebugDefaultSessionCap = time.Hour
	serviceRemoteDebugQueueSize         = 2048
)

type serviceRemoteDebugCommand struct {
	Action       string                         `json:"action"`
	SessionID    string                         `json:"sessionId"`
	LogLevel     string                         `json:"logLevel"`
	ExpiresAtUTC string                         `json:"expiresAtUtc"`
	Stream       serviceRemoteDebugStreamConfig `json:"stream"`
}

type serviceRemoteDebugStreamConfig struct {
	NatsSubject string `json:"natsSubject"`
	NatsWssURL  string `json:"natsWssUrl"`
}

type serviceRemoteDebugLogMessage struct {
	SessionID    string `json:"sessionId"`
	AgentID      string `json:"agentId"`
	Message      string `json:"message"`
	Level        string `json:"level"`
	TimestampUTC string `json:"timestampUtc"`
	Sequence     uint64 `json:"sequence"`
}

type serviceQueuedRemoteDebugLine struct {
	message string
	level   string
}

type serviceRemoteDebugPublisher interface {
	Name() string
	Publish(ctx context.Context, msg serviceRemoteDebugLogMessage) error
	Close() error
}

type serviceNATSRemoteDebugPublisher struct {
	name    string
	subject string
	conn    *nats.Conn
}

func (p *serviceNATSRemoteDebugPublisher) Name() string { return p.name }

func (p *serviceNATSRemoteDebugPublisher) Publish(_ context.Context, msg serviceRemoteDebugLogMessage) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if err := p.conn.Publish(p.subject, payload); err != nil {
		return err
	}
	p.conn.Flush()
	return p.conn.LastError()
}

func (p *serviceNATSRemoteDebugPublisher) Close() error {
	if p.conn != nil {
		p.conn.Close()
	}
	return nil
}

type serviceRemoteDebugSession struct {
	sessionID   string
	agentID     string
	minLevel    int
	deadline    time.Time
	logQueue    chan serviceQueuedRemoteDebugLine
	cancel      context.CancelFunc
	publishers  []serviceRemoteDebugPublisher
	activeIndex int
}

type serviceRemoteDebugManager struct {
	mu            sync.Mutex
	activeSession *serviceRemoteDebugSession
	logf          func(string)
	getConfig     func() agentconn.Config
}

func newServiceRemoteDebugManager(logf func(string), getConfig func() agentconn.Config) *serviceRemoteDebugManager {
	if logf == nil {
		logf = func(string) {}
	}
	if getConfig == nil {
		getConfig = func() agentconn.Config { return agentconn.Config{} }
	}
	return &serviceRemoteDebugManager{
		logf:      logf,
		getConfig: getConfig,
	}
}

func (m *serviceRemoteDebugManager) HandleCommand(parent context.Context, cmdType string, payload any) (bool, int, string, string) {
	if !isRemoteDebugCommandType(cmdType) {
		return false, 0, "", ""
	}

	cmd, err := parseServiceRemoteDebugCommand(payload)
	if err != nil {
		return true, 2, "", "payload remoto invalido: " + err.Error()
	}
	action := strings.ToLower(strings.TrimSpace(cmd.Action))
	switch action {
	case "start":
		if err := m.startSession(parent, cmd); err != nil {
			return true, 1, "", err.Error()
		}
		return true, 0, fmt.Sprintf("remote debug (service) iniciado sessionId=%s", strings.TrimSpace(cmd.SessionID)), ""
	case "stop":
		stopped := m.stopSession(strings.TrimSpace(cmd.SessionID), "stop")
		if !stopped {
			return true, 0, "remote debug (service) sem sessao ativa para encerrar", ""
		}
		return true, 0, fmt.Sprintf("remote debug (service) encerrado sessionId=%s", strings.TrimSpace(cmd.SessionID)), ""
	default:
		return true, 2, "", "acao remota invalida"
	}
}

func (m *serviceRemoteDebugManager) OnCommandOutput(cmdType, output, errText string) {
	if isRemoteDebugCommandType(cmdType) {
		return
	}
	for _, line := range serviceRemoteDebugSplitLines(output) {
		m.EnqueueRawLog(line, "info")
	}
	for _, line := range serviceRemoteDebugSplitLines(errText) {
		m.EnqueueRawLog(line, "error")
	}
}

func (m *serviceRemoteDebugManager) EnqueueRawLog(message, level string) {
	m.mu.Lock()
	session := m.activeSession
	m.mu.Unlock()
	if session == nil {
		return
	}
	m.enqueueToSession(session, message, level)
}

func (m *serviceRemoteDebugManager) enqueueToSession(session *serviceRemoteDebugSession, message, level string) {
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	level = serviceRemoteDebugNormalizeLevel(level)
	if serviceRemoteDebugLevelValue(level) < session.minLevel {
		return
	}
	select {
	case session.logQueue <- serviceQueuedRemoteDebugLine{message: message, level: level}:
	default:
		m.logf("[remote-debug][service] fila cheia: log descartado")
	}
}

func (m *serviceRemoteDebugManager) startSession(parent context.Context, cmd serviceRemoteDebugCommand) error {
	sessionID := strings.TrimSpace(cmd.SessionID)
	if sessionID == "" {
		return fmt.Errorf("sessionId ausente")
	}

	cfg := m.getConfig()
	token := strings.TrimSpace(cfg.AuthToken)
	agentID := strings.TrimSpace(cfg.AgentID)
	if token == "" || agentID == "" {
		return fmt.Errorf("authToken/agentId ausentes para remote debug no service")
	}

	deadline := computeServiceRemoteDebugDeadline(strings.TrimSpace(cmd.ExpiresAtUTC), time.Now().UTC())
	publishers, err := buildServiceRemoteDebugPublishers(cfg, cmd.Stream, token)
	if err != nil {
		return err
	}

	baseCtx := parent
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	ctx, cancel := context.WithCancel(baseCtx)
	session := &serviceRemoteDebugSession{
		sessionID:  sessionID,
		agentID:    agentID,
		minLevel:   serviceRemoteDebugLevelValue(cmd.LogLevel),
		deadline:   deadline,
		logQueue:   make(chan serviceQueuedRemoteDebugLine, serviceRemoteDebugQueueSize),
		cancel:     cancel,
		publishers: publishers,
	}

	m.mu.Lock()
	previous := m.activeSession
	m.activeSession = session
	m.mu.Unlock()

	if previous != nil {
		m.stopGivenSession(previous, "replaced")
	}

	m.logf(fmt.Sprintf("[remote-debug][service] sessao iniciada: sessionId=%s deadline=%s transport=%s", sessionID, deadline.Format(time.RFC3339), session.publishers[0].Name()))
	go m.publishLoop(ctx, session)
	go m.autoStopAtDeadline(sessionID, deadline)
	return nil
}

func (m *serviceRemoteDebugManager) autoStopAtDeadline(sessionID string, deadline time.Time) {
	wait := time.Until(deadline)
	if wait < 0 {
		wait = 0
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	<-timer.C
	m.stopSession(sessionID, "timeout")
}

func (m *serviceRemoteDebugManager) publishLoop(ctx context.Context, session *serviceRemoteDebugSession) {
	var seq uint64
	for {
		select {
		case <-ctx.Done():
			return
		case item := <-session.logQueue:
			if strings.TrimSpace(item.message) == "" {
				continue
			}
			seq++
			msg := serviceRemoteDebugLogMessage{
				SessionID:    session.sessionID,
				AgentID:      session.agentID,
				Message:      serviceRemoteDebugFormatMessageWithOrigin("service", serviceRemoteDebugTruncateMessage(item.message)),
				Level:        serviceRemoteDebugNormalizeLevel(item.level),
				TimestampUTC: time.Now().UTC().Format(time.RFC3339),
				Sequence:     seq,
			}
			if err := m.publishWithFallback(ctx, session, msg); err != nil {
				m.logf("[remote-debug][service] falha ao publicar log remoto: " + err.Error())
			}
		}
	}
}

func (m *serviceRemoteDebugManager) publishWithFallback(ctx context.Context, session *serviceRemoteDebugSession, msg serviceRemoteDebugLogMessage) error {
	for idx := session.activeIndex; idx < len(session.publishers); idx++ {
		pub := session.publishers[idx]
		if err := pub.Publish(ctx, msg); err != nil {
			m.logf(fmt.Sprintf("[remote-debug][service] publish falhou em %s: %v", pub.Name(), err))
			_ = pub.Close()
			session.activeIndex = idx + 1
			continue
		}
		if idx != session.activeIndex {
			m.logf(fmt.Sprintf("[remote-debug][service] fallback aplicado para transporte=%s", pub.Name()))
			session.activeIndex = idx
		}
		return nil
	}
	return fmt.Errorf("nenhum transporte remoto disponivel")
}

func (m *serviceRemoteDebugManager) stopSession(sessionID, reason string) bool {
	sessionID = strings.TrimSpace(sessionID)

	m.mu.Lock()
	session := m.activeSession
	if session == nil {
		m.mu.Unlock()
		return false
	}
	if sessionID != "" && !strings.EqualFold(session.sessionID, sessionID) {
		m.mu.Unlock()
		return false
	}
	m.activeSession = nil
	m.mu.Unlock()

	m.stopGivenSession(session, reason)
	return true
}

func (m *serviceRemoteDebugManager) stopGivenSession(session *serviceRemoteDebugSession, reason string) {
	if session == nil {
		return
	}
	if session.cancel != nil {
		session.cancel()
	}
	for _, pub := range session.publishers {
		_ = pub.Close()
	}
	m.logf(fmt.Sprintf("[remote-debug][service] sessao encerrada: sessionId=%s reason=%s", session.sessionID, strings.TrimSpace(reason)))
}

func parseServiceRemoteDebugCommand(payload any) (serviceRemoteDebugCommand, error) {
	if payload == nil {
		return serviceRemoteDebugCommand{}, fmt.Errorf("payload ausente")
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return serviceRemoteDebugCommand{}, err
	}
	var cmd serviceRemoteDebugCommand
	if err := json.Unmarshal(b, &cmd); err != nil {
		return serviceRemoteDebugCommand{}, err
	}
	cmd.Action = strings.TrimSpace(cmd.Action)
	cmd.SessionID = strings.TrimSpace(cmd.SessionID)
	cmd.LogLevel = serviceRemoteDebugNormalizeLevel(cmd.LogLevel)
	return cmd, nil
}

func computeServiceRemoteDebugDeadline(expiresAt string, now time.Time) time.Time {
	defaultDeadline := now.Add(serviceRemoteDebugDefaultSessionCap)
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

type serviceRemoteDebugEndpoint struct {
	server string
	name   string
}

func buildServiceRemoteDebugPublishers(cfg agentconn.Config, stream serviceRemoteDebugStreamConfig, token string) ([]serviceRemoteDebugPublisher, error) {
	subject := strings.TrimSpace(stream.NatsSubject)
	if subject == "" {
		return nil, fmt.Errorf("subject NATS ausente no comando de remote debug")
	}
	if !isCanonicalRemoteDebugSubject(subject) {
		return nil, fmt.Errorf("subject NATS invalido para remote debug: esperado sufixo .remote-debug.log")
	}

	var candidates []serviceRemoteDebugEndpoint
	seen := make(map[string]struct{})

	if wss := strings.TrimSpace(stream.NatsWssURL); wss != "" {
		candidates = appendServiceRemoteDebugCandidate(candidates, seen, wss, "nats-wss")
	}
	if cfg.NatsWsServer != "" {
		candidates = appendServiceRemoteDebugCandidate(candidates, seen, cfg.NatsWsServer, "nats-wss")
	}
	if cfg.NatsServer != "" {
		candidates = appendServiceRemoteDebugCandidate(candidates, seen, cfg.NatsServer, "nats")
	}

	host := serviceRemoteDebugExtractHost(cfg.NatsServerHost)
	if host != "" {
		if native := "nats://" + net.JoinHostPort(host, "4222"); !cfg.NatsUseWssExternal {
			candidates = appendServiceRemoteDebugCandidate(candidates, seen, native, "nats")
		}
		if wss, err := serviceBuildExternalNATSWSSURL(host); err == nil {
			candidates = appendServiceRemoteDebugCandidate(candidates, seen, wss, "nats-wss")
		}
	}

	apiHost := serviceRemoteDebugExtractHost(cfg.ApiServer)
	if apiHost != "" {
		candidates = appendServiceRemoteDebugCandidate(candidates, seen, "nats://"+net.JoinHostPort(apiHost, "4222"), "nats")
		if wss, err := serviceBuildExternalNATSWSSURL(apiHost); err == nil {
			candidates = appendServiceRemoteDebugCandidate(candidates, seen, wss, "nats-wss")
		}
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("nenhum endpoint NATS disponivel para remote debug no modo service")
	}

	var publishers []serviceRemoteDebugPublisher
	var lastErr error
	for _, candidate := range candidates {
		p, err := newServiceNATSRemoteDebugPublisher(candidate.server, token, subject, candidate.name)
		if err != nil {
			lastErr = err
			continue
		}
		publishers = append(publishers, p)
	}

	if len(publishers) == 0 {
		if lastErr != nil {
			return nil, fmt.Errorf("nenhum transporte remoto disponivel: %w", lastErr)
		}
		return nil, fmt.Errorf("nenhum transporte remoto disponivel")
	}
	return publishers, nil
}

func appendServiceRemoteDebugCandidate(candidates []serviceRemoteDebugEndpoint, seen map[string]struct{}, server, name string) []serviceRemoteDebugEndpoint {
	normalized, err := normalizeServiceNATSEndpoint(server)
	if err != nil {
		return candidates
	}
	key := strings.ToLower(strings.TrimSpace(normalized))
	if _, exists := seen[key]; exists {
		return candidates
	}
	seen[key] = struct{}{}
	if strings.TrimSpace(name) == "" {
		name = serviceRemoteDebugTransportName(normalized)
	}
	return append(candidates, serviceRemoteDebugEndpoint{server: normalized, name: name})
}

func normalizeServiceNATSEndpoint(server string) (string, error) {
	server = strings.TrimSpace(server)
	if server == "" {
		return "", fmt.Errorf("endpoint NATS vazio")
	}
	if !strings.Contains(server, "://") {
		server = "nats://" + server
	}

	u, err := url.Parse(server)
	if err != nil {
		return "", fmt.Errorf("endpoint NATS invalido: %w", err)
	}
	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	switch scheme {
	case "nats", "ws", "wss":
	default:
		return "", fmt.Errorf("scheme NATS nao suportado: %s", scheme)
	}
	if strings.TrimSpace(u.Host) == "" {
		return "", fmt.Errorf("host NATS ausente")
	}

	serviceEnsureDefaultNATSPort(u, scheme)
	if (scheme == "ws" || scheme == "wss") && strings.TrimSpace(u.Path) == "" {
		u.Path = "/nats/"
	}

	return u.String(), nil
}

func serviceEnsureDefaultNATSPort(u *url.URL, scheme string) {
	if strings.Contains(u.Host, ":") {
		return
	}
	switch strings.ToLower(strings.TrimSpace(scheme)) {
	case "nats":
		u.Host += ":4222"
	case "ws":
		u.Host += ":80"
	case "wss":
		u.Host += ":443"
	}
}

func serviceRemoteDebugTransportName(server string) string {
	u, err := url.Parse(strings.TrimSpace(server))
	if err != nil {
		return "nats"
	}
	switch strings.ToLower(strings.TrimSpace(u.Scheme)) {
	case "ws", "wss":
		return "nats-wss"
	default:
		return "nats"
	}
}

func serviceBuildExternalNATSWSSURL(host string) (string, error) {
	host = serviceRemoteDebugExtractHost(host)
	if host == "" {
		return "", fmt.Errorf("host vazio")
	}
	if !strings.Contains(host, ":") {
		host += ":443"
	}
	return "wss://" + host + "/nats/", nil
}

func serviceRemoteDebugExtractHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		if parsed, err := url.Parse(raw); err == nil {
			raw = strings.TrimSpace(parsed.Host)
		}
	}
	if h, _, err := net.SplitHostPort(raw); err == nil {
		raw = strings.TrimSpace(h)
	}
	return strings.Trim(strings.TrimSpace(raw), "[]")
}

func isCanonicalRemoteDebugSubject(subject string) bool {
	subject = strings.TrimSpace(strings.ToLower(subject))
	if subject == "" {
		return false
	}
	if strings.ContainsAny(subject, " *>\t\r\n") {
		return false
	}
	return strings.HasSuffix(subject, ".remote-debug.log")
}

func newServiceNATSRemoteDebugPublisher(server, token, subject, name string) (serviceRemoteDebugPublisher, error) {
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
		nats.Name("discovery-service-remote-debug"),
		nats.Token(normalizedToken),
		nats.Timeout(5*time.Second),
		nats.ReconnectWait(2*time.Second),
		nats.MaxReconnects(1),
	)
	if err != nil {
		return nil, err
	}
	return &serviceNATSRemoteDebugPublisher{name: name, subject: subject, conn: nc}, nil
}

func serviceRemoteDebugSplitLines(raw string) []string {
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

func serviceRemoteDebugTruncateMessage(s string) string {
	s = strings.TrimSpace(s)
	const maxLen = 4096
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

func serviceRemoteDebugNormalizeLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug", "info", "warn", "error":
		return strings.ToLower(strings.TrimSpace(level))
	default:
		return "info"
	}
}

func serviceRemoteDebugLevelValue(level string) int {
	switch serviceRemoteDebugNormalizeLevel(level) {
	case "debug":
		return 0
	case "info":
		return 1
	case "warn":
		return 2
	case "error":
		return 3
	default:
		return 1
	}
}

func serviceRemoteDebugDetectLevel(line string) string {
	l := strings.ToLower(strings.TrimSpace(line))
	switch {
	case strings.Contains(l, "[error]") || strings.Contains(l, " error"):
		return "error"
	case strings.Contains(l, "[warn]") || strings.Contains(l, " warning"):
		return "warn"
	case strings.Contains(l, "[debug]"):
		return "debug"
	default:
		return "info"
	}
}

func serviceRemoteDebugFormatMessageWithOrigin(origin, message string) string {
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

func isRemoteDebugCommandType(cmdType string) bool {
	switch strings.ToLower(strings.TrimSpace(cmdType)) {
	case "8", "remotedebug", "remote-debug":
		return true
	default:
		return false
	}
}
