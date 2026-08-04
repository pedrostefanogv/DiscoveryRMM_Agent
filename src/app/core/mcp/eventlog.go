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

// allowedEventLogNames is the hard-coded set of Windows Event Log names that
// the agent is permitted to query. "Security" is intentionally excluded —
// it contains authentication and logon events that would expose sensitive
// user data to the LLM without an RBAC gate.
var allowedEventLogNames = map[string]bool{
	"System":      true,
	"Application": true,
	"Setup":       true,
}

// allowedEventLevels maps the string level names accepted from callers to the
// numeric values used in the PowerShell -FilterHashtable. Using an enum
// prevents any injection via the level parameter.
var allowedEventLevels = map[string]int{
	"Critical":    1,
	"Error":       2,
	"Warning":     3,
	"Information": 4,
}

const (
	eventLogMaxEvents    = 100 // hard cap — prevents DoS / huge LLM context
	eventLogMaxHours     = 168 // 7 days
	eventLogMinHours     = 1
	eventLogDefaultMax   = 50
	eventLogDefaultHours = 24
	eventLogTimeout      = 30 * time.Second
)

// EventLogEntry is the structured result for a single event log entry.
type EventLogEntry struct {
	TimeCreated  string `json:"timeCreated"`
	EventID      int64  `json:"eventId"`
	Level        string `json:"level"`
	ProviderName string `json:"providerName"`
	LogName      string `json:"logName"`
	Message      string `json:"message"`
}

// eventlogPSLiteral wraps a string in PowerShell single-quote literals,
// escaping embedded single quotes by doubling them. This is the same
// technique used in internal/printer/manager.go and prevents PS injection.
func eventlogPSLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// clampInt returns v clamped to [min, max].
func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// QueryEventLog queries a Windows Event Log with the given filters.
// logName must be one of: System, Application, Setup.
// level must be one of: Critical, Error, Warning, Information — empty means all levels.
// source is an optional event source/provider filter (empty = no filter).
// maxEvents is capped at eventLogMaxEvents.
// lastHours is clamped to [1, 168].
func QueryEventLog(ctx context.Context, logName, level, source string, maxEvents, lastHours int) (json.RawMessage, error) {
	if runtime.GOOS != "windows" {
		return nil, fmt.Errorf("consulta de Event Log suportada apenas no Windows")
	}

	// Validate logName against allowlist.
	logName = strings.TrimSpace(logName)
	if logName == "" {
		logName = "System"
	}
	if !allowedEventLogNames[logName] {
		return nil, fmt.Errorf("logName invalido: %q — valores aceitos: System, Application, Setup", logName)
	}

	// Validate level; empty = all levels (no filter).
	level = strings.TrimSpace(level)
	if level != "" {
		if _, ok := allowedEventLevels[level]; !ok {
			return nil, fmt.Errorf("level invalido: %q — valores aceitos: Critical, Error, Warning, Information", level)
		}
	}

	// Sanitize source — empty means no filter.
	source = strings.TrimSpace(source)

	// Apply server-side caps.
	if maxEvents <= 0 {
		maxEvents = eventLogDefaultMax
	}
	maxEvents = clampInt(maxEvents, 1, eventLogMaxEvents)

	if lastHours <= 0 {
		lastHours = eventLogDefaultHours
	}
	lastHours = clampInt(lastHours, eventLogMinHours, eventLogMaxHours)

	// Build the -FilterHashtable. We use a Go string builder so that user
	// inputs (source) are always inserted via psLiteral, never raw.
	var filterParts []string
	filterParts = append(filterParts, "LogName="+eventlogPSLiteral(logName))

	if level != "" {
		// Level is already validated against the allowlist — safe to use as integer.
		filterParts = append(filterParts, fmt.Sprintf("Level=%d", allowedEventLevels[level]))
	}

	startTimeMS := lastHours * 3600 * 1000
	filterParts = append(filterParts, fmt.Sprintf("StartTime=(Get-Date).AddMilliseconds(-%d)", startTimeMS))

	if source != "" {
		filterParts = append(filterParts, "ProviderName="+eventlogPSLiteral(source))
	}

	filterHashtable := "@{" + strings.Join(filterParts, "; ") + "}"

	// Build the final PowerShell script. -Newest is applied after filtering
	// to guarantee the cap is respected even when Get-WinEvent returns early.
	script := fmt.Sprintf(`$ErrorActionPreference = 'Stop'
$filter = %s
$events = @(Get-WinEvent -FilterHashtable $filter -MaxEvents %d -ErrorAction SilentlyContinue |
  Select-Object -First %d |
  ForEach-Object {
    $msg = ''
    try { $msg = $_.FormatDescription() } catch { $msg = $_.Message }
    if (-not $msg) { $msg = '' }
    [PSCustomObject]@{
      timeCreated  = $_.TimeCreated.ToString('o')
      eventId      = [int64]$_.Id
      level        = $_.LevelDisplayName
      providerName = $_.ProviderName
      logName      = $_.LogName
      message      = ($msg -replace '\r?\n', ' ').Trim()
    }
  })
$events | ConvertTo-Json -Depth 4 -Compress`, filterHashtable, maxEvents, maxEvents)

	return runEventLogScript(ctx, script, eventLogTimeout)
}

