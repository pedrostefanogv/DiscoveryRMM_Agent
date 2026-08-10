//go:build !windows

package remotesession

import (
	"context"

	"github.com/nats-io/nats.go"
)

// Este arquivo fornece um stub do Manager para plataformas não-Windows.
// O acesso remoto (screen/terminal) é exclusivo do Windows; em outras
// plataformas o Manager é um no-op que rejeita comandos.

// Session representa uma sessao remota (stub).
type Session struct {
	ID   string         `json:"sessionId"`
	Kind string         `json:"kind"`
	Meta map[string]any `json:"-"`
}

// Manager é um stub no-op para plataformas não-Windows.
type Manager struct{}

// NewManager cria um Manager stub.
func NewManager(nc *nats.Conn) *Manager {
	return &Manager{}
}

// SetNatsConn é um no-op em plataformas não-Windows.
func (m *Manager) SetNatsConn(nc *nats.Conn, tenantID, siteID, agentID string) {}

// SetCallbacks é um no-op em plataformas não-Windows.
func (m *Manager) SetCallbacks(onStarted func(sessionID, kind string), onEnded func(sessionID, reason string)) {
}

// HandleCommand rejeita comandos em plataformas não-Windows.
func (m *Manager) HandleCommand(ctx context.Context, payload map[string]any) (bool, string) {
	return false, "acesso remoto não suportado nesta plataforma"
}

// ServiceName retorna o nome do service para logging.
func (m *Manager) ServiceName() string {
	return "remotesession.Manager"
}

// Startup é um no-op em plataformas não-Windows.
func (m *Manager) Startup(_ context.Context) error {
	return nil
}

// Shutdown é um no-op em plataformas não-Windows.
func (m *Manager) Shutdown() error {
	return nil
}
