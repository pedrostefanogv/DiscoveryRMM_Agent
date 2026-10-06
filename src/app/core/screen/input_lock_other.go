//go:build !windows

package screen

import "errors"

// Stubs não-Windows do bloqueio de entrada e do indicador de bandeja.

// BlockInputSystem é um stub para plataformas não-Windows.
func BlockInputSystem() (string, error) {
	return "", errors.New("bloqueio de entrada (KVM lock) não é suportado nesta plataforma")
}

// UnblockInputSystem é um stub para plataformas não-Windows.
func UnblockInputSystem() error {
	return errors.New("bloqueio de entrada (KVM lock) não é suportado nesta plataforma")
}

// TrayIndicator é um stub sem efeito para plataformas não-Windows.
type TrayIndicator struct{}

// NewTrayIndicator é um stub para plataformas não-Windows.
func NewTrayIndicator() (*TrayIndicator, error) {
	return nil, errors.New("indicador de bandeja não é suportado nesta plataforma")
}

// Show é um no-op no stub.
func (t *TrayIndicator) Show(string) error { return nil }

// Hide é um no-op no stub.
func (t *TrayIndicator) Hide() error { return nil }

// Close é um no-op no stub.
func (t *TrayIndicator) Close() {}
