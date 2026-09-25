//go:build windows

package remotesession

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"discovery/app/core/safego"
	"discovery/app/core/screen"
	"discovery/app/core/sessioncontrol"
	"discovery/app/core/sessionlive"
	"discovery/app/core/terminal"
)

// Session representa uma sessao remota ativa gerenciada pelo agent.
type Session struct {
	ID           string    `json:"sessionId"`
	Kind         string    `json:"kind"`         // screen, terminal, files, proxy
	Transport    string    `json:"transport"`    // webrtc, nats, http
	Quality      string    `json:"quality"`      // ultra, high, medium, low, ultralow
	Codec        string    `json:"codec"`        // jpeg, webp, h264
	ImageQuality int       `json:"imageQuality"` // compressão JPEG/WebP 1-100, default 70
	MaxFps       int       `json:"maxFps"`       // taxa máxima de quadros por segundo, default 15
	NatsSubject  string    `json:"natsSubject"`  // subject base para stream
	ExpiresAt    time.Time `json:"expiresAtUtc"`
	StartedAt    time.Time `json:"startedAtUtc"`
	Recording    bool      `json:"recording"`

	// estado interno
	stopCh chan struct{}
	doneCh chan struct{}
	Meta   map[string]any `json:"-"` // metadados do payload original (shell, termCols, termRows, etc.)

	// liveness (ping/pong no canal .control)
	live            *sessionlive.Peer   // presenca do viewer e deadline deslizante
	runner          *sessionlive.Runner // loop de ping/avaliacao de ausencia
	ttl             time.Duration       // janela de renovacao concedida a cada sinal do viewer
	initialDeadline time.Time           // prazo original do start (fallback sem primeiro ping)
	maxDeadline     time.Time           // teto absoluto (startedAt + duracao maxima)
	controlSub      *nats.Subscription  // assinatura do canal .control (por sessao)
	controlSeq      uint64
	controlSeqMu    *sync.Mutex // ponteiro: Session e copiada em GetActiveSessions
}

// Manager gerencia o lifecycle de sessoes remotas no agent.
type Manager struct {
	mu             sync.RWMutex
	sessions       map[string]*Session       // key: sessionId
	screenSessions map[string]*SessionScreen // key: sessionId — referência às goroutines ativas
	nc             *nats.Conn
	natsStream     *NatsStreamHandler

	// callbacks para notificar a UI/tray
	onSessionStarted func(sessionID, kind string)
	onSessionEnded   func(sessionID, reason string)

	// started indica se Startup foi chamado com sucesso.
	started bool
}

// NewManager cria um novo gerenciador de sessoes remotas.
func NewManager(nc *nats.Conn) *Manager {
	return &Manager{
		sessions:       make(map[string]*Session),
		screenSessions: make(map[string]*SessionScreen),
		nc:             nc,
	}
}

// SetNatsConn configura a conexão NATS e inicializa o NatsStreamHandler.
// Deve ser chamado após a conexão NATS ser estabelecida.
func (m *Manager) SetNatsConn(nc *nats.Conn, tenantID, siteID, agentID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nc = nc
	m.natsStream = NewNatsStreamHandler(nc, tenantID, siteID, agentID)
}

// SetCallbacks configura callbacks para notificar a UI/tray sobre mudancas de sessao.
func (m *Manager) SetCallbacks(onStarted func(sessionID, kind string), onEnded func(sessionID, reason string)) {
	m.onSessionStarted = onStarted
	m.onSessionEnded = onEnded
}

// HandleCommand processa comandos de sessao remota recebidos via NATS.
// Retorna (ok, errorMessage).
func (m *Manager) HandleCommand(ctx context.Context, payload map[string]any) (bool, string) {
	action := toString(payload["action"])
	if action == "" {
		return false, "payload sem action"
	}

	switch action {
	case "start":
		return m.handleStart(ctx, payload)
	case "stop":
		return m.handleStop(ctx, payload)
	case "quality":
		return m.handleQuality(ctx, payload)
	case "recording_start":
		return m.handleRecordingStart(ctx, payload)
	case "recording_stop":
		return m.handleRecordingStop(ctx, payload)
	default:
		return false, fmt.Sprintf("acao desconhecida: %s", action)
	}
}

