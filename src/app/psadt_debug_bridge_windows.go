//go:build windows

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	psadt "github.com/pedrostefanogv/go-psadt"
	pstypes "github.com/pedrostefanogv/go-psadt/types"
)

// extractPSADTDataField extrai o campo "Data" do envelope JSON
// {Success, Data, Error} retornado pela lib go-psadt. Se o campo Data
// for uma string JSON aninhada, ela é desembrulhada. Retorna nil em erro.
func extractPSADTDataField(raw []byte) []byte {
	if len(raw) == 0 {
		return nil
	}
	var envelope struct {
		Success bool            `json:"Success"`
		Data    json.RawMessage `json:"Data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		// Se não for um envelope (ex.: já é o JSON direto), retorna como está.
		return raw
	}
	if !envelope.Success || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil
	}
	// Se Data for uma string JSON aninhada (ex.: `"{\"isAdmin\":...}"`),
	// desembrulha uma vez para obter o objeto interno.
	var s string
	if err := json.Unmarshal(envelope.Data, &s); err == nil {
		return []byte(s)
	}
	return envelope.Data
}

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

	session, err := client.OpenSessionWithContext(ctx, pstypes.NewSessionConfig().
		App("Discovery", "Discovery Agent", "1.0").
		Install().
		Silent().
		Build())
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

	// Executa todas as verificações em um único round-trip via ExecuteRawScript,
	// reduzindo a latência de 4 chamadas sequenciais para 1.
	// O script emite um JSON estruturado com todos os resultados, que chega
	// dentro do envelope {Success, Data, Error} da lib.
	raw, rawErr := session.ExecuteRawScript(ctx, `
$r = [ordered]@{
  isAdmin = [bool](Test-ADTCallerIsAdmin)
  reboot  = [bool]((Get-ADTPendingReboot).IsSystemRebootPending)
  online  = [bool](Test-ADTNetworkConnection)
  focus   = [bool](Test-ADTUserInFocusMode)
}
$r | ConvertTo-Json -Compress
`)
	if rawErr == nil {
		var parsed struct {
			IsAdmin bool `json:"isAdmin"`
			Reboot  bool `json:"reboot"`
			Online  bool `json:"online"`
			Focus   bool `json:"focus"`
		}
		// Extrai o campo Data do envelope {Success, Data, Error} da lib e
		// re-parseia o JSON interno emitido pelo script PS.
		if data := extractPSADTDataField(raw); data != nil {
			rawErr = json.Unmarshal(data, &parsed)
		} else {
			rawErr = fmt.Errorf("campo Data ausente no envelope PSADT")
		}
		if rawErr == nil {
			result.IsAdmin = parsed.IsAdmin
			result.RebootPending = parsed.Reboot
			result.NetworkAvailable = parsed.Online
			result.UserInFocusMode = parsed.Focus
		} else {
			a.logs.append("[psadt] preflight [AVISO] falha ao parsear batch: " + rawErr.Error())
		}
	} else {
		a.logs.append("[psadt] preflight [AVISO] batch falhou, tentando individual: " + rawErr.Error())
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
	}

	result.Success = true
	a.logs.append(fmt.Sprintf("[psadt] preflight concluído: os=%s %s admin=%t reboot=%t net=%t focus=%t",
		result.OSName, result.OSVersion, result.IsAdmin, result.RebootPending, result.NetworkAvailable, result.UserInFocusMode))
	return result
}
