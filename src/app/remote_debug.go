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

type remoteDebugCommandPayload struct {
	Action       *string                   `json:"action"`
	SessionID    *string                   `json:"sessionId"`
	LogLevel     *string                   `json:"logLevel"`
	StartedAtUTC *string                   `json:"startedAtUtc"`
	ExpiresAtUTC *string                   `json:"expiresAtUtc"`
	StoppedAtUTC *string                   `json:"stoppedAtUtc"`
	Stream       *remoteDebugStreamPayload `json:"stream"`
}

type remoteDebugStreamConfig struct {
	NatsSubject string `json:"natsSubject"`
	NatsWssURL  string `json:"natsWssUrl"`
}

type remoteDebugStreamPayload struct {
	NatsSubject *string `json:"natsSubject"`
	NatsWssURL  *string `json:"natsWssUrl"`
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
	getAgentCfg   func() AgentConfiguration
	subscribeLogs func(func(string)) func()
	replayLogs    func(func(string)) func()
}

func newRemoteDebugManager(logf func(string), getDebugCfg func() DebugConfig, getAgentCfg func() AgentConfiguration, subscribeLogs func(func(string)) func(), replayLogs func(func(string)) func()) *remoteDebugManager {
	if logf == nil {
		logf = func(string) {}
	}
	if getDebugCfg == nil {
		getDebugCfg = func() DebugConfig { return DebugConfig{} }
	}
	if getAgentCfg == nil {
		getAgentCfg = func() AgentConfiguration { return AgentConfiguration{} }
	}
	if subscribeLogs == nil {
		subscribeLogs = func(func(string)) func() { return func() {} }
	}
	if replayLogs == nil {
		replayLogs = subscribeLogs
	}
	return &remoteDebugManager{
		logf:          logf,
		getDebugCfg:   getDebugCfg,
		getAgentCfg:   getAgentCfg,
		subscribeLogs: subscribeLogs,
		replayLogs:    replayLogs,
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
		return fmt.Errorf("authToken/agentId ausentes para remote debug (token=vazio=%v agentId=vazio=%v)", token == "", agentID == "")
	}

	agentCfg := m.getAgentCfg()
	clientID := strings.TrimSpace(agentCfg.ClientID)
	siteID := strings.TrimSpace(agentCfg.SiteID)

	m.logf(fmt.Sprintf("[remote-debug] iniciando sessao: sessionId=%s agentId=%s clientId=%s siteId=%s subjectRaw=%q", sessionID, agentID, clientID, siteID, strings.TrimSpace(cmd.Stream.NatsSubject)))

	deadline := computeRemoteDebugDeadline(strings.TrimSpace(cmd.ExpiresAtUTC), time.Now().UTC())
	publishers, err := buildRemoteDebugPublishers(cfg, cmd.Stream, token, clientID, siteID)
	if err != nil {
		m.logf(fmt.Sprintf("[remote-debug] FALHA ao criar publishers: %v", err))
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

	unsubscribe := m.replayLogs(func(line string) {
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
	// Só para a sessão se ela ainda for a ativa com o mesmo sessionID.
	// Se o servidor reabriu a sessão com mesmo ID, a sessão antiga já foi
	// parada por stopGivenSession("replaced") no startSession, e uma nova
	// sessão com novo deadline foi criada — não devemos parar a nova.
	m.mu.Lock()
	current := m.activeSession
	matches := current != nil && strings.EqualFold(current.sessionID, strings.TrimSpace(sessionID))
	m.mu.Unlock()
	if matches {
		m.stopSession(sessionID, "timeout")
	}
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
				Level:        normalizeRemoteDebugStreamLevel(item.level),
				TimestampUTC: time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
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
