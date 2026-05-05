package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

const (
	remoteDebugDefaultSessionCap = time.Hour
	remoteDebugQueueSize         = 2048
)

type remoteDebugCommand struct {
	Action       string                  `json:"action"`
	SessionID    string                  `json:"sessionId"`
	LogLevel     string                  `json:"logLevel"`
	StartedAtUTC string                  `json:"startedAtUtc"`
	ExpiresAtUTC string                  `json:"expiresAtUtc"`
	StoppedAtUTC string                  `json:"stoppedAtUtc"`
	Stream       remoteDebugStreamConfig `json:"stream"`
}

type remoteDebugStreamConfig struct {
	NatsSubject string `json:"natsSubject"`
	NatsWssURL  string `json:"natsWssUrl"`
}

type remoteDebugLogMessage struct {
	SessionID    string `json:"sessionId"`
	AgentID      string `json:"agentId"`
	Message      string `json:"message"`
	Level        string `json:"level"`
	TimestampUTC string `json:"timestampUtc"`
	Sequence     uint64 `json:"sequence"`
}

type queuedRemoteDebugLine struct {
	message string
	level   string
}

type remoteDebugPublisher interface {
	Name() string
	Publish(ctx context.Context, msg remoteDebugLogMessage) error
	Close() error
}

type natsRemoteDebugPublisher struct {
	name    string
	subject string
	conn    *nats.Conn
}

func (p *natsRemoteDebugPublisher) Name() string { return p.name }

func (p *natsRemoteDebugPublisher) Publish(_ context.Context, msg remoteDebugLogMessage) error {
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

func (p *natsRemoteDebugPublisher) Close() error {
	if p.conn != nil {
		p.conn.Close()
	}
	return nil
}

type remoteDebugSession struct {
	sessionID   string
	agentID     string
	minLevel    int
	deadline    time.Time
	logQueue    chan queuedRemoteDebugLine
	cancel      context.CancelFunc
	unsubscribe func()
	publishers  []remoteDebugPublisher
	activeIndex int
}

type remoteDebugManager struct {
	mu            sync.Mutex
	activeSession *remoteDebugSession
	logf          func(string)
	getDebugCfg   func() DebugConfig
	subscribeLogs func(func(string)) func()
}

func newRemoteDebugManager(logf func(string), getDebugCfg func() DebugConfig, subscribeLogs func(func(string)) func()) *remoteDebugManager {
	if logf == nil {
		logf = func(string) {}
	}
	if getDebugCfg == nil {
		getDebugCfg = func() DebugConfig { return DebugConfig{} }
	}
	if subscribeLogs == nil {
		subscribeLogs = func(func(string)) func() { return func() {} }
	}
	return &remoteDebugManager{
		logf:          logf,
		getDebugCfg:   getDebugCfg,
		subscribeLogs: subscribeLogs,
	}
}

func (m *remoteDebugManager) HandleCommand(_ context.Context, cmdType string, payload any) (bool, int, string, string) {
	if !isRemoteDebugCommandType(cmdType) {
		return false, 0, "", ""
	}

	cmd, err := parseRemoteDebugCommand(payload)
	if err != nil {
		return true, 2, "", "payload remoto invalido: " + err.Error()
	}
	action := strings.ToLower(strings.TrimSpace(cmd.Action))
	switch action {
	case "start":
		if err := m.startSession(cmd); err != nil {
			return true, 1, "", err.Error()
		}
		return true, 0, fmt.Sprintf("remote debug iniciado sessionId=%s", strings.TrimSpace(cmd.SessionID)), ""
	case "stop":
		stopped := m.stopSession(strings.TrimSpace(cmd.SessionID), "stop")
		if !stopped {
			return true, 0, "remote debug sem sessao ativa para encerrar", ""
		}
		return true, 0, fmt.Sprintf("remote debug encerrado sessionId=%s", strings.TrimSpace(cmd.SessionID)), ""
	default:
		return true, 2, "", "acao remota invalida"
	}
}

func (m *remoteDebugManager) OnCommandOutput(cmdType, output, errText string) {
	if isRemoteDebugCommandType(cmdType) {
		return
	}
	for _, line := range splitLines(output) {
		m.enqueue(line, "info")
	}
	for _, line := range splitLines(errText) {
		m.enqueue(line, "error")
	}
}

func (m *remoteDebugManager) startSession(cmd remoteDebugCommand) error {
	sessionID := strings.TrimSpace(cmd.SessionID)
	if sessionID == "" {
		return fmt.Errorf("sessionId ausente")
	}

	cfg := m.getDebugCfg()
	token := strings.TrimSpace(cfg.AuthToken)
	agentID := strings.TrimSpace(cfg.AgentID)
	if token == "" || agentID == "" {
		return fmt.Errorf("authToken/agentId ausentes para remote debug")
	}

	deadline := computeRemoteDebugDeadline(strings.TrimSpace(cmd.ExpiresAtUTC), time.Now().UTC())
	publishers, err := buildRemoteDebugPublishers(cfg, cmd.Stream, token)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	session := &remoteDebugSession{
		sessionID:  sessionID,
		agentID:    agentID,
		minLevel:   remoteDebugLevelValue(cmd.LogLevel),
		deadline:   deadline,
		logQueue:   make(chan queuedRemoteDebugLine, remoteDebugQueueSize),
		cancel:     cancel,
		publishers: publishers,
	}

	unsubscribe := m.subscribeLogs(func(line string) {
		m.enqueueWithSession(sessionID, line, detectRemoteDebugLevel(line))
	})
	session.unsubscribe = unsubscribe

	m.mu.Lock()
	previous := m.activeSession
	m.activeSession = session
	m.mu.Unlock()

	if previous != nil {
		m.stopGivenSession(previous, "replaced")
	}

	m.logf(fmt.Sprintf("[remote-debug] sessao iniciada: sessionId=%s deadline=%s transport=%s", sessionID, deadline.Format(time.RFC3339), session.publishers[0].Name()))
	go m.publishLoop(ctx, session)
	go m.autoStopAtDeadline(sessionID, deadline)
	return nil
}

func (m *remoteDebugManager) autoStopAtDeadline(sessionID string, deadline time.Time) {
	wait := time.Until(deadline)
	if wait < 0 {
		wait = 0
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	<-timer.C
	m.stopSession(sessionID, "timeout")
}

func (m *remoteDebugManager) publishLoop(ctx context.Context, session *remoteDebugSession) {
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
			msg := remoteDebugLogMessage{
				SessionID:    session.sessionID,
				AgentID:      session.agentID,
				Message:      truncateRemoteDebugMessage(item.message),
				Level:        normalizeRemoteDebugLevel(item.level),
				TimestampUTC: time.Now().UTC().Format(time.RFC3339),
				Sequence:     seq,
			}
			if err := m.publishWithFallback(ctx, session, msg); err != nil {
				m.logf("[remote-debug] falha ao publicar log remoto: " + err.Error())
			}
		}
	}
}

