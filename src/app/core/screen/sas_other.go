//go:build !windows

package screen

import "errors"

// ErrSASUnsupported indica que o recurso é exclusivo do Windows.
var ErrSASUnsupported = errors.New("Secure Attention Sequence (Ctrl+Alt+Del) não é suportada nesta plataforma")

// SendSAS é um stub para plataformas não-Windows.
func SendSAS() error { return ErrSASUnsupported }

// LockWorkstation é um stub para plataformas não-Windows.
func LockWorkstation() error {
	return errors.New("bloqueio de estação (Win+L) não é suportado nesta plataforma")
}
