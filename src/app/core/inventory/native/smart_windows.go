//go:build windows

package native

import (
	"context"
	"encoding/json"
	"log"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"discovery/app/core/processutil"
)

// SmartHealth holds basic disk health/SMART data collected from Windows.
// All fields are optional — when the OS/driver does not expose a value it is
// left zero/empty and the caller decides how to render it.
type SmartHealth struct {
	// HealthStatus from Get-PhysicalDisk: "Healthy" | "Warning" | "Unhealthy" | "".
	HealthStatus string `json:"healthStatus"`
	// TemperatureC in Celsius (0 when unknown).
	TemperatureC int `json:"temperatureC"`
	// PowerOnHours total hours the disk has been powered on.
	PowerOnHours int `json:"powerOnHours"`
	// Wear is the SSD wear percentage (0-100, 0 when unknown).
	Wear int `json:"wear"`
	// ReadErrorsTotal cumulative read errors.
	ReadErrorsTotal int `json:"readErrorsTotal"`
	// WriteErrorsTotal cumulative write errors.
	WriteErrorsTotal int `json:"writeErrorsTotal"`
}

// collectSmartHealth queries disk health/SMART data via PowerShell
// (Get-PhysicalDisk + Get-StorageReliabilityCounter) and returns a map keyed
// by drive letter (e.g. "C:") so it can be merged into logical volumes,
// mirroring the media-type mapping pattern. Returns nil on any failure (the
// caller degrades gracefully).
func collectSmartHealth() map[string]SmartHealth {
	if runtime.GOOS != "windows" {
		return nil
	}

	script := `$ErrorActionPreference = 'SilentlyContinue'
$result = @(Get-PhysicalDisk | ForEach-Object {
    $disk = $_
    $rel = $disk | Get-StorageReliabilityCounter
    Get-Disk -Number $disk.DeviceId | Get-Partition |
        Where-Object { $_.DriveLetter } |
        ForEach-Object {
            [PSCustomObject]@{
                DriveLetter      = $_.DriveLetter + ':'
                HealthStatus     = $disk.HealthStatus
                TemperatureC     = if ($rel) { $rel.Temperature } else { $null }
                PowerOnHours     = if ($rel) { $rel.PowerOnHours } else { $null }
                Wear             = if ($rel) { $rel.Wear } else { $null }
                ReadErrorsTotal  = if ($rel) { $rel.ReadErrorsTotal } else { $null }
                WriteErrorsTotal = if ($rel) { $rel.WriteErrorsTotal } else { $null }
            }
        }
})
if ($result.Count -eq 0) { '[]' } else { $result | ConvertTo-Json -Depth 2 -Compress }`

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "powershell",
		"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-Command", script)
	processutil.HideWindow(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[inventory] falha ao consultar saude SMART dos discos: %v", err)
		return nil
	}

	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" || trimmed == "[]" || trimmed == "null" {
		return nil
	}

	type rawDisk struct {
		DriveLetter      any `json:"DriveLetter"`
		HealthStatus     any `json:"HealthStatus"`
		TemperatureC     any `json:"TemperatureC"`
		PowerOnHours     any `json:"PowerOnHours"`
		Wear             any `json:"Wear"`
		ReadErrorsTotal  any `json:"ReadErrorsTotal"`
		WriteErrorsTotal any `json:"WriteErrorsTotal"`
	}

	var raw []rawDisk
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		log.Printf("[inventory] falha ao parsear saida SMART: %v", err)
		return nil
	}

	result := make(map[string]SmartHealth, len(raw))
	for _, r := range raw {
		letter := strings.ToUpper(strings.TrimSpace(wmiString(map[string]any{"v": r.DriveLetter}, "v")))
		letter = strings.TrimRight(letter, `\`)
		if len(letter) >= 2 && letter[1] == ':' {
			letter = letter[:2]
		}
		if letter == "" {
			continue
		}
		result[letter] = SmartHealth{
			HealthStatus:     wmiString(map[string]any{"v": r.HealthStatus}, "v"),
			TemperatureC:     wmiInt(map[string]any{"v": r.TemperatureC}, "v"),
			PowerOnHours:     wmiInt(map[string]any{"v": r.PowerOnHours}, "v"),
			Wear:             wmiInt(map[string]any{"v": r.Wear}, "v"),
			ReadErrorsTotal:  wmiInt(map[string]any{"v": r.ReadErrorsTotal}, "v"),
			WriteErrorsTotal: wmiInt(map[string]any{"v": r.WriteErrorsTotal}, "v"),
		}
	}

	return result
}