func (m *remoteDebugManager) publishWithFallback(ctx context.Context, session *remoteDebugSession, msg remoteDebugLogMessage) error {
	for idx := session.activeIndex; idx < len(session.publishers); idx++ {
		pub := session.publishers[idx]
		if err := pub.Publish(ctx, msg); err != nil {
			m.logf(fmt.Sprintf("[remote-debug] publish falhou em %s: %v", pub.Name(), err))
			_ = pub.Close()
			session.activeIndex = idx + 1
			continue
		}
		if idx != session.activeIndex {
			m.logf(fmt.Sprintf("[remote-debug] fallback aplicado para transporte=%s", pub.Name()))
			session.activeIndex = idx
		}
		return nil
	}
	return fmt.Errorf("nenhum transporte remoto disponivel")
}

func (m *remoteDebugManager) enqueue(message, level string) {
	m.mu.Lock()
	session := m.activeSession
	m.mu.Unlock()
	if session == nil {
		return
	}
	m.enqueueToSession(session, message, level)
}

func (m *remoteDebugManager) enqueueWithSession(sessionID, message, level string) {
	m.mu.Lock()
	session := m.activeSession
	m.mu.Unlock()
	if session == nil || !strings.EqualFold(session.sessionID, strings.TrimSpace(sessionID)) {
		return
	}
	m.enqueueToSession(session, message, level)
}

func (m *remoteDebugManager) enqueueToSession(session *remoteDebugSession, message, level string) {
	if remoteDebugLevelValue(level) < session.minLevel {
		return
	}
	select {
	case session.logQueue <- queuedRemoteDebugLine{message: message, level: level}:
	default:
		m.logf("[remote-debug] fila cheia: log descartado")
	}
}

