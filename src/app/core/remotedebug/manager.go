package remotedebug

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"discovery/app/core/sessionlive"
)

// queuedLine é uma linha de log enfileirada para publicação.
type queuedLine struct {
	message string
	level   string
}

// Session representa uma sessão de remote debug ativa.
type Session struct {
	sessionID      string
	agentID        string
	controlSubject string
	minLevel       int
	// levelMu protege minLevel: o setLevel chega pelo canal de controle
	// (goroutine do NATS) enquanto o enqueue de logs lê o nível.
	levelMu   sync.RWMutex
	startedAt time.Time
	deadline  time.Time
	// maxDeadline é o teto absoluto (startedAt + duração configurada na
	// instalação). A renovação nunca ultrapassa este instante.
	maxDeadline time.Time
	// ttl é a janela de renovação deslizante.
	ttl time.Duration
	// live rastreia a presença do viewer (ping/pong) e calcula o deadline.
	live *sessionlive.Peer
	// runner roda o loop de liveness (ping + avaliação de ausência).
	runner *sessionlive.Runner
	// controlOffs guarda os disposers das assinaturas do canal de controle.
	controlOffs []func()
	// controlSeq numerar os frames do canal de controle (idempotencia/dedupe).
	controlSeqMu sync.Mutex
	controlSeq   uint64
	// transportMu protege activeIndex: publishLoop, o handler do controle e o
	// runner podem publicar ao mesmo tempo.
	transportMu sync.Mutex
	logQueue    chan queuedLine
	cancel      context.CancelFunc
	unsubscribe func()
	publishers  []Publisher
	activeIndex int
	// publishFailureLogged evita spam: sem isso, cada linha de log com todos os
	// transportes mortos gravaria "falha ao publicar log remoto" no
	// agent-service.log (que já passa de 8 MB), escondendo o problema real.
	// Acessado apenas pelo goroutine do publishLoop — não precisa de lock.
	publishFailureLogged bool
}

// SessionMinLevel lê o nível mínimo atual da sessão com proteção de lock.
func (s *Session) SessionMinLevel() int {
	if s == nil {
		return LevelValue("info")
	}
	s.levelMu.RLock()
	defer s.levelMu.RUnlock()
	return s.minLevel
}

