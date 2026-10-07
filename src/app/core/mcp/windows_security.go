package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// --- Windows Update -------------------------------------------------------

const windowsUpdateInstalledScript = `$ErrorActionPreference = 'Stop'
@(Get-CimInstance Win32_QuickFixEngineering |
  Select-Object HotFixID, Description, InstalledBy, InstalledOn, Caption) |
  ConvertTo-Json -Compress -Depth 4`

const pendingRebootScript = `$ErrorActionPreference = 'SilentlyContinue'
$cbs = Test-Path 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending'
$wu  = Test-Path 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\RebootRequired'
[PSCustomObject]@{
  cbsRebootPending            = [bool]$cbs
  windowsUpdateRebootRequired = [bool]$wu
  pendingReboot               = [bool]($cbs -or $wu)
} | ConvertTo-Json -Compress -Depth 4`

const windowsUpdateHistoryScript = `$ErrorActionPreference = 'SilentlyContinue'
$events = @(Get-WinEvent -LogName Setup -MaxEvents 50 | ForEach-Object {
  [PSCustomObject]@{
    timeCreated  = $_.TimeCreated.ToString('o')
    eventId      = [int64]$_.Id
    level        = $_.LevelDisplayName
    providerName = $_.ProviderName
    message      = (($_.Message) -replace '\r?\n', ' ').Trim()
  }
})
if ($events.Count -eq 0) {
  $events = @(Get-CimInstance Win32_QuickFixEngineering |
    Sort-Object InstalledOn -Descending |
    Select-Object -First 50 |
    ForEach-Object {
      [PSCustomObject]@{
        timeCreated  = if ($_.InstalledOn) { ([datetime]$_.InstalledOn).ToString('o') } else { $null }
        eventId      = 0
        level        = 'Information'
        providerName = 'Win32_QuickFixEngineering'
        message      = ($_.HotFixID + ' ' + $_.Description)
      }
    })
}
$events | ConvertTo-Json -Compress -Depth 4`

// registerWindowsUpdateTool registra a tool "windows_update".
func registerWindowsUpdateTool(reg *Registry) {
	reg.Register(Tool{
		Name: "windows_update",
		Description: "Atualizacoes do Windows (familia action-based). action: " +
			"installed (hotfixes instalados) | pending_reboot (reinicio pendente no registro) | " +
			"history (ultimos 50 eventos do log Setup; fallback para os hotfixes).",
		Params: []ToolParam{
			{Name: "action", Type: "string", Description: "Acao: installed, pending_reboot, history", Required: true},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			action, err := requireAction(args, "installed", "pending_reboot", "history")
			if err != nil {
				return nil, err
			}
			switch action {
			case "installed":
				return runPowerShell(ctx, windowsUpdateInstalledScript, 60*time.Second), nil
			case "pending_reboot":
				return runPowerShell(ctx, pendingRebootScript, 30*time.Second), nil
			case "history":
				return runPowerShell(ctx, windowsUpdateHistoryScript, 90*time.Second), nil
			}
			return nil, fmt.Errorf("action nao suportada: %s", action)
		},
	})
}

// --- Seguranca ------------------------------------------------------------

const defenderStatusScript = `$ErrorActionPreference = 'Stop'
Get-MpComputerStatus | ConvertTo-Json -Compress -Depth 4`

const firewallStatusScript = `$ErrorActionPreference = 'Stop'
@(Get-NetFirewallProfile |
  Select-Object Name, Enabled, DefaultInboundAction, DefaultOutboundAction, AllowInboundRules, AllowOutboundRules, NotifyOnListen) |
  ConvertTo-Json -Compress -Depth 4`

const tpmStatusScript = `$ErrorActionPreference = 'Stop'
Get-Tpm | ConvertTo-Json -Compress -Depth 4`

// registerSecurityStatusTool registra a tool "security_status".
func registerSecurityStatusTool(reg *Registry) {
	reg.Register(Tool{
		Name: "security_status",
		Description: "Seguranca do Windows (familia action-based). action: " +
			"summary (agrega defender + firewall + tpm, com erro isolado por bloco) | " +
			"defender (Get-MpComputerStatus) | firewall (Get-NetFirewallProfile) | tpm (Get-Tpm).",
		Params: []ToolParam{
			{Name: "action", Type: "string", Description: "Acao: summary, defender, firewall, tpm", Required: true},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			action, err := requireAction(args, "summary", "defender", "firewall", "tpm")
			if err != nil {
				return nil, err
			}
			switch action {
			case "defender":
				return runPowerShell(ctx, defenderStatusScript, 60*time.Second), nil
			case "firewall":
				return runPowerShell(ctx, firewallStatusScript, 60*time.Second), nil
			case "tpm":
				return runPowerShell(ctx, tpmStatusScript, 60*time.Second), nil
			case "summary":
				return securityStatusSummary(ctx), nil
			}
			return nil, fmt.Errorf("action nao suportada: %s", action)
		},
	})
}

// securityStatusSummary roda cada bloco de forma independente: a falha de um
// (ex.: Get-Tpm indisponivel) nunca derruba os demais.
func securityStatusSummary(ctx context.Context) map[string]any {
	blocks := []struct {
		key    string
		script string
	}{
		{"defender", defenderStatusScript},
		{"firewall", firewallStatusScript},
		{"tpm", tpmStatusScript},
	}

	// Os 3 blocos são independentes e cada PowerShell leva segundos: rodar em
	// paralelo reduz o tempo de 3 spawns em série para o do mais lento.
	summary := map[string]any{}
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, block := range blocks {
		wg.Add(1)
		go func(key, script string) {
			defer wg.Done()

			raw := runPowerShell(ctx, script, 60*time.Second)
			var value any
			if errMsg, hasErr := rawHasError(raw); hasErr {
				value = map[string]any{"error": errMsg}
			} else if err := json.Unmarshal(raw, &value); err != nil {
				value = map[string]any{"error": "saida inesperada do PowerShell: " + truncate(string(raw), 200)}
			}

			mu.Lock()
			summary[key] = value
			mu.Unlock()
		}(block.key, block.script)
	}

	wg.Wait()
	return summary
}