func (m *Manager) handleStart(ctx context.Context, payload map[string]any) (bool, string) {
	sessionID := toString(payload["sessionId"])
	if sessionID == "" {
		return false, "payload sem sessionId"
	}

	kind := toString(payload["kind"])
	transport := normalizeTransport(toString(payload["transport"]))
	quality := toString(payload["quality"])
	codec := toString(payload["codec"])
	natsSubject := toString(payload["natsSubject"])
	imageQuality := toInt(payload["imageQuality"], 70)
	maxFps := toInt(payload["maxFps"], 15)

	log.Printf("[remote-session] handleStart: sessionId=%s kind=%s transport=%s quality=%s codec=%s imageQ=%d maxFps=%d natsSubject=%s\n",
		sessionID, kind, transport, quality, codec, imageQuality, maxFps, natsSubject)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Fecha sessao anterior do mesmo tipo se existir (uma por kind)
	for id, s := range m.sessions {
		if s.Kind == kind {
			log.Printf("[remote-session] handleStart: fechando sessao anterior %s (kind=%s)\n", id, kind)
			m.closeSessionLocked(id, "superseded")
			break
		}
	}

	now := time.Now().UTC()
	expiresAt, _ := time.Parse(time.RFC3339, toString(payload["expiresAtUtc"]))
	if expiresAt.IsZero() {
		expiresAt = now.Add(30 * time.Minute)
	}
	expiresAt = expiresAt.UTC()

	maxExpiresAt, _ := time.Parse(time.RFC3339, toString(payload["maxExpiresAtUtc"]))
	maxExpiresAt = maxExpiresAt.UTC()
	if maxExpiresAt.IsZero() {
		// Servidor antigo (sem contrato de liveness): o teto absoluto e o
		// proprio prazo do start — a sessao nao pode ser renovada alem dele.
		maxExpiresAt = expiresAt
	}
	renewalTTL := expiresAt.Sub(now)
	if renewalTTL <= 0 {
		renewalTTL = 30 * time.Minute
	}
	liveness := parseLivenessConfig(payload["liveness"])

	livePeer := sessionlive.NewPeer(sessionlive.Config{
		Interval:      time.Duration(liveness.PingIntervalSeconds) * time.Second,
		MissesAllowed: liveness.MissedPingsBeforeClose,
		InitialGrace:  time.Duration(liveness.InitialGraceSeconds) * time.Second,
		MaxDeadline:   maxExpiresAt,
		// Acesso remoto: viewers antigos nao enviam ping. Sem o primeiro sinal
		// NAO fechar por ausencia — a sessao cai no prazo original do start.
		CloseWithoutPeerSignal: false,
	}, now)

	session := &Session{
		ID:              sessionID,
		Kind:            kind,
		Transport:       transport,
		Quality:         quality,
		Codec:           codec,
		ImageQuality:    imageQuality,
		MaxFps:          maxFps,
		NatsSubject:     natsSubject,
		StartedAt:       now,
		ExpiresAt:       expiresAt,
		stopCh:          make(chan struct{}),
		doneCh:          make(chan struct{}),
		Meta:            payload, // payload original (shell, termCols, termRows, etc.)
		live:            livePeer,
		ttl:             renewalTTL,
		initialDeadline: expiresAt,
		maxDeadline:     maxExpiresAt,
		controlSeqMu:    &sync.Mutex{},
	}

	// Garante que o NatsStreamHandler esta configurado. Como o comando start
	// chega via NATS, o natsStream normalmente já está setado (onNatsConnected).
	// Se não estiver, NÃO criar sessão fantasma: os runners retornariam cedo e
	// o viewer ficaria preso sem stream. Retorna erro para o backend ANTES de
	// registrar a sessão no map (evita sessão órfã que nunca seria limpa).
	if m.natsStream == nil {
		log.Printf("[remote-session] handleStart: natsStream NAO configurado — rejeitando start sessionId=%s kind=%s\n",
			sessionID, kind)
		return false, "nats stream not ready"
	}

	// Canal .control assinado para TODO kind: carrega a liveness do viewer
	// (ping/pong) e comandos de dominio (ex. keyframe da tela). Fail-fast: sem
	// assinatura a sessao nao sabe se o viewer esta vivo; melhor recusar o
	// start do que abrir uma sessao que se autodestroi.
	controlSub, cerr := m.natsStream.SubscribeToControl(sessionID, func(data []byte) {
		m.handleControlFrame(session, data)
	})
	if cerr != nil {
		log.Printf("[remote-session] handleStart: ERRO ao subscrever control sessionId=%s: %v\n", sessionID, cerr)
		return false, "subscribe control: " + cerr.Error()
	}
	session.controlSub = controlSub

	m.sessions[sessionID] = session

	m.natsStream.PublishEvent(sessionID, "started", session)

	// Inicia o stream conforme o tipo (goroutine com safego)
	switch kind {
	case "screen", "all":
		safego.Go(func() {
			m.runScreenSession(ctx, session)
		}, func(line string) {
			fmt.Printf("[remote-session-screen] %s\n", line)
		})
	case "terminal":
		safego.Go(func() {
			m.runTerminalSession(ctx, session)
		}, func(line string) {
			fmt.Printf("[remote-session-term] %s\n", line)
		})
	case "files":
		safego.Go(func() {
			m.runFilesSession(ctx, session)
		}, func(line string) {
			fmt.Printf("[remote-session-files] %s\n", line)
		})
	case "processes":
		safego.Go(func() {
			m.runProcessesSession(ctx, session)
		}, func(line string) {
			fmt.Printf("[remote-session-processes] %s\n", line)
		})
	case "proxy":
		safego.Go(func() {
			m.runProxySession(ctx, session)
		}, func(line string) {
			fmt.Printf("[remote-session-proxy] %s\n", line)
		})
	}

	// Loop de liveness: envia ping ao viewer e avalia o deadline deslizante
	// (renovado a cada sinal do viewer) com teto absoluto em maxDeadline.
	session.runner = sessionlive.NewRunner(livePeer, sessionlive.RunnerOptions{
		OnTick: func(context.Context) {
			m.tickLiveness(session)
		},
		OnPeerLost: func(reason string) {
			log.Printf("[remote-session] viewer ausente (%s): encerrando sessao %s\n", reason, sessionID)
			m.closeSession(sessionID, "viewer-timeout")
		},
		Logf: func(line string) {
			log.Printf("[remote-session-live] %s\n", line)
		},
	})
	safego.Go(func() {
		session.runner.Run(ctx)
	}, func(line string) {
		fmt.Printf("[remote-session-live] %s\n", line)
	})

	if m.onSessionStarted != nil {
		m.onSessionStarted(sessionID, kind)
	}

	return true, ""
}

