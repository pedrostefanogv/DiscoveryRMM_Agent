//go:build !windows

package terminal

import (
	"syscall"
)

// processAlive reporta se o processo ainda está em execução usando signal 0
// (que não entrega sinal algum, apenas verifica a existência do processo).
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// Signal(0) verifica permissão/existência; retorna nil se o processo
	// existe.
	return syscall.Kill(pid, 0) == nil
}