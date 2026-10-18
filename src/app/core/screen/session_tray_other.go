//go:build !windows

package screen

import "fmt"

// SessionTrayOptions descreve o ícone da sessão (stub).
type SessionTrayOptions struct {
	Operator        string
	Text            string
	AllowDisconnect bool
	OnDisconnect    func()
}

// SessionTray é um stub sem efeito para plataformas não-Windows.
type SessionTray struct{}

// NewSessionTray retorna erro em plataformas não-Windows.
func NewSessionTray(SessionTrayOptions) (*SessionTray, error) {
	return nil, fmt.Errorf("ícone de bandeja disponível apenas no Windows")
}

// Show/Hide/Close são no-ops no stub.
func (t *SessionTray) Show(string) error { return nil }

// Hide é um no-op no stub.
func (t *SessionTray) Hide() error { return nil }

// Close é um no-op no stub.
func (t *SessionTray) Close() {}