func (m *Manager) handleStop(_ context.Context, payload map[string]any) (bool, string) {
	sessionID := toString(payload["sessionId"])
	if sessionID == "" {
		return false, "payload sem sessionId"
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	return m.closeSessionLocked(sessionID, "stopped-by-server"), ""
}

func (m *Manager) handleQuality(_ context.Context, payload map[string]any) (bool, string) {
	sessionID := toString(payload["sessionId"])
	quality := toString(payload["quality"])
	codec := toString(payload["codec"])
	imageQuality := payload["imageQuality"] // pode ser nil ou float64
	maxFpsVal := payload["maxFps"]          // pode ser nil ou float64
	autoMode := false
	if a, ok := payload["auto"].(bool); ok {
		autoMode = a
	}
	if sessionID == "" || quality == "" {
		return false, "payload sem sessionId ou quality"
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return false, "sessao nao encontrada"
	}
	s.Quality = quality
	if codec != "" {
		s.Codec = codec
	}

	// Propaga mudanca para a sessao ativa (SessionScreen)
	if screen, ok := m.screenSessions[sessionID]; ok {
		screen.SetQuality(quality)
		if codec != "" {
			screen.SetCodec(codec)
		}
		// Fonte da verdade para o modo auto: o viewer envia `auto`. Manual
		// desabilita a adaptação; auto volta a adaptar. Independente dos
		// overrides de imagem/FPS (que podem ser aplicados no start).
		screen.SetManualMode(!autoMode)
		// Em modo Auto: limpa TODOS os overrides manuais e aplica a qualidade
		// do perfil SEM o clamp manual (perfis vão de 25 a 92) — a adaptação
		// automática continua ativa (adapt/downgrade).
		// Em Manual: aplica overrides explícitos com clamp 10-90.
		if autoMode {
			s.ImageQuality = 0
			// Volta ao AUTO (10-90 de 10 em 10): mantém a escada já aprendida
			// (rede/host calibrados) ou re-semeia pela POTÊNCIA DA MÁQUINA se
			// nunca houve adaptação. A imageQuality do payload é do PERFIL
			// (ex. unlimited=75) e NÃO deve semear a escada automática — era
			// uma das fontes do card travado em 75%.
			screen.ResetAutoToProfile()
			s.MaxFps = 0
			screen.ClearMaxFps()
			// FPS do perfil pode ser aplicado (não é a escada de imagem).
			if mf, ok := toFloat64(maxFpsVal); ok {
				screen.SetMaxFps(int(mf))
			}
		} else {
			// Só aplica override se o valor veio explicitamente no payload
			if iq, ok := toFloat64(imageQuality); ok {
				s.ImageQuality = int(iq)
				screen.SetImageQuality(int(iq))
			} else {
				s.ImageQuality = 0 // reset — volta ao perfil
				screen.ClearImageQuality()
			}
			if mf, ok := toFloat64(maxFpsVal); ok {
				s.MaxFps = int(mf)
				screen.SetMaxFps(int(mf))
			} else {
				s.MaxFps = 0 // reset — volta ao perfil
				screen.ClearMaxFps()
			}
		}
		log.Printf("[remote-session] qualidade alterada: sessionId=%s quality=%s codec=%s imageQ=%d maxFps=%d auto=%v\n",
			sessionID, quality, codec, s.ImageQuality, s.MaxFps, autoMode)
	} else {
		log.Printf("[remote-session] qualidade alterada no registro, mas sessao screen nao encontrada: sessionId=%s\n",
			sessionID)
	}

	m.publishEvent(sessionID, "quality_changed", map[string]string{
		"quality":      quality,
		"codec":        codec,
		"imageQuality": fmt.Sprintf("%d", s.ImageQuality),
		"maxFps":       fmt.Sprintf("%d", s.MaxFps),
	})

	return true, ""
}

// handleRecordingStart inicia o tap REAL de gravação da sessão de tela.
// M33: antes só setava o flag e emitia "recording_started" — nenhum frame era
// capturado (RecordingSource.Start() nunca era chamado).
func (m *Manager) handleRecordingStart(_ context.Context, payload map[string]any) (bool, string) {
	sessionID := toString(payload["sessionId"])
	if sessionID == "" {
		return false, "payload sem sessionId"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok {
		return false, "sessao nao encontrada"
	}
	s.Recording = true
	if ss, ok := m.screenSessions[sessionID]; ok && ss != nil && ss.recording != nil {
		ss.recording.Start()
	}
	m.publishEvent(sessionID, "recording_started", nil)
	return true, ""
}

func (m *Manager) handleRecordingStop(_ context.Context, payload map[string]any) (bool, string) {
	sessionID := toString(payload["sessionId"])
	if sessionID == "" {
		return false, "payload sem sessionId"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok {
		return false, "sessao nao encontrada"
	}
	s.Recording = false
	if ss, ok := m.screenSessions[sessionID]; ok && ss != nil && ss.recording != nil {
		ss.recording.Stop()
	}
	m.publishEvent(sessionID, "recording_stopped", nil)
	return true, ""
}

func (m *Manager) closeSessionLocked(sessionID, reason string) bool {
	s, ok := m.sessions[sessionID]
	if !ok {
		return false
	}
	delete(m.sessions, sessionID)
	delete(m.screenSessions, sessionID) // limpa referencia da SessionScreen

	select {
	case <-s.stopCh:
		// ja fechado
	default:
		close(s.stopCh)
	}

	// Para o loop de liveness antes de avisar o viewer (evita novo ping).
	if s.runner != nil {
		s.runner.Stop()
	}

	// Avisa o viewer pelo canal .control (motivo do encerramento), para que a
	// popup mostre o placeholder em vez de continuar tentando conectar.
	m.sendControl(s, ControlTypeClosed, map[string]any{"reason": strings.TrimSpace(reason)})

	if s.controlSub != nil {
		_ = s.controlSub.Unsubscribe()
	}

	if m.natsStream != nil {
		m.natsStream.PublishEvent(sessionID, "closed", map[string]string{"reason": reason})
	} else {
		m.publishEventLegacy(sessionID, "closed", nil)
	}

	if m.onSessionEnded != nil {
		m.onSessionEnded(sessionID, reason)
	}
	return true
}

func (m *Manager) publishEventLegacy(sessionID, eventType string, data any) {
	if m.nc == nil || !m.nc.IsConnected() {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"sessionId": sessionID,
		"eventType": eventType,
		"data":      data,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
	// Fallback: publica no subject legacy; sera substituido quando natsStream configurado
	_ = m.nc.Publish(fmt.Sprintf("agent.remote.session.%s.event", sessionID), payload)
}

func (m *Manager) publishEvent(sessionID, eventType string, data any) {
	if m.natsStream != nil {
		_ = m.natsStream.PublishEvent(sessionID, eventType, data)
		return
	}
	m.publishEventLegacy(sessionID, eventType, data)
}

// livenessConfig espelha o bloco `liveness` do payload de start enviado pelo
// backend (RemoteAccessOptions). Defaults iguais aos do debug remoto.
type livenessConfig struct {
	PingIntervalSeconds    int
	MissedPingsBeforeClose int
	InitialGraceSeconds    int
}

func parseLivenessConfig(raw any) livenessConfig {
	cfg := livenessConfig{PingIntervalSeconds: 5, MissedPingsBeforeClose: 3, InitialGraceSeconds: 60}
	m, ok := raw.(map[string]any)
	if !ok {
		return cfg
	}
	if v := toInt(m["pingIntervalSeconds"], 0); v > 0 {
		cfg.PingIntervalSeconds = v
	}
	if v := toInt(m["missedPingsBeforeClose"], 0); v > 0 {
		cfg.MissedPingsBeforeClose = v
	}
	if v := toInt(m["initialGraceSeconds"], 0); v > 0 {
		cfg.InitialGraceSeconds = v
	}
	return cfg
}

// tickLiveness roda a cada intervalo do runner: envia ping, avalia o teto
// absoluto e renova o deadline quando o viewer ja provou estar vivo.
func (m *Manager) tickLiveness(session *Session) {
	if session == nil || session.live == nil {
		return
	}
	now := time.Now().UTC()

	m.sendControl(session, ControlTypePing, nil)

	if session.live.ExceededMaxDeadline(now) {
		log.Printf("[remote-session] teto de sessao atingido (max-duration): sessionId=%s\n", session.ID)
		m.closeSession(session.ID, "max-duration")
		return
	}

	// Sem o primeiro sinal do viewer (viewer antigo, sem ping): o prazo
	// original do start continua valendo — nao renovar no escuro.
	if !session.live.PeerSeen() {
		if now.After(session.initialDeadline) {
			log.Printf("[remote-session] prazo original expirado sem sinal do viewer: sessionId=%s\n", session.ID)
			m.closeSession(session.ID, "expired")
		}
		return
	}

	m.extendDeadline(session)
}

// extendDeadline renova o prazo da sessao enquanto o viewer estiver vivo,
// sem ultrapassar maxDeadline (teto absoluto).
func (m *Manager) extendDeadline(session *Session) {
	if session == nil || session.live == nil {
		return
	}
	next := session.live.Deadline(time.Now().UTC(), session.ttl)
	if next.IsZero() {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[session.ID]; ok {
		session.ExpiresAt = next
	}
}

// closeSession adquire o lock do manager e encerra a sessao. Usado pelos
// callbacks que rodam fora do lock (runner de liveness).
func (m *Manager) closeSession(sessionID, reason string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closeSessionLocked(sessionID, reason)
}

// handleControlFrame processa um frame do canal .control da sessao.
// Valida sessionId, descarta o eco do proprio agente e trata ping/pong
// (liveness) e keyframe (tela).
func (m *Manager) handleControlFrame(session *Session, data []byte) {
	if session == nil {
		return
	}
	env, err := DecodeRemoteSessionControl(data)
	if err != nil {
		log.Printf("[remote-session] frame de controle descartado: %v\n", err)
		return
	}
	if env.SessionID != "" && !strings.EqualFold(strings.TrimSpace(env.SessionID), session.ID) {
		return
	}
	if env.From == sessioncontrol.RoleAgent {
		return // eco do proprio agente
	}

	// Presenca: somente ping/pong do VIEWER contam. O servidor nao prova que
		// o navegador esta vivo.
	if session.live != nil && env.From == sessioncontrol.RoleViewer &&
		(env.Type == ControlTypePing || env.Type == ControlTypePong) {
		session.live.NotePeerSignal(time.Now().UTC())
	}

	switch env.Type {
	case ControlTypePing:
		m.sendControl(session, ControlTypePong, nil)
	case ControlTypeKeyframe:
		m.mu.RLock()
		screenSession, ok := m.screenSessions[session.ID]
		m.mu.RUnlock()
		if ok {
			screenSession.RequestKeyFrame()
		}
	case ControlTypePong, ControlTypeClosed:
		// presenca ja registrada acima
	}
}

// sendControl publica um frame de controle no subject .control da sessao.
func (m *Manager) sendControl(session *Session, typ string, payload map[string]any) {
	if session == nil || m.natsStream == nil {
		return
	}
	if session.controlSeqMu != nil {
		session.controlSeqMu.Lock()
		session.controlSeq++
		session.controlSeqMu.Unlock()
	}
	seq := session.controlSeq

	env := sessioncontrol.NewEnvelope(sessioncontrol.RoleAgent, typ, session.ID, seq, payload)
	raw, err := encodeRemoteSessionControl(env)
	if err != nil {
		log.Printf("[remote-session] falha ao codificar controle: %v\n", err)
		return
	}
	if err := m.natsStream.PublishControl(session.ID, raw); err != nil && typ != ControlTypeClosed {
		log.Printf("[remote-session] falha ao publicar controle %s: %v\n", typ, err)
	}
}

// ── Stream runners (iniciados em goroutines pelo handleStart) ──

func (m *Manager) runScreenSession(ctx context.Context, session *Session) {
	defer close(session.doneCh)

	if m.natsStream == nil {
		log.Printf("[remote-session-screen] ERRO: natsStream nao configurado para sessao %s\n", session.ID)
		return
	}

	log.Printf("[remote-session-screen] iniciando screen capturer para sessao %s (quality=%s codec=%s)\n",
		session.ID, session.Quality, session.Codec)

	// Seleção de monitor opcional via payload (Meta["monitorIndex"]). Fallback 0 (primário).
	monitorIndex := 0
	if idx, ok := session.Meta["monitorIndex"].(float64); ok && int(idx) >= 0 {
		monitorIndex = int(idx)
	}

	screenSession, err := NewSessionScreenMonitor(session.ID, m.natsStream, monitorIndex)
	if err != nil {
		log.Printf("[remote-session-screen] ERRO ao criar SessionScreen: %v\n", err)
		m.publishEvent(session.ID, "error", map[string]string{"error": err.Error()})
		return
	}
	defer screenSession.Stop()

	log.Printf("[remote-session-screen] SessionScreen criado com sucesso para sessao %s\n", session.ID)

	// Publica a lista de monitores do agent para o viewer (seletor dinâmico).
	if mons, err := screen.GetMonitors(); err == nil && len(mons) > 0 {
		_ = m.natsStream.PublishMonitors(session.ID, mons)
		log.Printf("[remote-session-screen] %d monitor(es) publicado(s) para sessao %s\n", len(mons), session.ID)
	}

	// Registra referencia para permitir handleQuality propagar mudancas
	m.mu.Lock()
	m.screenSessions[session.ID] = screenSession
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		delete(m.screenSessions, session.ID)
		m.mu.Unlock()
	}()

	// Configura qualidade, codec, imagem e FPS.
	// NOTA (qualidade): o backend NÃO envia "auto" no payload de start
	// (RemoteSessionCommandHandlers) — tratá-lo como ausente significava
	// sessão nascendo em MODO MANUAL com override do perfil (ex. unlimited
	// = 75%) e a escada automática 10→90 nunca rodava: era o card travado
	// em 75%. Sessão agora nasce em MODO AUTO por padrão: a escada parte da
	// POTÊNCIA DA MÁQUINA (CPU/memória/núcleos) e refina de 10 em 10 com as
	// netstats do viewer. Modo manual só via comando quality (auto=false).
	autoStart := true
	if a, ok := session.Meta["auto"].(bool); ok {
		autoStart = a
	}
	screenSession.SetQuality(session.Quality)
	screenSession.SetCodec(session.Codec)
	screenSession.SetManualMode(!autoStart)
	if session.ImageQuality > 0 && !autoStart {
		// Override manual EXPLÍCITO (comando quality auto=false) — clampa 10-90.
		screenSession.SetImageQuality(session.ImageQuality)
	} else if autoStart {
		// Semente automática pelo poder/carga da máquina (10-90, de 10 em 10).
		screenSession.SetImageQualityAuto(SeedAutoImageQuality())
	}
	// FPS: >0 = máximo; 0 = sem limite (perfil "unlimited"). No modo auto o
	// limite do perfil (payload) é o desejado; 0 = captura sem throttle.
	if session.MaxFps > 0 {
		screenSession.SetMaxFps(session.MaxFps)
	} else if session.Quality == "unlimited" {
		screenSession.SetMaxFps(0) // sem limite
	}
	// Modo tile-based (otimização de banda) — ATIVADO POR PADRÃO.
	// Pode ser desligado explicitamente via payload Meta["tileMode"]:false.
	tileMode := true
	if tm, ok := session.Meta["tileMode"].(bool); ok {
		tileMode = tm
	}
	screenSession.SetTileMode(tileMode)
	log.Printf("[remote-session-screen] tile-mode=%v para sessao %s\n", tileMode, session.ID)

	// Cursor separado (P2) — ATIVADO POR PADRÃO: frame sem cursor + subject .cursor.
	// Desligar via Meta["cursorSeparate"]:false.
	cursorSeparate := true
	if cs, ok := session.Meta["cursorSeparate"].(bool); ok {
		cursorSeparate = cs
	}
	screenSession.SetCursorSeparate(cursorSeparate)
	log.Printf("[remote-session-screen] cursor-separate=%v para sessao %s\n", cursorSeparate, session.ID)

	// Subscreve input do viewer (mouse/teclado + netstats p/ adaptação)
	screenSession.inputCtrl.SetNetstatsHandler(func(rttMs, recvKbps float64, recvFrames int) {
		screenSession.UpdateNetworkMetrics(rttMs, recvKbps, recvFrames)
	})
	inputSub, err := m.natsStream.SubscribeToInput(session.ID, func(data []byte) {
		screenSession.inputCtrl.HandleInput(data)
	})
	if err != nil {
		log.Printf("[remote-session-screen] ERRO ao subscrever input: %v", err)
	} else {
		defer inputSub.Unsubscribe()
	}

	// O canal .control e assinado uma unica vez em handleStart (para todos os
	// kinds); o keyframe chega por handleControlFrame -> RequestKeyFrame().

	// ── Clipboard (somente texto, bidirecional) ──
	// viewer→agent: o viewer publica o texto em .clipboard.req; o agent aplica
	//   no clipboard do Windows (SetClipboardText) e injeta Ctrl+V para colar.
	// agent→viewer: um monitor detecta mudança no clipboard da máquina remota e
	//   publica em .clipboard; o viewer escreve no clipboard local do usuário.
	var clipboardMu sync.Mutex
	lastPublished := ""   // último texto publicado ao viewer (evita eco)
	lastSetByViewer := "" // último texto aplicado a partir do viewer (evita re-publicar)

	clipSub, clipErr := m.natsStream.SubscribeToClipboardReq(session.ID, func(text string) {
		if err := screen.SetClipboardText(text); err != nil {
			log.Printf("[remote-session-screen] SetClipboardText falhou: %v", err)
			return
		}
		clipboardMu.Lock()
		lastSetByViewer = text
		lastPublished = text
		clipboardMu.Unlock()
		// Injeta Ctrl+V para colar imediatamente no app remoto focado.
		_ = screen.InjectKeyDown(screen.VK_CONTROL)
		_ = screen.InjectKeyPress('V')
		_ = screen.InjectKeyUp(screen.VK_CONTROL)
	})
	if clipErr != nil {
		log.Printf("[remote-session-screen] ERRO ao subscrever clipboard.req: %v", clipErr)
	} else {
		defer clipSub.Unsubscribe()
	}

	// Monitor do clipboard da máquina remota (1s): publica ao viewer quando muda.
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				text, err := screen.GetClipboardText()
				if err != nil {
					continue
				}
				clipboardMu.Lock()
				if text != "" && text != lastPublished && text != lastSetByViewer {
					lastPublished = text
					clipboardMu.Unlock()
					if err := m.natsStream.PublishClipboard(session.ID, text); err != nil {
						log.Printf("[remote-session-screen] PublishClipboard falhou: %v", err)
					}
				} else {
					clipboardMu.Unlock()
				}
			case <-session.stopCh:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	// Context que encerra quando stopCh fecha ou expires
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		select {
		case <-session.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	fps := screenSession.quality.Current().EffectiveFps()
	// 0 = sem limite (o loop de captura respeita: curFps 0 = sem throttle)
	log.Printf("[remote-session-screen] iniciando loop de captura (fps=%d) para sessao %s\n", fps, session.ID)
	if err := screenSession.Start(ctx, fps); err != nil {
		log.Printf("[remote-session-screen] loop de captura encerrado com erro: %v\n", err)
		m.publishEvent(session.ID, "error", map[string]string{"error": err.Error()})
	} else {
		log.Printf("[remote-session-screen] loop de captura encerrado normalmente para sessao %s\n", session.ID)
	}
}

func (m *Manager) runTerminalSession(ctx context.Context, session *Session) {
	defer close(session.doneCh)

	if m.natsStream == nil {
		log.Printf("[remote-session-term] ERRO: natsStream nao configurado para sessao %s\n", session.ID)
		return
	}

	// Shell padrao: powershell
	defaultShell := terminal.ShellPowerShell
	if sk, ok := sessionMetaString(session, "shell"); ok && sk != "" {
		// A14: campo shell vem do servidor sem validação — restringe aos
		// ShellKind conhecidos e a distros WSL no charset permitido antes
		// de chegar ao CreateProcessW (via ConPTY).
		defaultShell = terminal.ValidateSessionShellKind(sk)
	}

	cols := sessionMetaInt(session, "termCols", 120)
	rows := sessionMetaInt(session, "termRows", 40)

	log.Printf("[remote-session-term] iniciando console para sessao %s (shell=%s cols=%d rows=%d)\n",
		session.ID, defaultShell, cols, rows)

	sessTerm := NewSessionTerminal(session.ID, m.natsStream, session.Recording)

	// Quando o console (shell) encerra, fecha a sessão para liberar recursos
	// e notificar o viewer (não deixa sessão "morta" ativa).
	sessTerm.SetOnExit(func(reason string) {
		log.Printf("[remote-session-term] console encerrado (sessao %s): %s", session.ID, reason)
		m.mu.Lock()
		m.closeSessionLocked(session.ID, "terminal-exit")
		m.mu.Unlock()
	})

	// Console único — cria o terminal (subjects fixos term.out / term.in)
	console, err := sessTerm.Start(ctx, defaultShell, cols, rows)
	if err != nil {
		log.Printf("[remote-session-term] ERRO ao iniciar console (conptyDisponivel=%v): %v",
			terminal.IsConPTYAvailable(), err)
		m.publishEvent(session.ID, "error", map[string]string{"error": err.Error()})
		return
	}

	// Notificar viewer com shells disponiveis e console pronto
	availableShells := []string{"powershell", "cmd"}
	if available, distros := terminal.IsWSLAvailable(); available {
		for _, d := range distros {
			availableShells = append(availableShells, "wsl:"+d)
		}
	}
	readyPayload, _ := json.Marshal(map[string]any{
		"shells":    availableShells,
		"consoleId": console.ID,
		"termCols":  cols,
		"termRows":  rows,
		"backend":   terminal.ShellBackendName(console.Shell),
	})
	m.natsStream.PublishTermReady(session.ID, map[string]any{
		"shells":    availableShells,
		"consoleId": console.ID,
		"termCols":  cols,
		"termRows":  rows,
		"backend":   terminal.ShellBackendName(console.Shell),
	})
	// Guarda o payload para republicar no primeiro term.in (handshake).
	sessTerm.SetReadyPayload(readyPayload)
	log.Printf("[remote-session-term] console pronto para sessao %s (consoleId=%s, shells=%v)\n",
		session.ID, console.ID, availableShells)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		select {
		case <-session.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	<-ctx.Done()
	sessTerm.Stop()
}

// sessionMetaString extrai string de session.meta (via payload original).
// Como Session nao tem campo meta, usamos valores default.
func sessionMetaString(session *Session, key string) (string, bool) {
	if session.Meta == nil {
		return "", false
	}
	v, ok := session.Meta[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func sessionMetaInt(session *Session, key string, defaultVal int) int {
	if session.Meta == nil {
		return defaultVal
	}
	v, ok := session.Meta[key]
	if !ok {
		return defaultVal
	}
	switch val := v.(type) {
	case float64:
		return int(val)
	case int:
		return val
	case int64:
		return int(val)
	default:
		return defaultVal
	}
}

func (m *Manager) runFilesSession(ctx context.Context, session *Session) {
	defer close(session.doneCh)

	if m.natsStream == nil {
		return
	}

	// Raiz padrão: C:\ (Windows). Pode ser sobrescrita via payload Meta["rootPath"].
	rootPath := "C:\\"
	if rp, ok := sessionMetaString(session, "rootPath"); ok && rp != "" {
		rootPath = rp
	}

	sf := NewSessionFiles(session.ID, m.natsStream, rootPath)
	log.Printf("[remote-session-files] sessão de arquivos iniciada para %s (root=%s)", session.ID, rootPath)

	// Context que encerra quando stopCh fecha (sessão encerrada/expirou)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		select {
		case <-session.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	if err := sf.Start(ctx); err != nil {
		log.Printf("[remote-session-files] sessão encerrada: %v", err)
	} else {
		log.Printf("[remote-session-files] sessão encerrada normalmente para %s", session.ID)
	}
}

func (m *Manager) runProxySession(ctx context.Context, session *Session) {
	defer close(session.doneCh)

	if m.natsStream == nil {
		return
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		select {
		case <-session.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	// TODO: integrar netproxy.Proxy com natsStream.SubscribeToProxyReq
	select {
	case <-ctx.Done():
	case <-session.stopCh:
	}
}

// runProcessesSession inicia a sessão de processos/serviços remota.
func (m *Manager) runProcessesSession(ctx context.Context, session *Session) {
	defer close(session.doneCh)

	if m.natsStream == nil {
		return
	}

	sp := NewSessionProcesses(session.ID, m.natsStream)
	log.Printf("[remote-session-processes] sessão de processos/serviços iniciada para %s", session.ID)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		select {
		case <-session.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	if err := sp.Start(ctx); err != nil {
		log.Printf("[remote-session-processes] sessão encerrada: %v", err)
	} else {
		log.Printf("[remote-session-processes] sessão encerrada normalmente para %s", session.ID)
	}
}

// GetActiveSessions retorna um snapshot das sessoes ativas (para UI/tray).
func (m *Manager) GetActiveSessions() []Session {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		result = append(result, *s)
	}
	return result
}

// HasActiveSession retorna true se existe sessao do tipo especificado.
func (m *Manager) HasActiveSession(kind string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.sessions {
		if s.Kind == kind {
			return true
		}
	}
	return false
}

// CountActive retorna o numero de sessoes ativas.
func (m *Manager) CountActive() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sessions)
}

// StopAll fecha todas as sessoes ativas.
func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.sessions {
		m.closeSessionLocked(id, "agent-shutdown")
	}
}

// ServiceName retorna o nome do service para logging.
func (m *Manager) ServiceName() string {
	return "remotesession.Manager"
}

// Startup marca o ciclo de vida do domínio de acesso remoto como iniciado.
// Não há contexto de ciclo de vida próprio a preparar: as goroutines de
// sessão são controladas por stopCh (fechado via StopAll/closeSessionLocked),
// não por um ctx compartilhado. Idempotente: a primeira chamada vence.
func (m *Manager) Startup(_ context.Context) error {
	if m == nil {
		return nil
	}
	if m.started {
		return nil
	}
	m.started = true
	return nil
}

// Shutdown encerra todas as sessoes ativas via StopAll (fecha stopCh de cada
// sessão, parando as goroutines de stream). Idempotente.
func (m *Manager) Shutdown() error {
	if m == nil || !m.started {
		return nil
	}
	m.StopAll()
	m.started = false
	return nil
}

// normalizeTransport mapeia o transporte pedido pelo servidor para o que o
// binario realmente suporta. WebRTC foi removido: qualquer pedido de "webrtc"
// (ou vazio) cai para NATS.
func normalizeTransport(requested string) string {
	if requested == "" || requested == "webrtc" {
		return "nats"
	}
	return requested
}

// helper
func toString(v any) string {
	s, _ := v.(string)
	return s
}

func toInt(v any, defaultVal int) int {
	switch val := v.(type) {
	case float64:
		return int(val)
	case int:
		return val
	case int64:
		return int(val)
	default:
		return defaultVal
	}
}

func toFloat64(v any) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	default:
		return 0, false
	}
}