func (m *remoteDebugManager) stopSession(sessionID, reason string) bool {
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

func (m *remoteDebugManager) stopGivenSession(session *remoteDebugSession, reason string) {
	if session == nil {
		return
	}
	if session.unsubscribe != nil {
		session.unsubscribe()
	}
	if session.cancel != nil {
		session.cancel()
	}
	for _, pub := range session.publishers {
		_ = pub.Close()
	}
	m.logf(fmt.Sprintf("[remote-debug] sessao encerrada: sessionId=%s reason=%s", session.sessionID, strings.TrimSpace(reason)))
}

func parseRemoteDebugCommand(payload any) (remoteDebugCommand, error) {
	if payload == nil {
		return remoteDebugCommand{}, fmt.Errorf("payload ausente")
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return remoteDebugCommand{}, err
	}
	var cmd remoteDebugCommand
	if err := json.Unmarshal(b, &cmd); err != nil {
		return remoteDebugCommand{}, err
	}
	cmd.Action = strings.TrimSpace(cmd.Action)
	cmd.SessionID = strings.TrimSpace(cmd.SessionID)
	cmd.LogLevel = normalizeRemoteDebugLevel(cmd.LogLevel)
	return cmd, nil
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

func buildRemoteDebugPublishers(cfg DebugConfig, stream remoteDebugStreamConfig, token string) ([]remoteDebugPublisher, error) {
	var publishers []remoteDebugPublisher
	subject := strings.TrimSpace(stream.NatsSubject)
	if subject == "" {
		return nil, fmt.Errorf("subject NATS ausente no comando de remote debug")
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

func newNATSRemoteDebugPublisher(server, token, subject, name string) (remoteDebugPublisher, error) {
	server = strings.TrimSpace(server)
	token = strings.TrimSpace(token)
	subject = strings.TrimSpace(subject)
	if server == "" || token == "" || subject == "" {
		return nil, fmt.Errorf("config NATS incompleta")
	}

	nc, err := nats.Connect(server,
		nats.Name("discovery-remote-debug"),
		nats.Token(token),
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

func normalizeRemoteDebugLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug", "info", "warn", "error":
		return strings.ToLower(strings.TrimSpace(level))
	default:
		return "info"
	}
}

func remoteDebugLevelValue(level string) int {
	switch normalizeRemoteDebugLevel(level) {
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

func detectRemoteDebugLevel(line string) string {
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

func isRemoteDebugCommandType(cmdType string) bool {
	switch strings.ToLower(strings.TrimSpace(cmdType)) {
	case "8", "remotedebug", "remote-debug":
		return true
	default:
		return false
	}
}

func isAgentUpdateCommandType(cmdType string) bool {
	switch strings.ToLower(strings.TrimSpace(cmdType)) {
	case "10", "update", "agentupdate", "selfupdate", "self-update":
		return true
	default:
		return false
	}
}

func parseAgentUpdatePayload(payload any) (string, error) {
	if payload == nil {
		return "check-update", nil
	}
	switch typed := payload.(type) {
	case string:
		action := strings.ToLower(strings.TrimSpace(typed))
		if action == "" {
			return "check-update", nil
		}
		return action, nil
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return "", fmt.Errorf("falha ao serializar payload de update: %w", err)
		}
		var parsed struct {
			Action string `json:"action"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return "", fmt.Errorf("payload de update invalido: %w", err)
		}
		if strings.TrimSpace(parsed.Action) == "" {
			return "check-update", nil
		}
		return strings.ToLower(strings.TrimSpace(parsed.Action)), nil
	}
}

func (a *App) requestAgentUpdateCheck(ctx context.Context, source string) error {
	if a == nil {
		return fmt.Errorf("app indisponivel")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	source = strings.TrimSpace(source)
	if source == "" {
		source = "manual"
	}
	if a.serviceConnectedMode.Load() && a.serviceClient != nil {
		if !a.serviceClient.IsConnected() {
			if err := a.serviceClient.Connect(ctx); err != nil {
				return err
			}
		}
		if _, err := a.serviceClient.TriggerUpdateCheck(ctx, source); err != nil {
			return err
		}
		a.logs.append("[update] force-check enviado ao service: source=" + source)
		return nil
	}
	return fmt.Errorf("self-update requer Windows Service conectado")
}

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
			return NotificationDispatchRequest{}, fmt.Errorf("falha ao serializar payload de notificacao: %w", err)
		}
		var req NotificationDispatchRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return NotificationDispatchRequest{}, fmt.Errorf("payload de notificacao invalido: %w", err)
		}
		return req, nil
	}
}

func (a *App) handleAgentRuntimeCommand(parent context.Context, cmdType string, payload any) (bool, int, string, string) {
	if isPsadtAlertCommandType(cmdType) {
		p, err := parsePsadtAlertPayload(payload)
		if err != nil {
			return true, 2, "", err.Error()
		}
		exitCode, output, errText := a.handlePsadtAlert(parent, p)
		return true, exitCode, output, errText
	}

	if isNotificationDispatchCommandType(cmdType) {
		req, err := parseNotificationDispatchPayload(payload)
		if err != nil {
			return true, 2, "", err.Error()
		}
		resp := a.DispatchNotification(req)
		body, _ := json.Marshal(resp)
		switch resp.Result {
		case "approved":
			return true, 0, string(body), ""
		case "denied":
			return true, 10, string(body), "usuario negou a notificacao"
		case "timeout_policy_applied":
			return true, 124, string(body), "timeout de confirmacao"
		default:
			if resp.Accepted {
				return true, 0, string(body), ""
			}
			return true, 1, string(body), strings.TrimSpace(resp.Message)
		}
	}

	if isAgentUpdateCommandType(cmdType) {
		action, err := parseAgentUpdatePayload(payload)
		if err != nil {
			return true, 2, "", err.Error()
		}
		switch action {
		case "", "check-update", "force-check":
			if err := a.requestAgentUpdateCheck(parent, "command:"+action); err != nil {
				return true, 1, "", err.Error()
			}
			return true, 0, "self-update check solicitado", ""
		default:
			return true, 2, "", "acao de update nao suportada"
		}
	}

	if a == nil || a.remoteDebug == nil {
		return false, 0, "", ""
	}
	return a.remoteDebug.HandleCommand(parent, cmdType, payload)
}

func (a *App) onAgentCommandOutput(cmdType, output, errText string) {
	if a == nil || a.remoteDebug == nil {
		return
	}
	a.remoteDebug.OnCommandOutput(cmdType, output, errText)
}
