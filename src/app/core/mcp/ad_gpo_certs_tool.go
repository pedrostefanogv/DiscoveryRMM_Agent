package mcp

import (
	"context"
	"fmt"
	"time"
)

const adGpoStatusScript = `$ErrorActionPreference = 'SilentlyContinue'
$cs = Get-CimInstance Win32_ComputerSystem
$dc = $null
if ($cs.PartOfDomain) {
  $nl = (& nltest /dsgetdc:$($cs.Domain) 2>$null) -join [Environment]::NewLine
  if ($nl -match 'DC:\s*\\\\([^\s\r\n]+)') { $dc = $Matches[1] }
}
$dsreg = (& dsregcmd /status 2>$null) -join [Environment]::NewLine
$azureJoined = ($dsreg -match 'AzureAdJoined\s*:\s*YES')
$domainJoined = ($dsreg -match 'DomainJoined\s*:\s*YES') -or [bool]$cs.PartOfDomain
[PSCustomObject]@{
  partOfDomain   = [bool]$cs.PartOfDomain
  domain         = $cs.Domain
  workgroup      = $cs.Workgroup
  domainRole     = [int]$cs.DomainRole
  domainController = $dc
  domainJoined   = [bool]$domainJoined
  azureAdJoined  = [bool]$azureJoined
} | ConvertTo-Json -Compress -Depth 3`

const bitlockerStatusScript = `$ErrorActionPreference = 'SilentlyContinue'
$vols = @(Get-BitLockerVolume | ForEach-Object {
  [PSCustomObject]@{
    mountPoint           = $_.MountPoint
    volumeType           = [string]$_.VolumeType
    protectionStatus     = [string]$_.ProtectionStatus
    volumeStatus         = [string]$_.VolumeStatus
    encryptionPercentage = [int]$_.EncryptionPercentage
    lockStatus           = [string]$_.LockStatus
  }
})
$vols | ConvertTo-Json -Compress -Depth 3`

const userSessionsScript = `$ErrorActionPreference = 'SilentlyContinue'
$raw = & quser 2>$null
if ($LASTEXITCODE -eq 0 -and $raw) {
  $lines = @($raw | Select-Object -Skip 1 | ForEach-Object { $_.Trim() } | Where-Object { $_ })
  [PSCustomObject]@{ source = 'quser'; sessions = $lines } | ConvertTo-Json -Compress -Depth 3
} else {
  $cs = Get-CimInstance Win32_ComputerSystem
  [PSCustomObject]@{ source = 'Win32_ComputerSystem'; userName = $cs.UserName } | ConvertTo-Json -Compress -Depth 3
}`

func buildCertsScript(daysThreshold int) string {
	return fmt.Sprintf(`$ErrorActionPreference = 'SilentlyContinue'
$now = Get-Date
$limit = $now.AddDays(%d)
$certs = @(Get-ChildItem Cert:\LocalMachine\My |
  Where-Object { $_.NotAfter -le $limit } |
  Sort-Object NotAfter |
  Select-Object -First 30 |
  ForEach-Object {
    [PSCustomObject]@{
      subject       = $_.Subject
      issuer        = $_.Issuer
      thumbprint    = $_.Thumbprint
      notAfter      = $_.NotAfter.ToString('o')
      daysRemaining = [int][math]::Floor(($_.NotAfter - $now).TotalDays)
      expired       = ($_.NotAfter -lt $now)
    }
  })
$certs | ConvertTo-Json -Compress -Depth 3`, daysThreshold)
}

// registerAdGpoCertsTool registra a tool "ad_gpo_certs" (somente leitura) para
// diagnóstico corporativo de AD, certificados locais, BitLocker e sessões.
func registerAdGpoCertsTool(reg *Registry) {
	reg.Register(Tool{
		Name: "ad_gpo_certs",
		Description: "Diagnostico corporativo (somente leitura, familia action-based). action: " +
			"ad (dominio, DC ativo e status Azure AD/Entra) | " +
			"certs (certificados em LocalMachine\\My expirados ou proximos de expirar; nunca exporta chave privada) | " +
			"bitlocker (status de criptografia por volume; nunca expoe recovery key) | " +
			"sessions (sessoes de usuarios logados).",
		Params: []ToolParam{
			{Name: "action", Type: "string", Description: "Acao: ad, certs, bitlocker, sessions", Required: true},
			{Name: "daysThreshold", Type: "integer", Description: "Dias para expiracao em certs (padrao 60, 1..3650)", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			action, err := requireAction(args, "ad", "certs", "bitlocker", "sessions")
			if err != nil {
				return nil, err
			}
			switch action {
			case "ad":
				return runPowerShell(ctx, adGpoStatusScript, 45*time.Second), nil
			case "certs":
				days := optionalIntArg(args, "daysThreshold")
				if days <= 0 {
					days = 60
				}
				if days > 3650 {
					days = 3650
				}
				return runPowerShell(ctx, buildCertsScript(days), 45*time.Second), nil
			case "bitlocker":
				return runPowerShell(ctx, bitlockerStatusScript, 45*time.Second), nil
			case "sessions":
				return runPowerShell(ctx, userSessionsScript, 30*time.Second), nil
			}
			return nil, fmt.Errorf("action nao suportada: %s", action)
		},
	})
}
