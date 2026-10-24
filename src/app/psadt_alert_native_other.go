//go:build !windows

package app

import "context"

// Stubs não-Windows: o backend nativo não é suportado; os chamadores aplicam
// seus fallbacks próprios (toast do pipeline, shutdown.exe etc.).

func (a *App) nativeToastAlert(ctx context.Context, p PsadtAlertPayload) (int, string, string, bool) {
	return 1, "", "", false
}

func (a *App) nativeModalAlert(ctx context.Context, p PsadtAlertPayload) (int, string, string, bool) {
	return 1, "", "", false
}

func (a *App) nativeProgressAlert(ctx context.Context, p PsadtAlertPayload) (int, string, string, bool) {
	return 1, "", "", false
}

func (a *App) nativePowerCountdown(ctx context.Context, action string, delaySeconds int, message string) (string, bool) {
	return "", false
}