// SetMinLevel aplica um novo nível mínimo (usado pelo setLevel do canal de
// controle, sem reiniciar a sessão).
func (s *Session) SetMinLevel(level int) {
	if s == nil {
		return
	}
	s.levelMu.Lock()
	s.minLevel = level
	s.levelMu.Unlock()
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

	// ctx é o contexto de ciclo de vida, cancelado no shutdown.
	ctx    context.Context
	cancel context.CancelFunc
	// started indica se Startup foi chamado com sucesso.
	started bool
	// shutdownRequested marca que Shutdown foi chamado explicitamente: a partir
	// daí novas sessões são recusadas (o processo está encerrando). Diferente de
	// started=false, que também significa "Startup nunca foi chamado" — nesse
	// caso o manager se auto-inicializa (ver ensureStarted).
	shutdownRequested bool
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
		m.logf("[remote-debug] FALHA ao iniciar sessao: sessionId ausente no comando")
		return fmt.Errorf("sessionId ausente")
	}

	// Garante o contexto de ciclo de vida antes de qualquer trabalho.
	//
	// O Startup é responsabilidade do processo dono (adapter Wails v3 na UI,
	// runCoreStartup no modo serviço). Se esse wiring falhar, o manager se
	// auto-inicializa e registra um AVISO em vez de recusar o comando sem
	// deixar rastro — foi exatamente esse o bug do agente no modo serviço
	// (comando "remotedebug" recebido, exitCode=1, ZERO linha [remote-debug]
	// no agent-service.log e console eternamente em "Aguardando entradas").
	if err := m.ensureStarted(); err != nil {
		m.logf("[remote-debug] FALHA ao iniciar sessao: " + err.Error())
		return err
	}

	m.mu.Lock()
	lifecycleCtx := m.ctx
	m.mu.Unlock()

	cfg := m.getConfig()
	token := strings.TrimSpace(cfg.AuthToken)
	agentID := strings.TrimSpace(cfg.AgentID)
	if token == "" || agentID == "" {
		m.logf(fmt.Sprintf("[remote-debug] FALHA ao iniciar sessao: credenciais incompletas (authTokenVazio=%v agentIdVazio=%v)", token == "", agentID == ""))
		return fmt.Errorf("authToken/agentId ausentes para remote debug (token=vazio=%v agentId=vazio=%v)", token == "", agentID == "")
	}

	agentCfg := m.getAgentCfg()
	clientID := strings.TrimSpace(agentCfg.ClientID)
	siteID := strings.TrimSpace(agentCfg.SiteID)

	m.logf(fmt.Sprintf("[remote-debug] iniciando sessao: sessionId=%s agentId=%s clientId=%s siteId=%s subjectRaw=%q", sessionID, agentID, clientID, siteID, strings.TrimSpace(cmd.Stream.NatsSubject)))

	now := time.Now().UTC()
	deadline := ComputeDeadline(strings.TrimSpace(cmd.ExpiresAtUTC), now)
	maxDeadline := ComputeMaxDeadline(strings.TrimSpace(cmd.MaxExpiresAtUTC), now, deadline)
	renewalTTL := deadline.Sub(now)
	if renewalTTL <= 0 {
		renewalTTL = DefaultSessionCap
	}

	controlSubject, err := ResolveControlSubjectPrefixed(cmd.Stream, clientID, siteID, agentID)
	if err != nil {
		m.logf(fmt.Sprintf("[remote-debug] FALHA ao iniciar sessao: %v", err))
		return err
	}

	publishers, err := BuildPublishers(cfg, cmd.Stream, token, clientID, siteID)
	if err != nil {
		m.logf(fmt.Sprintf("[remote-debug] FALHA ao criar publishers: %v", err))
		return err
	}

	lifecycleBase := lifecycleCtx
	if lifecycleBase == nil {
		lifecycleBase = context.Background()
	}
	ctx, cancel := context.WithCancel(lifecycleBase)

	liveness := cmd.Liveness
	livePeer := sessionlive.NewPeer(sessionlive.Config{
		Interval:      time.Duration(liveness.PingIntervalSeconds) * time.Second,
		MissesAllowed: liveness.MissedPingsBeforeClose,
		InitialGrace:  time.Duration(liveness.InitialGraceSeconds) * time.Second,
		MaxDeadline:   maxDeadline,
		// Debug remoto: sem nenhum sinal do viewer dentro da grace, a sessao
		// nao pode ficar presa esperando uma popup que nunca conectou.
		CloseWithoutPeerSignal: true,
	}, now)

	session := &Session{
		sessionID:      sessionID,
		agentID:        agentID,
		controlSubject: controlSubject,
		minLevel:       LevelValue(cmd.LogLevel),
		startedAt:      now,
		deadline:       deadline,
		maxDeadline:    maxDeadline,
		ttl:            renewalTTL,
		live:           livePeer,
		logQueue:       make(chan queuedLine, QueueSize),
		cancel:         cancel,
		publishers:     publishers,
	}

	// Canal de controle: assina o MESMO subject em cada transporte. Fail-fast:
	// sem assinatura a sessao morreria em InitialGrace — melhor recusar o start
	// com erro claro (ACL do servidor desatualizada) do que abrir uma sessao
	// que se autodestroi. Nao ha retrocompatibilidade com servidores antigos.
	for _, pub := range publishers {
		off, subErr := pub.Subscribe(controlSubject, func(data []byte) {
			m.handleControlFrame(session, data)
		})
		if subErr != nil {
			m.logf(fmt.Sprintf("[remote-debug] assinatura do canal de controle falhou em %s: %v", pub.Name(), subErr))
			continue
		}
		session.controlOffs = append(session.controlOffs, off)
	}
	if len(session.controlOffs) == 0 {
		for _, pub := range publishers {
			_ = pub.Close()
		}
		cancel()
		errText := fmt.Sprintf("ping subscribe indisponivel em %s — ACL do servidor desatualizada", controlSubject)
		m.logf("[remote-debug] FALHA ao iniciar sessao: " + errText)
		return fmt.Errorf("%s", errText)
	}

	unsubscribe := m.replayLogs(func(line string) {
		m.enqueueWithSession(sessionID, line, DetectLevel(line))
	})
	session.unsubscribe = unsubscribe

	session.runner = sessionlive.NewRunner(livePeer, sessionlive.RunnerOptions{
		OnTick: func(context.Context) {
			m.sendControl(session, ControlTypePing, nil)
			m.extendDeadline(session)
		},
		OnPeerLost: func(reason string) {
			m.logf(fmt.Sprintf("[remote-debug] viewer ausente (%s): encerrando sessao %s", reason, sessionID))
			m.stopSession(sessionID, "viewer-timeout")
		},
		Logf: m.logf,
	})

	m.mu.Lock()
	previous := m.activeSession
	m.activeSession = session
	m.mu.Unlock()

	if previous != nil {
		m.stopGivenSession(previous, "replaced")
	}

	m.logf(fmt.Sprintf("[remote-debug] sessao iniciada: sessionId=%s deadline=%s maxDeadline=%s control=%s transport=%s", sessionID, deadline.Format(time.RFC3339), maxDeadline.Format(time.RFC3339), controlSubject, session.publishers[0].Name()))
	go m.publishLoop(ctx, session)
	go session.runner.Run(ctx)
	return nil
}

