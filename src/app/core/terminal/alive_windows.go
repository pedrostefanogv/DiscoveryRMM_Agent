//go:build windows

package terminal

import (
	"golang.org/x/sys/windows"
)

// processAlive reporta se o processo com o PID informado ainda está em
// execução. Usa OpenProcess + GetExitCodeProcess, sem bloquear e sem
// consumir o Wait() — seguro para detecção de morte prematura no startup.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(
		windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE,
		false,
		uint32(pid),
	)
	if err != nil {
		// Processo não existe (ERROR_INVALID_PARAMETER) ou sem acesso.
		return false
	}
	defer windows.CloseHandle(handle)

	// Tipos de saída "STILL_ACTIVE" (259) indicam processo vivo.
	var exitCode uint32
	err = windows.GetExitCodeProcess(handle, &exitCode)
	if err != nil {
		return false
	}
	// 259 = STILL_ACTIVE (processo ainda em execução).
	return exitCode == 259
}

// processExitCodeNow devolve o exit code corrente do processo com o PID
// informado (OpenProcess + GetExitCodeProcess). ok=false quando o processo
// não existe mais ou o handle está indisponível. Usado para diagnosticar a
// morte prematura do ConPTY no startup (0xC0000142 vs outros códigos).
func processExitCodeNow(pid int) (int, bool) {
	if pid <= 0 {
		return 0, false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return 0, false
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return 0, false
	}
	if exitCode == 259 { // STILL_ACTIVE — não morreu de fato
		return 0, false
	}
	return int(int32(exitCode)), true
}
