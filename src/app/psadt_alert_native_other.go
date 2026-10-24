//go:build !windows

package app

// Stubs não-Windows: o backend nativo não é suportado e o chamador cai no
// caminho PSADT (que também devolve "não é Windows" nos fluxos de hoje).

func (a *App) nativeToastAlert(p PsadtAlertPayload) (int, string, string, bool) {
	return 1, "", "", false
}

func (a *App) nativeModalAlert(p PsadtAlertPayload) (int, string, string, bool) {
	return 1, "", "", false
}

func (a *App) nativePowerCountdown(action string, delaySeconds int, message string) (string, bool) {
	return "", false
}