// extendDeadline estende o deadline da sessão enquanto o viewer estiver vivo,
// sem ultrapassar maxDeadline. Ao atingir o teto, encerra com max-duration.
func (m *Manager) extendDeadline(session *Session) {
	if session == nil || session.live == nil {
		return
	}
	now := time.Now().UTC()
	if session.live.ExceededMaxDeadline(now) {
		m.logf(fmt.Sprintf("[remote-debug] teto de sessao atingido (max-duration): sessionId=%s", session.sessionID))
		m.stopSession(session.sessionID, "max-duration")
		return
	}
	next := session.live.Deadline(now, session.ttl)
	if next.IsZero() {
		return
	}
	m.mu.Lock()
	if m.activeSession == session {
		session.deadline = next
	}
	m.mu.Unlock()
}

func (m *Manager) nextControlSeq(session *Session) uint64 {
	session.controlSeqMu.Lock()
	defer session.controlSeqMu.Unlock()
	session.controlSeq++
	return session.controlSeq
}

// handleControlFrame processa um frame recebido no canal de controle.
// Validações: sessionId da sessão ativa, from diferente do próprio agente
// (eco), tipo na allow-list e tamanho — tudo isso já é checado no DecodeControl.
func (m *Manager) handleControlFrame(session *Session, data []byte) {
	if session == nil {
		return
	}
	env, err := DecodeControl(data)
	if err != nil {
		m.logf(fmt.Sprintf("[remote-debug] frame de controle descartado: %v", err))
		return
	}
	if !strings.EqualFold(strings.TrimSpace(env.SessionID), session.sessionID) {
		return
	}
	if env.From == RoleAgent {
		return // eco do próprio agente
	}
	// Presença do VIEWER: apenas ping/pong do viewer contam. Um setLevel vindo
	// do servidor NÃO prova que o navegador está vivo — se contasse, a sessão
	// sobreviveria com a popup fechada.
	if env.From == RoleViewer && (env.Type == ControlTypePing || env.Type == ControlTypePong) {
		session.live.NotePeerSignal(time.Now().UTC())
	}
	switch env.Type {
	case ControlTypePing:
		m.sendControl(session, ControlTypePong, nil)
	case ControlTypeSetLevel:
		level := NormalizeLevel(payloadString(env.Payload, "logLevel"))
		session.SetMinLevel(LevelValue(level))
		m.sendControl(session, ControlTypeLevelChanged, map[string]any{"logLevel": level})
		m.logf(fmt.Sprintf("[remote-debug] nivel de log alterado em tempo real: sessionId=%s level=%s", session.sessionID, level))
	case ControlTypePong, ControlTypeLevelChanged, ControlTypeClosed:
		// presença já registrada acima
	}
}

// sendControl publica um frame de controle no transporte ativo.
func (m *Manager) sendControl(session *Session, typ string, payload map[string]any) {
	if session == nil || session.controlSubject == "" {
		return
	}
	seq := m.nextControlSeq(session)
	env := NewControlEnvelope(RoleAgent, typ, session.sessionID, seq, payload)
	raw, err := EncodeControl(env)
	if err != nil {
		m.logf(fmt.Sprintf("[remote-debug] falha ao codificar controle: %v", err))
		return
	}
	if err := m.publishControlRaw(session, session.controlSubject, raw); err != nil && typ != ControlTypeClosed {
		m.logf(fmt.Sprintf("[remote-debug] falha ao publicar controle %s: %v", typ, err))
	}
}

func (m *Manager) publishControlRaw(session *Session, subject string, payload []byte) error {
	session.transportMu.Lock()
	defer session.transportMu.Unlock()
	for idx := session.activeIndex; idx < len(session.publishers); idx++ {
		pub := session.publishers[idx]
		if err := pub.PublishRaw(context.Background(), subject, payload); err != nil {
			m.logf(fmt.Sprintf("[remote-debug] publish de controle falhou em %s: %v", pub.Name(), err))
			_ = pub.Close()
			session.activeIndex = idx + 1
			continue
		}
		return nil
	}
	return fmt.Errorf("nenhum transporte remoto disponivel")
}

func payloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	if v, ok := payload[key].(string); ok {
		return v
	}
	return ""
}

