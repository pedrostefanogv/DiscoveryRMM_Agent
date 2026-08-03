//go:build !windows

package terminal

import "errors"

// Este arquivo fornece stubs para plataformas não-Windows (darwin, linux).
// O terminal remoto (ConPTY) é uma funcionalidade exclusiva do Windows.

// ShellKind define o tipo de shell.
type ShellKind string

const (
	ShellCmd        ShellKind = "cmd"
	ShellPowerShell ShellKind = "powershell"
	ShellBash       ShellKind = "bash"
	ShellWSL        ShellKind = "wsl"
)

// ConPTYShell é um stub para plataformas não-Windows.
type ConPTYShell struct{}

// NewConPTYShell retorna erro em plataformas não-Windows.
func NewConPTYShell(shell ShellKind, cols, rows int, onOutput func(string)) (*ConPTYShell, error) {
	return nil, errors.New("ConPTY não suportado nesta plataforma")
}

// IsWSLAvailable retorna false em plataformas não-Windows.
func IsWSLAvailable() (available bool, distros []string) {
	return false, nil
}
