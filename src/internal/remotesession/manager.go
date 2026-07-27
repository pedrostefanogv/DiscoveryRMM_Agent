package remotesession

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"discovery/internal/safego"
)

// Session representa uma sessao remota ativa gerenciada pelo agent.
type Session struct {
	ID           string    `json:"sessionId"`
	Kind         string    `json:"kind"`         // screen, terminal, files, proxy
	Transport    string    `json:"transport"`    // webrtc, nats, http
	Quality      string    `json:"quality"`      // ultra, high, medium, low, ultralow
	Codec        string    `json:"codec"`        // jpeg, webp, h264
	NatsSubject  string    `json:"natsSubject"`  // subject base para stream
	ExpiresAt    time.Time `json:"expiresAtUtc"`
	StartedAt    time.Time `json:"startedAtUtc"`
	Recording    bool      `json:"recording"`

	// estado interno
	stopCh chan struct{}
	doneCh chan struct{}
}

// Manager gerencia o lifecycle de sessoes remotas no agent.
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*Session // key: sessionId
	nc       *nats.Conn

	// callbacks para notificar a UI/tray
	onSessionStarted func(sessionID, kind string)
	onSessionEnded   func(sessionID, reason string)
}

// NewManager cria um novo gerenciador de sessoes remotas.
func NewManager(nc *nats.Conn) *Manager {
	return &Manager{
		sessions: make(map[string]*Session),
		nc:       nc,
	}
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
		return m.handleQuality(payload)
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

	m.mu.Lock()
	defer m.mu.Unlock()

	// Fecha sessao anterior do mesmo tipo se existir (uma por kind)
	for id, s := range m.sessions {
		if s.Kind == toString(payload["kind"]) {
			m.closeSessionLocked(id, "superseded")
			break
		}
	}

	expiresAt, _ := time.Parse(time.RFC3339, toString(payload["expiresAtUtc"]))
	if expiresAt.IsZero() {
		expiresAt = time.Now().Add(8 * time.Hour)
	}

	session := &Session{
		ID:          sessionID,
		Kind:        toString(payload["kind"]),
		Transport:   toString(payload["transport"]),
		Quality:     toString(payload["quality"]),
		Codec:       toString(payload["codec"]),
		NatsSubject: toString(payload["natsSubject"]),
		ExpiresAt:   expiresAt,
		StartedAt:   time.Now(),
		stopCh:      make(chan struct{}),
		doneCh:      make(chan struct{}),
	}
	m.sessions[sessionID] = session

	// Publica evento de sessão iniciada no NATS
	m.publishEvent(sessionID, "started", session)

	// Monitor de expiração
	safego.Go(func() {
		m.monitorExpiration(sessionID, expiresAt)
	}, func(format string, args ...interface{}) {
		fmt.Printf("[remote-session] "+format+"\n", args...)
	})

	if m.onSessionStarted != nil {
		m.onSessionStarted(sessionID, session.Kind)
	}

	return true, ""
}

func (m *Manager) handleStop(ctx context.Context, payload map[string]any) (bool, string) {
	sessionID := toString(payload["sessionId"])
	if sessionID == "" {
		return false, "payload sem sessionId"
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	return m.closeSessionLocked(sessionID, "stopped-by-server"), ""
}

func (m *Manager) handleQuality(payload map[string]any) (bool, string) {
	sessionID := toString(payload["sessionId"])
	quality := toString(payload["quality"])
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
	s.Codec = toString(payload["codec"])
	return true, ""
}

func (m *Manager) handleRecordingStart(ctx context.Context, payload map[string]any) (bool, string) {
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

func (m *Manager) handleRecordingStop(ctx context.Context, payload map[string]any) (bool, string) {
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

	select {
	case <-s.stopCh:
		// ja fechado
	default:
		close(s.stopCh)
	}

	m.publishEvent(sessionID, "closed", map[string]string{"reason": reason})

	if m.onSessionEnded != nil {
		m.onSessionEnded(sessionID, reason)
	}
	return true
}

func (m *Manager) publishEvent(sessionID, eventType string, data any) {
	if m.nc == nil || !m.nc.IsConnected() {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"sessionId": sessionID,
		"eventType": eventType,
		"data":      data,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
	// Publica no subject de eventos (formato tenant.{c}.site.{s}.agent.{a}.remote.session.{id}.event)
	// Como estamos no agent, o servidor inscreve nesse subject
	_ = m.nc.Publish(fmt.Sprintf("agent.remote.session.%s.event", sessionID), payload)
}

func (m *Manager) monitorExpiration(sessionID string, expiresAt time.Time) {
	select {
	case <-time.After(time.Until(expiresAt)):
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, ok := m.sessions[sessionID]; ok {
			m.closeSessionLocked(sessionID, "expired")
		}
	case <-m.sessions[sessionID].stopCh:
		// sessao fechada antes de expirar
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

// helper
func toString(v any) string {
	s, _ := v.(string)
	return s
}