func (m *Manager) publishLoop(ctx context.Context, session *Session) {
	var seq uint64
	for {
		select {
		case <-ctx.Done():
			return
		case item := <-session.logQueue:
			// select escolhe aleatoriamente entre cases prontos: depois do
			// cancel ainda poderíamos consumir um item e publicar com os
			// publishers já fechados por stopGivenSession, gerando um
			// "PUBLICACAO FALHOU" falso no encerramento da sessão.
			if ctx.Err() != nil {
				return
			}
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
				// Loga UMA vez por sessão: repetir a cada linha esconde o
				// problema e enche o log. Esta linha é o que o operador deve
				// procurar quando o console fica vazio.
				if !session.publishFailureLogged {
					session.publishFailureLogged = true
					m.logf("[remote-debug] PUBLICACAO FALHOU — os logs NAO estao chegando ao servidor (console ficara em \"Aguardando entradas\"): " + err.Error())
				}
				continue
			}
			if session.publishFailureLogged {
				session.publishFailureLogged = false
				m.logf("[remote-debug] publicacao restabelecida")
			}
		}
	}
}

func (m *Manager) publishWithFallback(ctx context.Context, session *Session, msg LogMessage) error {
	session.transportMu.Lock()
	defer session.transportMu.Unlock()
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
	if LevelValue(level) < session.SessionMinLevel() {
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
	if session.runner != nil {
		session.runner.Stop()
	}
	if session.unsubscribe != nil {
		session.unsubscribe()
	}
	if session.cancel != nil {
		session.cancel()
	}
	// Avisa o viewer (best-effort) ANTES de fechar publisher/assinaturas.
	m.sendControl(session, ControlTypeClosed, map[string]any{"reason": strings.TrimSpace(reason)})
	for _, off := range session.controlOffs {
		if off != nil {
			off()
		}
	}
	for _, pub := range session.publishers {
		_ = pub.Close()
	}
	m.logf(fmt.Sprintf("[remote-debug] sessao encerrada: sessionId=%s reason=%s", session.sessionID, strings.TrimSpace(reason)))
}

// ServiceName retorna o nome do service para logging.
func (m *Manager) ServiceName() string {
	return "remotedebug.Manager"
}

// ensureStarted garante que o contexto de ciclo de vida está ativo.
//
// Três estados possíveis:
//   - started=true            → nada a fazer;
//   - shutdownRequested=true  → processo encerrando; recusa (evita sessão órfã
//     com contexto já cancelado);
//   - nenhum dos dois         → Startup nunca foi chamado (wiring incompleto):
//     auto-inicializa com context.Background() e AVISA no log, em vez de
//     falhar silenciosamente.
func (m *Manager) ensureStarted() error {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return nil
	}
	if m.shutdownRequested {
		m.mu.Unlock()
		return fmt.Errorf("remote debug nao inicializado (shutdown em andamento)")
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.ctx = ctx
	m.cancel = cancel
	m.started = true
	m.mu.Unlock()

	m.logf("[remote-debug] AVISO: lifecycle nao foi iniciado pelo processo (Startup ausente) — auto-inicializando; verifique o wiring de lifecycle no modo servico")
	return nil
}

// Startup prepara o contexto de ciclo de vida do domínio remote debug.
// É chamado pelo App (ou pelo adapter Wails v3) durante o startup.
// Idempotente: a primeira chamada vence.
func (m *Manager) Startup(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started || m.shutdownRequested {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	m.ctx = ctx
	m.cancel = cancel
	m.started = true
	return nil
}

// Shutdown encerra a sessão ativa (se houver), cancela o contexto de ciclo de
// vida e libera publishers/goroutines. Idempotente.
func (m *Manager) Shutdown() error {
	if m == nil {
		return nil
	}

	m.mu.Lock()
	// shutdownRequested é marcado mesmo quando o manager nunca foi iniciado:
	// assim um Start posterior não "ressuscita" o domínio num processo que já
	// está encerrando.
	m.shutdownRequested = true
	if !m.started {
		m.mu.Unlock()
		return nil
	}
	cancel := m.cancel
	m.cancel = nil
	m.ctx = nil
	m.started = false
	session := m.activeSession
	m.activeSession = nil
	m.mu.Unlock()

	// Cancela o contexto de ciclo de vida e encerra a sessão ativa (se houver)
	// fora do lock para evitar bloquear concurrentes no startSession.
	if cancel != nil {
		cancel()
	}
	if session != nil {
		m.stopGivenSession(session, "shutdown")
	}
	return nil
}

// Ctx retorna o contexto de ciclo de vida do service.
// Retorna context.Background() se Startup ainda não foi chamado.
func (m *Manager) Ctx() context.Context {
	if m == nil {
		return context.Background()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx == nil {
		return context.Background()
	}
	return m.ctx
}
