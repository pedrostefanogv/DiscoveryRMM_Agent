//go:build !windows

package app

import "time"

func (a *App) RunPSADTPreflightChecks() PSADTPreflightResult {
	result := PSADTPreflightResult{CheckedAtUTC: time.Now().UTC().Format(time.RFC3339)}
	result.Error = "PSADT suportado apenas em Windows"
	return result
}

func (a *App) RunPSADTWelcome(closeProcesses string, countdown int) PSADTWelcomeResult {
	_ = closeProcesses
	_ = countdown
	result := PSADTWelcomeResult{ExecutedAtUTC: time.Now().UTC().Format(time.RFC3339)}
	result.Success = false
	result.Action = "error"
	result.Message = "PSADT suportado apenas em Windows"
	return result
}

func (a *App) RunPSADTRestartPrompt(countdownSeconds int, silentRestart bool) PSADTRestartPromptResult {
	_ = countdownSeconds
	_ = silentRestart
	result := PSADTRestartPromptResult{ExecutedAtUTC: time.Now().UTC().Format(time.RFC3339)}
	result.Success = false
	result.Action = "error"
	result.Message = "PSADT suportado apenas em Windows"
	return result
}

func (a *App) GetPSADTSessionProperties() PSADTSessionProperties {
	result := PSADTSessionProperties{CheckedAtUTC: time.Now().UTC().Format(time.RFC3339)}
	result.Success = false
	result.Error = "PSADT suportado apenas em Windows"
	return result
}
