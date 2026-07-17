//go:build windows

package app

import (
	"context"
	"fmt"
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
	a.logs.append(fmt.Sprintf("[psadt] preflight concluído: os=%s %s admin=%t reboot=%t net=%t focus=%t",
		result.OSName, result.OSVersion, result.IsAdmin, result.RebootPending, result.NetworkAvailable, result.UserInFocusMode))
	return result
}
