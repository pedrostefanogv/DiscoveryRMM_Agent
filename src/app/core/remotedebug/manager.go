package remotedebug

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// queuedLine é uma linha de log enfileirada para publicação.
type queuedLine struct {
	message string
	level   string
}

// Session representa uma sessão de remote debug ativa.
type Session struct {
	sessionID   string
	agentID     string
	minLevel    int
	deadline    time.Time
	logQueue    chan queuedLine
	cancel      context.CancelFunc
	unsubscribe func()
	publishers  []Publisher
	activeIndex int
}

// Deps são as dependências injetadas no Manager.
type Deps struct {
	// Logf appends a log line.
	Logf func(string)
	// GetConfig retorna a configuração de conexão (token, agentId, NATS).
	GetConfig func() Config
	// GetAgentConfig retorna clientId/siteId do agente.
	GetAgentConfig func() AgentConfig
	// SubscribeLogs assina novas linhas de log.
	SubscribeLogs func(func(string)) func()
	// ReplayLogs assina novas linhas e reproduz o histórico.
	ReplayLogs func(func(string)) func()
}

// AgentConfig é a visão mínima da configuração do agente usada pelo remote debug.
type AgentConfig struct {
	ClientID string
	SiteID   string
}

// Manager gerencia o lifecycle de sessões de remote debug.
type Manager struct {
	mu            sync.Mutex
	activeSession *Session
	logf          func(string)
	getConfig     func() Config
	getAgentCfg   func() AgentConfig
	subscribeLogs func(func(string)) func()
	replayLogs    func(func(string)) func()
}

// New cria um Manager com as dependências injetadas.
func New(deps Deps) *Manager {
	logf := deps.Logf
	if logf == nil {
		logf = func(string) {}
	}
	getConfig := deps.GetConfig
	if getConfig == nil {
		getConfig = func() Config { return Config{} }
	}
	getAgentCfg := deps.GetAgentConfig
	if getAgentCfg == nil {
		getAgentCfg = func() AgentConfig { return AgentConfig{} }
	}
	subscribeLogs := deps.SubscribeLogs
	if subscribeLogs == nil {
		subscribeLogs = func(func(string)) func() { return func() {} }
	}
	replayLogs := deps.ReplayLogs
	if replayLogs == nil {
		replayLogs = subscribeLogs
	}
	return &Manager{
		logf:          logf,
		getConfig:     getConfig,
		getAgentCfg:   getAgentCfg,
		subscribeLogs: subscribeLogs,
		replayLogs:    replayLogs,
	}
}

