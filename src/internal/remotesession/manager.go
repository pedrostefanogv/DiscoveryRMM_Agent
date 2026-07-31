package remotesession

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"discovery/internal/safego"
	"discovery/internal/terminal"
)

// Session representa uma sessao remota ativa gerenciada pelo agent.
type Session struct {
	ID          string    `json:"sessionId"`
	Kind        string    `json:"kind"`        // screen, terminal, files, proxy
	Transport   string    `json:"transport"`   // webrtc, nats, http
	Quality     string    `json:"quality"`     // ultra, high, medium, low, ultralow
	Codec       string    `json:"codec"`       // jpeg, webp, h264
	ImageQuality int      `json:"imageQuality"` // compressão JPEG/WebP 1-100, default 70
	MaxFps       int      `json:"maxFps"`       // taxa máxima de quadros por segundo, default 15
	NatsSubject string    `json:"natsSubject"` // subject base para stream
	ExpiresAt   time.Time `json:"expiresAtUtc"`
	StartedAt   time.Time `json:"startedAtUtc"`
	Recording   bool      `json:"recording"`

	// estado interno
	stopCh chan struct{}
	doneCh chan struct{}
	Meta   map[string]any `json:"-"` // metadados do payload original (shell, termCols, termRows, etc.)
}

// Manager gerencia o lifecycle de sessoes remotas no agent.
type Manager struct {
	mu         sync.RWMutex
	sessions   map[string]*Session       // key: sessionId
	screenSessions map[string]*SessionScreen // key: sessionId — referência às goroutines ativas
	nc         *nats.Conn
	natsStream *NatsStreamHandler

	// callbacks para notificar a UI/tray
	onSessionStarted func(sessionID, kind string)
	onSessionEnded   func(sessionID, reason string)
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

	expiresAt, _ := time.Parse(time.RFC3339, toString(payload["expiresAtUtc"]))
	if expiresAt.IsZero() {
		expiresAt = time.Now().Add(30 * time.Minute)
	}

	session := &Session{
		ID:           sessionID,
		Kind:         kind,
		Transport:    transport,
		Quality:      quality,
		Codec:        codec,
		ImageQuality: imageQuality,
		MaxFps:       maxFps,
		NatsSubject:  natsSubject,
		StartedAt:    time.Now(),
		ExpiresAt:    expiresAt,
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
		Meta:         payload, // armazena payload original para acesso a shell, termCols, termRows
	}
	m.sessions[sessionID] = session

	// Garante que o NatsStreamHandler esta configurado (extrai tenant/site/agent do subject)
	if m.natsStream == nil {
		// Tenta extrair IDs do natsSubject: tenant.{c}.site.{s}.agent.{a}.remote.session.{id}
		m.publishEventLegacy(sessionID, "started", session)
	} else {
		m.natsStream.PublishEvent(sessionID, "started", session)
	}

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
	case "proxy":
		safego.Go(func() {
			m.runProxySession(ctx, session)
		}, func(line string) {
			fmt.Printf("[remote-session-proxy] %s\n", line)
		})
	}

	// Monitor de expiracao — copia o stopCh ANTES de soltar o lock (evita race)
	stopCh := session.stopCh
	safego.Go(func() {
		m.monitorExpiration(sessionID, expiresAt, stopCh)
	}, func(line string) {
		fmt.Printf("[remote-session] %s\n", line)
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
	imageQuality := payload["imageQuality"]   // pode ser nil ou float64
	maxFpsVal := payload["maxFps"]            // pode ser nil ou float64
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
		log.Printf("[remote-session] qualidade alterada: sessionId=%s quality=%s codec=%s imageQ=%d maxFps=%d\n",
			sessionID, quality, codec, s.ImageQuality, s.MaxFps)
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

func (m *Manager) monitorExpiration(sessionID string, expiresAt time.Time, stopCh chan struct{}) {
	select {
	case <-time.After(time.Until(expiresAt)):
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, ok := m.sessions[sessionID]; ok {
			m.closeSessionLocked(sessionID, "expired")
		}
	case <-stopCh:
		// sessao fechada antes de expirar
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

	screenSession, err := NewSessionScreen(session.ID, m.natsStream)
	if err != nil {
		log.Printf("[remote-session-screen] ERRO ao criar SessionScreen: %v\n", err)
		m.publishEvent(session.ID, "error", map[string]string{"error": err.Error()})
		return
	}
	defer screenSession.Stop()

	log.Printf("[remote-session-screen] SessionScreen criado com sucesso para sessao %s\n", session.ID)

	// Registra referencia para permitir handleQuality propagar mudancas
	m.mu.Lock()
	m.screenSessions[session.ID] = screenSession
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		delete(m.screenSessions, session.ID)
		m.mu.Unlock()
	}()

	// Configura qualidade, codec, imagem e FPS
	screenSession.SetQuality(session.Quality)
	screenSession.SetCodec(session.Codec)
	if session.ImageQuality > 0 {
		screenSession.SetImageQuality(session.ImageQuality)
	}
	if session.MaxFps > 0 {
		screenSession.SetMaxFps(session.MaxFps)
	}

	// Subscreve input do viewer (mouse/teclado)
	inputSub, err := m.natsStream.SubscribeToInput(session.ID, func(data []byte) {
		screenSession.inputCtrl.HandleInput(data)
	})
	if err != nil {
		log.Printf("[remote-session-screen] ERRO ao subscrever input: %v", err)
	} else {
		defer inputSub.Unsubscribe()
	}

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

	fps := screenSession.quality.Current().Fps
	if fps <= 0 {
		fps = 15
	}
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
		return
	}

	// Shell padrao: powershell
	defaultShell := terminal.ShellPowerShell
	if sk, ok := sessionMetaString(session, "shell"); ok && sk != "" {
		defaultShell = terminal.ShellKind(sk)
	}

	cols := sessionMetaInt(session, "termCols", 120)
	rows := sessionMetaInt(session, "termRows", 40)

	sessTerm := NewSessionTerminal(session.ID, m.natsStream, session.Recording)

	// Criar tab inicial
	firstTab, err := sessTerm.CreateTab(ctx, defaultShell, cols, rows)
	if err != nil {
		log.Printf("[remote-session-term] ERRO ao criar tab inicial: %v", err)
		m.publishEvent(session.ID, "error", map[string]string{"error": err.Error()})
		return
	}

	// Subscrever comandos de controle de tabs
	m.natsStream.SubscribeToTermCreate(session.ID, func(data []byte) {
		var req struct {
			Shell string `json:"shell"`
			Cols  int    `json:"cols"`
			Rows  int    `json:"rows"`
		}
		if err := json.Unmarshal(data, &req); err != nil {
			return
		}
		sk := terminal.ShellKind(req.Shell)
		if sk == "" {
			sk = terminal.ShellPowerShell
		}
		c := req.Cols
		r := req.Rows
		if c <= 0 { c = cols }
		if r <= 0 { r = rows }
		tab, err := sessTerm.CreateTab(ctx, sk, c, r)
		if err != nil {
			log.Printf("[remote-session-term] ERRO ao criar tab %s: %v", req.Shell, err)
			return
		}
		log.Printf("[remote-session-term] nova tab criada: %s shell=%s", tab.ID, sk)
	})

	m.natsStream.SubscribeToTermClose(session.ID, func(data []byte) {
		var req struct {
			TabID string `json:"tabId"`
		}
		if err := json.Unmarshal(data, &req); err != nil {
			return
		}
		sessTerm.CloseTab(req.TabID)
	})

	// Notificar viewer com shells disponiveis
	availableShells := []string{"powershell", "cmd"}
	if available, distros := terminal.IsWSLAvailable(); available {
		for _, d := range distros {
			availableShells = append(availableShells, "wsl:"+d)
		}
	}
	m.natsStream.PublishTermReady(session.ID, map[string]any{
		"shells":      availableShells,
		"defaultTab":  firstTab.ID,
		"termCols":    cols,
		"termRows":    rows,
	})

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

	sf := NewSessionFiles(session.ID, m.natsStream, "C:\\")
	log.Printf("[remote-session-files] sessão de arquivos iniciada para %s", session.ID)

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

// normalizeTransport garante que o transport reportado ao servidor/viewer
// reflita a capacidade real do binario. Se o servidor pediu WebRTC mas o
// binario foi compilado sem a build tag webrtc, faz fallback para NATS.
func normalizeTransport(requested string) string {
	if requested == "" {
		return "nats"
	}
	if requested == "webrtc" && !WebRTCAvailable {
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
