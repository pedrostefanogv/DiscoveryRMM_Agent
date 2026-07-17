//go:build !windows

package app

import "time"

func (a *App) RunPSADTPreflightChecks() PSADTPreflightResult {
	result := PSADTPreflightResult{CheckedAtUTC: time.Now().UTC().Format(time.RFC3339)}
	result.Error = "PSADT suportado apenas em Windows"
	return result
}