// HandleCommand processa um comando de remote debug.
// Retorna (handled, exitCode, output, errText) no mesmo contrato do router de comandos.
func (m *Manager) HandleCommand(_ context.Context, cmdType string, payload any) (bool, int, string, string) {
	if !IsCommandType(cmdType) {
		return false, 0, "", ""
	}

	cmd, err := ParseCommand(payload)
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

// OnCommandOutput enfileira a saída de um comando para a sessão ativa.
func (m *Manager) OnCommandOutput(cmdType, output, errText string) {
	if IsCommandType(cmdType) {
		return
	}
	for _, line := range SplitLines(output) {
		m.enqueue(line, "info")
	}
	for _, line := range SplitLines(errText) {
		m.enqueue(line, "error")
	}
}

func (m *Manager) startSession(cmd Command) error {
	sessionID := strings.TrimSpace(cmd.SessionID)
	if sessionID == "" {
		return fmt.Errorf("sessionId ausente")
	}

	cfg := m.getConfig()
	token := strings.TrimSpace(cfg.AuthToken)
	agentID := strings.TrimSpace(cfg.AgentID)
	if token == "" || agentID == "" {
		return fmt.Errorf("authToken/agentId ausentes para remote debug (token=vazio=%v agentId=vazio=%v)", token == "", agentID == "")
	}

	agentCfg := m.getAgentCfg()
	clientID := strings.TrimSpace(agentCfg.ClientID)
	siteID := strings.TrimSpace(agentCfg.SiteID)

	m.logf(fmt.Sprintf("[remote-debug] iniciando sessao: sessionId=%s agentId=%s clientId=%s siteId=%s subjectRaw=%q", sessionID, agentID, clientID, siteID, strings.TrimSpace(cmd.Stream.NatsSubject)))

	deadline := ComputeDeadline(strings.TrimSpace(cmd.ExpiresAtUTC), time.Now().UTC())
	publishers, err := BuildPublishers(cfg, cmd.Stream, token, clientID, siteID)
	if err != nil {
		m.logf(fmt.Sprintf("[remote-debug] FALHA ao criar publishers: %v", err))
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	session := &Session{
		sessionID:  sessionID,
		agentID:    agentID,
		minLevel:   LevelValue(cmd.LogLevel),
		deadline:   deadline,
		logQueue:   make(chan queuedLine, QueueSize),
		cancel:     cancel,
		publishers: publishers,
	}

	unsubscribe := m.replayLogs(func(line string) {
		m.enqueueWithSession(sessionID, line, DetectLevel(line))
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

func (m *Manager) autoStopAtDeadline(sessionID string, deadline time.Time) {
	wait := time.Until(deadline)
	if wait < 0 {
		wait = 0
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	<-timer.C
	// Só para a sessão se ela ainda for a ativa com o MESMO sessionID E o MESMO
	// deadline. Se o servidor reabriu a sessão com o mesmo ID mas um deadline
	// mais longo, a sessão antiga já foi parada por stopGivenSession("replaced")
	// no startSession, e uma nova sessão com novo deadline foi criada — o timer
	// antigo não deve parar a nova prematuramente.
	m.mu.Lock()
	current := m.activeSession
	matches := current != nil &&
		strings.EqualFold(current.sessionID, strings.TrimSpace(sessionID)) &&
		current.deadline.Equal(deadline)
	m.mu.Unlock()
	if matches {
		m.stopSession(sessionID, "timeout")
	}
}

func (m *Manager) publishLoop(ctx context.Context, session *Session) {
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
			msg := LogMessage{
				SessionID:    session.sessionID,
				AgentID:      session.agentID,
				Message:      TruncateMessage(item.message),
				Level:        NormalizeStreamLevel(item.level),
				TimestampUTC: time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
				Sequence:     seq,
			}
			if err := m.publishWithFallback(ctx, session, msg); err != nil {
				m.logf("[remote-debug] falha ao publicar log remoto: " + err.Error())
			}
		}
	}
}

func (m *Manager) publishWithFallback(ctx context.Context, session *Session, msg LogMessage) error {
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

func (m *Manager) enqueue(message, level string) {
	m.mu.Lock()
	session := m.activeSession
	m.mu.Unlock()
	if session == nil {
		return
	}
	m.enqueueToSession(session, message, level)
}

func (m *Manager) enqueueWithSession(sessionID, message, level string) {
	m.mu.Lock()
	session := m.activeSession
	m.mu.Unlock()
	if session == nil || !strings.EqualFold(session.sessionID, strings.TrimSpace(sessionID)) {
		return
	}
	m.enqueueToSession(session, message, level)
}

func (m *Manager) enqueueToSession(session *Session, message, level string) {
	if LevelValue(level) < session.minLevel {
		return
	}
	select {
	case session.logQueue <- queuedLine{message: message, level: level}:
	default:
		m.logf("[remote-debug] fila cheia: log descartado")
	}
}

func (m *Manager) stopSession(sessionID, reason string) bool {
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

func (m *Manager) stopGivenSession(session *Session, reason string) {
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

// Shutdown encerra a sessão ativa (se houver) e libera publishers/goroutines.
// Chamado pelo App durante o shutdown da aplicação.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	session := m.activeSession
	m.activeSession = nil
	m.mu.Unlock()
	if session != nil {
		m.stopGivenSession(session, "shutdown")
	}
}
