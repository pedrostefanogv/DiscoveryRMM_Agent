//go:build windows

package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	psadt "github.com/pedrostefanogv/go-psadt"
	pstypes "github.com/pedrostefanogv/go-psadt/types"
)

// RunPSADTPreflightChecks executa verificacoes pre-flight usando a lib go-psadt.
func (a *App) RunPSADTPreflightChecks() PSADTPreflightResult {
	result := PSADTPreflightResult{CheckedAtUTC: time.Now().UTC().Format(time.RFC3339)}
	a.logs.append("[psadt] executando preflight checks via go-psadt...")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := psadt.NewClient(
		psadt.WithTimeout(60 * time.Second),
	)
	if err != nil {
		result.Error = fmt.Sprintf("psadt.NewClient: %v", err)
		a.logs.append("[psadt] preflight [ERRO] " + result.Error)
		return result
	}
	defer client.Close()

	env, err := client.GetEnvironment()
	if err != nil {
		result.Error = fmt.Sprintf("GetEnvironment: %v", err)
		a.logs.append("[psadt] preflight [ERRO] " + result.Error)
		return result
	}

	result.OSName = env.OS.Name
	result.OSVersion = env.OS.Version
	result.Architecture = env.OS.Architecture
	result.PSVersion = env.PowerShell.PSVersion
	result.ActiveUserSessions = len(env.Users.LoggedOnUserSessions)
	result.ModuleVersion = env.Toolkit.Version

	session, err := client.OpenSessionWithContext(ctx, pstypes.SessionConfig{
		AppVendor:      "Discovery",
		AppName:        "Discovery Agent",
		AppVersion:     "1.0",
		DeploymentType: pstypes.DeployInstall,
		DeployMode:     pstypes.DeployModeSilent,
	})
	if err != nil {
		result.Error = fmt.Sprintf("OpenSession: %v", err)
		a.logs.append("[psadt] preflight [AVISO] " + result.Error)
		result.Success = true
		return result
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		_ = session.CloseWithContext(closeCtx, 0)
	}()

	if isAdmin, err := session.TestCallerIsAdmin(); err == nil {
		result.IsAdmin = isAdmin
	}
	if rebootInfo, err := session.GetPendingReboot(); err == nil && rebootInfo != nil {
		result.RebootPending = rebootInfo.IsSystemRebootPending
	}
	if online, err := session.TestNetworkConnection(); err == nil {
		result.NetworkAvailable = online
	}
	if inFocus, err := session.TestUserInFocusMode(); err == nil {
		result.UserInFocusMode = inFocus
	}

	result.Success = true
	a.logs.append(fmt.Sprintf("[psadt] preflight concluido: os=%s %s admin=%t reboot=%t net=%t focus=%t",
		result.OSName, result.OSVersion, result.IsAdmin, result.RebootPending, result.NetworkAvailable, result.UserInFocusMode))
	return result
}

// RunPSADTWelcome executa o Welcome Dialog via go-psadt.
func (a *App) RunPSADTWelcome(closeProcesses string, countdown int) PSADTWelcomeResult {
	result := PSADTWelcomeResult{ExecutedAtUTC: time.Now().UTC().Format(time.RFC3339)}
	processes := parseProcessList(closeProcesses)
	if countdown <= 0 {
		countdown = 120
	}

	a.logs.append(fmt.Sprintf("[psadt] welcome dialog iniciando: processes=%v countdown=%d", processes, countdown))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	client, err := psadt.NewClient(psadt.WithTimeout(5 * time.Minute))
	if err != nil {
		result.Message = fmt.Sprintf("psadt.NewClient: %v", err)
		return result
	}
	defer client.Close()

	session, err := client.OpenSessionWithContext(ctx, pstypes.SessionConfig{
		AppVendor:      "Discovery",
		AppName:        "Discovery Agent",
		AppVersion:     "1.0",
		DeploymentType: pstypes.DeployInstall,
		DeployMode:     pstypes.DeployModeInteractive,
	})
	if err != nil {
		result.Message = fmt.Sprintf("OpenSession: %v", err)
		return result
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		_ = session.CloseWithContext(closeCtx, 0)
	}()

	start := time.Now()
	welcomeOpts := pstypes.WelcomeOptions{
		CloseProcessesCountdown: countdown,
		CheckDiskSpace:          true,
		AllowDefer:              true,
		DeferTimes:              3,
		Silent:                  false,
	}
	if len(processes) > 0 {
		var defs []pstypes.ProcessDefinition
		for _, p := range processes {
			defs = append(defs, pstypes.ProcessDefinition{Name: p})
		}
		welcomeOpts.CloseProcesses = defs
	}

	err = session.ShowInstallationWelcome(welcomeOpts)
	result.DurationMS = time.Since(start).Milliseconds()

	if err != nil {
		result.Message = fmt.Sprintf("ShowInstallationWelcome: %v", err)
		if strings.Contains(strings.ToLower(err.Error()), "defer") {
			result.Action = "deferred"
		} else {
			result.Action = "error"
		}
		a.logs.append("[psadt] welcome dialog falhou: " + result.Message)
		return result
	}

	result.Success = true
	result.Action = "displayed"
	result.Message = "Welcome dialog exibido com sucesso"
	a.logs.append("[psadt] welcome dialog concluido com sucesso")
	return result
}

