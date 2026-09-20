//go:build windows

package native

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"discovery/app/core/models"
	"discovery/app/core/processutil"
)

// monitorRowString lê um campo string tolerante a tipos/ausência.
func monitorRowString(row map[string]any, key string) string {
	v, ok := row[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(fmt.Sprintf("%v", v))
}

// collectMonitorsNative enumerates attached monitors via WmiMonitorID
// (root\wmi) through PowerShell. Os arrays uint16 do WMI (nomes em UTF-16
// terminados em zero) não são decodificáveis baratamente via go-ole COM — o
// PowerShell resolve (mesmo padrão do coletor de SMART). Best-effort: falha
// loga e devolve nil.
func collectMonitorsNative(ctx context.Context) ([]models.MonitorInfo, error) {
	script := `$ErrorActionPreference = 'SilentlyContinue'
$result = @(Get-CimInstance -Namespace root\wmi -ClassName WmiMonitorID | ForEach-Object {
    $join = { param($arr) if ($arr) { ($arr | Where-Object { $_ -ne 0 } | ForEach-Object { [char]$_ }) -join '' } else { '' } }
    [PSCustomObject]@{
        name         = & $join $_.UserFriendlyName
        manufacturer = & $join $_.ManufacturerName
        serial       = & $join $_.SerialNumberID
        instance     = $_.InstanceName
        status       = if ($_.Active) { 'Active' } else { 'Inactive' }
    }
})
if ($result.Count -eq 0) { '[]' } else { $result | ConvertTo-Json -Depth 2 -Compress }`

	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "powershell",
		"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-Command", script)
	processutil.HideWindow(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}

	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" || trimmed == "[]" || trimmed == "null" {
		return nil, nil
	}

	// Pode vir objeto único (um monitor só) ou array — aceitar ambos.
	var rows []map[string]any
	if err := json.Unmarshal([]byte(trimmed), &rows); err != nil {
		var single map[string]any
		if err2 := json.Unmarshal([]byte(trimmed), &single); err2 != nil {
			return nil, err2
		}
		rows = []map[string]any{single}
	}

	return mapMonitorRows(rows), nil
}

// monitorInstanceShort resume o InstanceName (ex.: DISPLAY\AOC2401\5&…) para
// uso como identificador de fallback quando o UserFriendlyName está vazio.
// O segmento útil é o que segue "DISPLAY\" (ID PnP do modelo); o sufixo
// final "4&2c0d…" é instância por máquina e varia.
func monitorInstanceShort(instance string) string {
	instance = strings.TrimSpace(instance)
	if instance == "" {
		return ""
	}
	parts := strings.Split(strings.TrimSuffix(instance, "\\"), "\\")
	if len(parts) >= 2 && strings.EqualFold(parts[0], "DISPLAY") && parts[1] != "" {
		return parts[1]
	}
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return ""
}

// mapMonitorRows mapeia as linhas WmiMonitorID para models.MonitorInfo.
func mapMonitorRows(rows []map[string]any) []models.MonitorInfo {
	if len(rows) == 0 {
		return nil
	}

	const maxMonitors = 16
	items := make([]models.MonitorInfo, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))

	for _, row := range rows {
		name := strings.TrimSpace(monitorRowString(row, "name"))
		manufacturer := strings.TrimSpace(monitorRowString(row, "manufacturer"))
		serial := strings.TrimSpace(monitorRowString(row, "serial"))
		instance := strings.TrimSpace(monitorRowString(row, "instance"))

		if name == "" && manufacturer == "" && serial == "" && instance == "" {
			continue
		}
		if name == "" {
			if short := monitorInstanceShort(instance); short != "" {
				name = "Monitor (" + short + ")"
			} else {
				name = "Monitor"
			}
		}

		key := name + "|" + serial
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		items = append(items, models.MonitorInfo{
			Name:         name,
			Manufacturer: manufacturer,
			Serial:       serial,
			Status:       strings.TrimSpace(monitorRowString(row, "status")),
		})

		if len(items) >= maxMonitors {
			break
		}
	}
	if len(items) == 0 {
		return nil
	}
	return items
}
