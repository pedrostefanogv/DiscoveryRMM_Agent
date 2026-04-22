package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"discovery/internal/processutil"
)

const (
	perfSnapshotTimeout = 20 * time.Second
	perfDiskTimeout     = 30 * time.Second
	perfProcessTimeout  = 15 * time.Second

	perfTopProcessesMax     = 50
	perfTopProcessesDefault = 10
)

// allowedOrderBy is the set of valid values for the orderBy parameter in
// get_top_processes. The actual PowerShell property name is resolved in Go —
// the user-supplied string is never interpolated into the script.
var allowedOrderBy = map[string]string{
	"cpu":    "CPU",
	"memory": "WorkingSet64",
}

// GetPerformanceSnapshot returns an instantaneous snapshot of CPU, memory and
// disk utilization. The script is fully static — no user input is accepted.
func GetPerformanceSnapshot(ctx context.Context) (json.RawMessage, error) {
	if runtime.GOOS != "windows" {
		return nil, fmt.Errorf("snapshot de desempenho suportado apenas no Windows")
	}

	// All values come from WMI/CIM classes — no user input whatsoever.
	script := `$ErrorActionPreference = 'Stop'

$os   = Get-CimInstance Win32_OperatingSystem
$cpu  = Get-CimInstance Win32_Processor | Measure-Object -Property LoadPercentage -Average
$disks = @(Get-CimInstance Win32_LogicalDisk | Where-Object { $_.DriveType -eq 3 } |
  ForEach-Object {
    $usedGB  = if ($_.Size -gt 0) { [math]::Round(($_.Size - $_.FreeSpace) / 1GB, 2) } else { 0 }
    $freeGB  = if ($_.FreeSpace -ge 0) { [math]::Round($_.FreeSpace / 1GB, 2) } else { -1 }
    $totalGB = if ($_.Size -gt 0) { [math]::Round($_.Size / 1GB, 2) } else { 0 }
    $usePct  = if ($_.Size -gt 0) { [math]::Round(($_.Size - $_.FreeSpace) / $_.Size * 100, 1) } else { 0 }
    [PSCustomObject]@{
      drive        = $_.DeviceID
      label        = $_.VolumeName
      totalGB      = $totalGB
      usedGB       = $usedGB
      freeGB       = $freeGB
      usePercent   = $usePct
    }
  })

$totalMemGB  = [math]::Round($os.TotalVisibleMemorySize / 1MB, 2)
$freeMemGB   = [math]::Round($os.FreePhysicalMemory / 1MB, 2)
$usedMemGB   = [math]::Round(($os.TotalVisibleMemorySize - $os.FreePhysicalMemory) / 1MB, 2)
$memUsePct   = if ($os.TotalVisibleMemorySize -gt 0) {
  [math]::Round(($os.TotalVisibleMemorySize - $os.FreePhysicalMemory) / $os.TotalVisibleMemorySize * 100, 1)
} else { 0 }

[PSCustomObject]@{
  timestamp        = (Get-Date).ToString('o')
  cpuLoadPercent   = [math]::Round($cpu.Average, 1)
  memory = [PSCustomObject]@{
    totalGB      = $totalMemGB
    usedGB       = $usedMemGB
    freeGB       = $freeMemGB
    usePercent   = $memUsePct
  }
  disks = $disks
} | ConvertTo-Json -Depth 5 -Compress`

	return runPerfScript(ctx, script, perfSnapshotTimeout)
}

// GetTopProcesses returns the top N processes ordered by CPU seconds or
// WorkingSet memory. The orderBy parameter is validated against an allowlist —
// the corresponding PowerShell property name is chosen in Go, not interpolated
// from user input. CommandLine is intentionally excluded to avoid leaking
// credentials or tokens that processes may receive as arguments.
func GetTopProcesses(ctx context.Context, top int, orderBy string) (json.RawMessage, error) {
	if runtime.GOOS != "windows" {
		return nil, fmt.Errorf("listagem de processos suportada apenas no Windows")
	}

	// Validate and clamp top.
	if top <= 0 {
		top = perfTopProcessesDefault
	}
	top = clampInt(top, 1, perfTopProcessesMax)

	// Validate orderBy against the allowlist; the PS property is chosen in Go.
	orderBy = strings.ToLower(strings.TrimSpace(orderBy))
	if orderBy == "" {
		orderBy = "cpu"
	}
	psProperty, ok := allowedOrderBy[orderBy]
	if !ok {
		return nil, fmt.Errorf("orderBy invalido: %q — valores aceitos: cpu, memory", orderBy)
	}

	// The script uses the safe psProperty string (one of two hard-coded values)
	// and the integer top — no free-form user input is interpolated.
	script := fmt.Sprintf(`$ErrorActionPreference = 'Stop'
$procs = @(Get-Process -ErrorAction SilentlyContinue |
  Sort-Object %s -Descending |
  Select-Object -First %d |
  ForEach-Object {
    $memMB = [math]::Round($_.WorkingSet64 / 1MB, 2)
    [PSCustomObject]@{
      name         = $_.ProcessName
      pid          = $_.Id
      cpuSeconds   = [math]::Round($_.CPU, 2)
      memoryMB     = $memMB
      description  = $_.Description
      startTime    = if ($_.StartTime) { $_.StartTime.ToString('o') } else { $null }
    }
  })
$procs | ConvertTo-Json -Depth 4 -Compress`, psProperty, top)

	return runPerfScript(ctx, script, perfProcessTimeout)
}

// GetDiskHealth returns the WMI-reported health status for all physical disks.
// True S.M.A.R.T. values (temperature, reallocated sectors, power-on hours)
// require vendor-specific WMI providers or external tools; this function uses
// the Win32_DiskDrive.Status field ("OK", "Pred Fail", "Unknown") which is
// available on all Windows installations without additional software.
// The script is fully static — no user input.
func GetDiskHealth(ctx context.Context) (json.RawMessage, error) {
	if runtime.GOOS != "windows" {
		return nil, fmt.Errorf("status de disco suportado apenas no Windows")
	}

	script := `$ErrorActionPreference = 'Stop'
$disks = @(Get-CimInstance Win32_DiskDrive |
  ForEach-Object {
    $sizeGB = if ($_.Size -gt 0) { [math]::Round($_.Size / 1GB, 2) } else { 0 }
    [PSCustomObject]@{
      index          = [int]$_.Index
      model          = $_.Model
      manufacturer   = $_.Manufacturer
      serialNumber   = $_.SerialNumber
      interfaceType  = $_.InterfaceType
      mediaType      = $_.MediaType
      sizeGB         = $sizeGB
      partitions     = [int]$_.Partitions
      status         = $_.Status
      statusInfo     = $_.StatusInfo
      firmware       = $_.FirmwareRevision
    }
  })
$disks | ConvertTo-Json -Depth 4 -Compress`

	return runPerfScript(ctx, script, perfDiskTimeout)
}

// runPerfScript executes a performance-monitoring PowerShell script using the
// standard agent flags and returns its stdout as a validated JSON raw message.
func runPerfScript(ctx context.Context, script string, timeout time.Duration) (json.RawMessage, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "powershell",
		"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-Command", script)
	processutil.HideWindow(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("erro ao coletar dados de desempenho: %w | saida: %s",
			err, strings.TrimSpace(string(output)))
	}

	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" || trimmed == "null" {
		return json.RawMessage("{}"), nil
	}

	if !json.Valid([]byte(trimmed)) {
		return nil, fmt.Errorf("saida inesperada do PowerShell (nao-JSON): %s",
			truncate(trimmed, 200))
	}

	return json.RawMessage(trimmed), nil
}
