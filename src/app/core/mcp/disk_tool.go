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

// registerDiskTool registra a tool "disk": health, usage e temp. Substitui a
// antiga get_disk_health.
func registerDiskTool(reg *Registry) {
	reg.Register(Tool{
		Name: "disk",
		Description: "Discos e volumes (familia action-based). action: " +
			"health (status WMI dos discos fisicos: OK / Pred Fail) | " +
			"usage (espaco usado/livre por volume) | " +
			"temp (tamanho de %TEMP% e C:\\Windows\\Temp em MB).",
		Params: []ToolParam{
			{Name: "action", Type: "string", Description: "Acao: health, usage, temp", Required: true},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			action, err := requireAction(args, "health", "usage", "temp")
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
			}
			return nil, fmt.Errorf("action nao suportada: %s", action)
		},
	})
}
