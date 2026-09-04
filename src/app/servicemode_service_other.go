//go:build !windows

package app

import "log"

// RunServiceMode é um stub não-Windows (o agente desktop só existe no Windows).
func RunServiceMode() {
	log.Println("[service] modo serviço não suportado nesta plataforma")
}

// IsWindowsServiceProcess é um stub não-Windows.
func IsWindowsServiceProcess() bool { return false }
