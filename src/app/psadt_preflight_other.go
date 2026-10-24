//go:build !windows

package app

import "time"

// RunNativePreflightChecks não é suportado fora do Windows.
func (a *App) RunNativePreflightChecks() NativePreflightResult {
	return NativePreflightResult{
		Error:        "preflight suportado apenas no Windows",
		Success:      false,
		CheckedAtUTC: time.Now().UTC().Format(time.RFC3339),
	}
}