// RunPSADTRestartPrompt executa o restart prompt via go-psadt.
func (a *App) RunPSADTRestartPrompt(countdownSeconds int, silentRestart bool) PSADTRestartPromptResult {
	result := PSADTRestartPromptResult{ExecutedAtUTC: time.Now().UTC().Format(time.RFC3339)}
	if countdownSeconds <= 0 {
		countdownSeconds = 300
	}

	a.logs.append(fmt.Sprintf("[psadt] restart prompt: countdown=%d silent=%t", countdownSeconds, silentRestart))

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(countdownSeconds+120)*time.Second)
	defer cancel()

	client, err := psadt.NewClient(psadt.WithTimeout(time.Duration(countdownSeconds+120) * time.Second))
	if err != nil {
		result.Message = fmt.Sprintf("psadt.NewClient: %v", err)
		return result
	}
	defer client.Close()

	session, err := client.OpenSessionWithContext(ctx, pstypes.SessionConfig{
		AppVendor:      "Discovery",
		AppName:        "Discovery Agent",
		AppVersion:     "1.0",
		DeploymentType: pstypes.DeployInstall,
		DeployMode:     pstypes.DeployModeInteractive,
	})
	if err != nil {
		result.Message = fmt.Sprintf("OpenSession: %v", err)
		return result
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		_ = session.CloseWithContext(closeCtx, 0)
	}()

	start := time.Now()
	err = session.ShowInstallationRestartPrompt(pstypes.RestartPromptOptions{
		CountdownSeconds: countdownSeconds,
		SilentRestart:    silentRestart,
	})
	result.DurationMS = time.Since(start).Milliseconds()

	if err != nil {
		result.Message = fmt.Sprintf("ShowInstallationRestartPrompt: %v", err)
		result.Action = "error"
		a.logs.append("[psadt] restart prompt falhou: " + result.Message)
		return result
	}

	result.Success = true
	result.Action = "restart"
	result.Message = "Restart prompt exibido com sucesso"
	a.logs.append("[psadt] restart prompt concluido")
	return result
}

// GetPSADTSessionProperties obtem propriedades da sessao ativa via Get-ADTSession.
func (a *App) GetPSADTSessionProperties() PSADTSessionProperties {
	result := PSADTSessionProperties{CheckedAtUTC: time.Now().UTC().Format(time.RFC3339)}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := psadt.NewClient(psadt.WithTimeout(30 * time.Second))
	if err != nil {
		result.Error = fmt.Sprintf("psadt.NewClient: %v", err)
		return result
	}
	defer client.Close()

	session, err := client.OpenSessionWithContext(ctx, pstypes.SessionConfig{
		AppVendor:      "Discovery",
		AppName:        "Discovery Agent",
		AppVersion:     "1.0",
		DeploymentType: pstypes.DeployInstall,
		DeployMode:     pstypes.DeployModeSilent,
	})
	if err != nil {
		result.Error = fmt.Sprintf("OpenSession: %v", err)
		return result
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		_ = session.CloseWithContext(closeCtx, 0)
	}()

	props, err := session.GetSession()
	if err != nil {
		result.Error = fmt.Sprintf("GetSession: %v", err)
		return result
	}

	result.Success = true
	result.AppName = props.AppName
	result.AppVendor = props.AppVendor
	result.AppVersion = props.AppVersion
	result.DeploymentType = props.DeploymentType
	result.DeployMode = props.DeployMode
	result.LogPath = props.LogPath
	result.LogName = props.LogName
	result.InstallPhase = props.InstallPhase
	return result
}

func parseProcessList(commaSep string) []string {
	raw := strings.Split(commaSep, ",")
	var out []string
	for _, r := range raw {
		trimmed := strings.TrimSpace(r)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
