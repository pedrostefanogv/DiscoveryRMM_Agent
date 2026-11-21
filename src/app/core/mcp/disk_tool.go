package mcp

import (
	"context"
	"fmt"
	"time"
)

// volumeUsageScript usa Get-Volume (espaco por volume). Script estatico.
const volumeUsageScript = `$ErrorActionPreference = 'Stop'
$vols = @(Get-Volume | Where-Object { $_.DriveLetter } | ForEach-Object {
  [PSCustomObject]@{
    driveLetter     = [string]$_.DriveLetter
    fileSystemLabel = $_.FileSystemLabel
    fileSystem      = $_.FileSystem
    driveType       = [string]$_.DriveType
    healthStatus    = [string]$_.HealthStatus
    sizeGB          = [math]::Round($_.Size / 1GB, 2)
    freeGB          = [math]::Round($_.SizeRemaining / 1GB, 2)
    usedGB          = [math]::Round(($_.Size - $_.SizeRemaining) / 1GB, 2)
    usePercent      = if ($_.Size -gt 0) { [math]::Round(($_.Size - $_.SizeRemaining) / $_.Size * 100, 1) } else { 0 }
  }
})
$vols | ConvertTo-Json -Compress -Depth 4`

// tempSizeScript mede o tamanho de %TEMP% e C:\Windows\Temp em MB.
const tempSizeScript = `$ErrorActionPreference = 'SilentlyContinue'
$paths = @($env:TEMP, 'C:\Windows\Temp')
$result = foreach ($p in $paths) {
  if ($p -and (Test-Path -LiteralPath $p)) {
    $sum = (Get-ChildItem -LiteralPath $p -Recurse -Force -ErrorAction SilentlyContinue | Measure-Object -Property Length -Sum).Sum
    [PSCustomObject]@{ path = $p; sizeMB = [math]::Round($sum / 1MB, 2) }
  }
}
$result | ConvertTo-Json -Compress -Depth 4`

const cleanupTempScript = `$ErrorActionPreference = 'SilentlyContinue'
$cutoff = (Get-Date).AddHours(-24)
$paths = @($env:TEMP, 'C:\Windows\Temp') | Select-Object -Unique
$removed = 0
$freedBytes = [int64]0
foreach ($p in $paths) {
  if ($p -and (Test-Path -LiteralPath $p)) {
    Get-ChildItem -LiteralPath $p -File -Recurse -Force -ErrorAction SilentlyContinue |
      Where-Object { $_.LastWriteTime -lt $cutoff } |
      ForEach-Object {
        $len = [int64]$_.Length
        try {
          Remove-Item -LiteralPath $_.FullName -Force -ErrorAction Stop
          $removed++
          $freedBytes += $len
        } catch {}
      }
  }
}
[PSCustomObject]@{
  removedFiles = $removed
  freedMB      = [math]::Round($freedBytes / 1MB, 2)
  paths        = $paths
} | ConvertTo-Json -Compress -Depth 3`

// registerDiskTool registra a tool "disk": health, usage, temp e cleanup_temp.
func registerDiskTool(reg *Registry) {
	reg.Register(Tool{
		Name: "disk",
		Description: "Discos e volumes (familia action-based). action: " +
			"health (status WMI dos discos fisicos: OK / Pred Fail) | " +
			"usage (espaco usado/livre por volume) | " +
			"temp (tamanho de %TEMP% e C:\\Windows\\Temp em MB) | " +
			"cleanup_temp (remove arquivos temporarios com mais de 24h; exige consentimento).",
		Params: []ToolParam{
			{Name: "action", Type: "string", Description: "Acao: health, usage, temp, cleanup_temp", Required: true},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			action, err := requireAction(args, "health", "usage", "temp", "cleanup_temp")
			if err != nil {
				return nil, err
			}
			switch action {
			case "health":
				return GetDiskHealth(ctx)
			case "usage":
				return runPowerShell(ctx, volumeUsageScript, 60*time.Second), nil
			case "temp":
				return runPowerShell(ctx, tempSizeScript, 120*time.Second), nil
			case "cleanup_temp":
				if err := requireConfirm(args); err != nil {
					return nil, err
				}
				return runPowerShell(ctx, cleanupTempScript, 120*time.Second), nil
			}
			return nil, fmt.Errorf("action nao suportada: %s", action)
		},
	})
}
