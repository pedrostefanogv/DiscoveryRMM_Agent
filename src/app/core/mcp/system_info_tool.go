package mcp

import (
	"context"
	"encoding/json"
	"time"
)

// systemInfoScript coleta uptime, boot time, hostname, dominio/workgroup,
// versao do SO e arquitetura. Script estatico (sem entrada do usuario).
const systemInfoScript = `$ErrorActionPreference = 'Stop'
$os = Get-CimInstance Win32_OperatingSystem
$cs = Get-CimInstance Win32_ComputerSystem
$boot = $os.LastBootUpTime
[PSCustomObject]@{
  hostname      = $env:COMPUTERNAME
  osCaption     = $os.Caption
  osVersion     = $os.Version
  osBuild       = $os.BuildNumber
  architecture  = $os.OSArchitecture
  bootTime      = if ($boot) { $boot.ToString('o') } else { $null }
  uptimeSeconds = if ($boot) { [int64]((Get-Date) - $boot).TotalSeconds } else { 0 }
  domain        = $cs.Domain
  workgroup     = $cs.Workgroup
  manufacturer  = $cs.Manufacturer
  model         = $cs.Model
} | ConvertTo-Json -Compress -Depth 4`

// registerSystemInfoTool registra a tool "system_info". Combina
// sysctrl.GetSystemInfo (RAM/CPU) com o PowerShell (SO/uptime/host).
func registerSystemInfoTool(reg *Registry) {
	reg.Register(Tool{
		Name: "system_info",
		Description: "Resumo do sistema: uptime, boot time, hostname, dominio/workgroup, " +
			"versao e arquitetura do SO, fabricante/modelo e uso de RAM/CPU.",
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			info := map[string]any{}
			raw := runPowerShell(ctx, systemInfoScript, 60*time.Second)
			if errMsg, hasErr := rawHasError(raw); hasErr {
				info["error"] = errMsg
			} else if err := json.Unmarshal(raw, &info); err != nil {
				info["error"] = "saida inesperada do PowerShell: " + truncate(string(raw), 200)
			}
			if extra := systemInfoNative(); extra != nil {
				for k, v := range extra {
					info[k] = v
				}
			}
			return info, nil
		},
	})
}