// GetRecentErrors is a convenience shortcut that returns Critical and Error
// events from both System and Application logs for the last lastHours hours.
func GetRecentErrors(ctx context.Context, lastHours int) (json.RawMessage, error) {
	if runtime.GOOS != "windows" {
		return nil, fmt.Errorf("consulta de Event Log suportada apenas no Windows")
	}

	if lastHours <= 0 {
		lastHours = eventLogDefaultHours
	}
	lastHours = clampInt(lastHours, eventLogMinHours, eventLogMaxHours)

	startTimeMS := lastHours * 3600 * 1000

	// Static script — no user input interpolated.
	script := fmt.Sprintf(`$ErrorActionPreference = 'Stop'
$startTime = (Get-Date).AddMilliseconds(-%d)
$logs = @('System','Application')
$results = @()
foreach ($log in $logs) {
  $filter = @{ LogName=$log; Level=1,2; StartTime=$startTime }
  $evts = @(Get-WinEvent -FilterHashtable $filter -MaxEvents 50 -ErrorAction SilentlyContinue |
    ForEach-Object {
      $msg = ''
      try { $msg = $_.FormatDescription() } catch { $msg = $_.Message }
      if (-not $msg) { $msg = '' }
      [PSCustomObject]@{
        timeCreated  = $_.TimeCreated.ToString('o')
        eventId      = [int64]$_.Id
        level        = $_.LevelDisplayName
        providerName = $_.ProviderName
        logName      = $_.LogName
        message      = ($msg -replace '\r?\n', ' ').Trim()
      }
    })
  $results += $evts
}
$results = $results | Sort-Object timeCreated -Descending | Select-Object -First %d
$results | ConvertTo-Json -Depth 4 -Compress`, startTimeMS, eventLogMaxEvents)

	return runEventLogScript(ctx, script, eventLogTimeout)
}

// GetRecentCrashes returns BSOD, kernel power failures and application crash
// events. The Event IDs are fixed constants — no user input is involved.
//
//	ID 41   (Kernel-Power)     — unexpected restart / BSOD
//	ID 6008 (EventLog)         — unexpected shutdown
//	ID 1001 (WER)              — Windows Error Reporting post-crash
//	ID 1000 (Application log)  — faulting application crash
func GetRecentCrashes(ctx context.Context, lastHours int) (json.RawMessage, error) {
	if runtime.GOOS != "windows" {
		return nil, fmt.Errorf("consulta de Event Log suportada apenas no Windows")
	}

	if lastHours <= 0 {
		lastHours = eventLogMaxHours // default 7 days for crash history
	}
	lastHours = clampInt(lastHours, eventLogMinHours, eventLogMaxHours)

	startTimeMS := lastHours * 3600 * 1000

	// Fully static script — Event IDs are hard-coded integers, not user input.
	script := fmt.Sprintf(`$ErrorActionPreference = 'Stop'
$startTime = (Get-Date).AddMilliseconds(-%d)
$results = @()

# Kernel power failures and BSODs (System log)
$f1 = @{ LogName='System'; Id=41,6008; StartTime=$startTime }
$results += @(Get-WinEvent -FilterHashtable $f1 -MaxEvents 50 -ErrorAction SilentlyContinue)

# Windows Error Reporting (Application log)
$f2 = @{ LogName='Application'; Id=1001,1000; StartTime=$startTime }
$results += @(Get-WinEvent -FilterHashtable $f2 -MaxEvents 50 -ErrorAction SilentlyContinue)

$results = $results |
  Sort-Object TimeCreated -Descending |
  Select-Object -First 50 |
  ForEach-Object {
    $msg = ''
    try { $msg = $_.FormatDescription() } catch { $msg = $_.Message }
    if (-not $msg) { $msg = '' }
    [PSCustomObject]@{
      timeCreated  = $_.TimeCreated.ToString('o')
      eventId      = [int64]$_.Id
      level        = $_.LevelDisplayName
      providerName = $_.ProviderName
      logName      = $_.LogName
      message      = ($msg -replace '\r?\n', ' ').Trim()
    }
  }
$results | ConvertTo-Json -Depth 4 -Compress`, startTimeMS)

	return runEventLogScript(ctx, script, eventLogTimeout)
}

// runEventLogScript executes a PowerShell script with the standard agent flags
// and returns its stdout as a JSON raw message.
func runEventLogScript(ctx context.Context, script string, timeout time.Duration) (json.RawMessage, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "powershell",
		"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-Command", script)
	processutil.HideWindow(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("erro ao consultar Event Log: %w | saida: %s",
			err, strings.TrimSpace(string(output)))
	}

	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" || trimmed == "null" {
		return json.RawMessage("[]"), nil
	}

	// Ensure the output is valid JSON before returning.
	if !json.Valid([]byte(trimmed)) {
		return nil, fmt.Errorf("saida inesperada do Event Log (nao-JSON): %s",
			truncate(trimmed, 200))
	}

	return json.RawMessage(trimmed), nil
}

// truncate returns s truncated to maxLen bytes with "..." appended if needed.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
