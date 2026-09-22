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

// collectScheduledTasksNative enumera as tarefas agendadas do Windows via
// PowerShell (Get-ScheduledTask + Get-ScheduledTaskInfo). Best-effort: em
// caso de falha devolve lista vazia e nil (não propaga erro, para não
// quebrar a coleta completa do inventário).
func collectScheduledTasksNative(ctx context.Context) ([]models.ScheduledTaskInfo, error) {
	runCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "powershell",
		"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-Command", scheduledTasksScript)
	processutil.HideWindow(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		// PowerShell bloqueado por política/AV ou Task Scheduler indisponível:
		// best-effort, não propaga erro.
		return nil, nil
	}

	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" || trimmed == "[]" || trimmed == "null" {
		return nil, nil
	}

	var rows []map[string]any
	if err := json.Unmarshal([]byte(trimmed), &rows); err != nil {
		var single map[string]any
		if err2 := json.Unmarshal([]byte(trimmed), &single); err2 != nil {
			return nil, nil
		}
		rows = []map[string]any{single}
	}

	return mapScheduledTaskRows(rows), nil
}

// scheduledTasksScript coleta TODAS as tarefas do Task Scheduler com o
// gatilho principal (tipo + descrição amigável) e o runtime (Status,
// NextRunTime, LastRunTime, LastTaskResult). Saída em JSON compacto.
const scheduledTasksScript = `$ErrorActionPreference = 'SilentlyContinue'
$result = @(Get-ScheduledTask | ForEach-Object {
    $t = $_
    $a = $t.Actions | Select-Object -First 1
    $tr = $t.Triggers | Select-Object -First 1
    $triggerType = 'other'
    $triggerDesc = ''
    if ($tr) {
        $cls = $tr.CimClass.CimClassName
        switch -Wildcard ($cls) {
            'MSFT_TaskBootTrigger'   { $triggerType = 'boot';   $triggerDesc = 'Na inicialização do sistema' }
            'MSFT_TaskLogonTrigger'  { $triggerType = 'logon';  $triggerDesc = 'Ao fazer logon'; if ($tr.UserId) { $triggerDesc = "Ao fazer logon ($($tr.UserId))" } }
            'MSFT_TaskDailyTrigger'  { $triggerType = 'daily';  $triggerDesc = 'Diário'; if ($tr.StartTime) { $triggerDesc = "Diário às $($tr.StartTime.ToString('HH:mm'))" } }
            'MSFT_TaskWeeklyTrigger' { $triggerType = 'weekly'; $triggerDesc = 'Semanal' }
            'MSFT_TaskTimeTrigger'   { $triggerType = 'once';   $triggerDesc = 'Uma vez'; if ($tr.StartTime) { $triggerDesc = "Uma vez em $($tr.StartTime.ToString('yyyy-MM-dd HH:mm'))" } }
            'MSFT_TaskIdleTrigger'   { $triggerType = 'idle';   $triggerDesc = 'Quando o computador estiver ocioso' }
            'MSFT_TaskEventTrigger'  { $triggerType = 'event';  $triggerDesc = 'Por evento' }
            default                  { $triggerType = 'other';  if ($cls) { $triggerDesc = [string]$cls } }
        }
        if ($triggerType -eq 'weekly' -and $tr.DaysOfWeek) {
            $names = @()
            $flags = [int]$tr.DaysOfWeek
            $map = @{ 1 = 'Dom'; 2 = 'Seg'; 4 = 'Ter'; 8 = 'Qua'; 16 = 'Qui'; 32 = 'Sex'; 64 = 'Sáb' }
            foreach ($k in $map.Keys) { if ($flags -band [int]$k) { $names += $map[$k] } }
            if ($names.Count -gt 0) { $triggerDesc = "Semanal ($($names -join ','))" }
            if ($tr.StartTime) { $triggerDesc = "$triggerDesc às $($tr.StartTime.ToString('HH:mm'))" }
        }
        if ($triggerType -eq 'daily' -and $tr.DaysInterval -gt 1) { $triggerDesc = "A cada $($tr.DaysInterval) dias" }
    }
    $info = $t | Get-ScheduledTaskInfo
    $state = [string]$t.State
    if ($state -eq 'Disabled') { $state = 'disabled' } else { $state = 'enabled' }
    $next = ''; if ($info.NextRunTime) { $next = $info.NextRunTime.ToString('yyyy-MM-ddTHH:mm:ssZ') }
    $last = ''; if ($info.LastRunTime) { $last = $info.LastRunTime.ToString('yyyy-MM-ddTHH:mm:ssZ') }
    [PSCustomObject]@{
        taskPath    = $t.TaskPath
        taskName    = $t.TaskName
        state       = $state
        status      = [string]$info.Status
        author      = [string]$t.Author
        actionPath  = [string]$a.Execute
        actionArgs  = [string]$a.Arguments
        triggerType = $triggerType
        triggerDesc = [string]$triggerDesc
        nextRun     = $next
        lastRun     = $last
        lastResult  = [int64]$info.LastTaskResult
    }
})
if ($result.Count -eq 0) { '[]' } else { $result | ConvertTo-Json -Depth 3 -Compress }`

func mapScheduledTaskRows(rows []map[string]any) []models.ScheduledTaskInfo {
	tasks := make([]models.ScheduledTaskInfo, 0, len(rows))
	for _, row := range rows {
		taskPath := strings.TrimSpace(scheduledTaskString(row, "taskPath"))
		taskName := strings.TrimSpace(scheduledTaskString(row, "taskName"))
		if taskName == "" {
			continue
		}
		tasks = append(tasks, models.ScheduledTaskInfo{
			TaskPath:    taskPath,
			TaskName:    taskName,
			State:       normalizeScheduledTaskState(scheduledTaskString(row, "state")),
			Status:      strings.TrimSpace(scheduledTaskString(row, "status")),
			Author:      strings.TrimSpace(scheduledTaskString(row, "author")),
			ActionPath:  strings.TrimSpace(scheduledTaskString(row, "actionPath")),
			ActionArgs:  strings.TrimSpace(scheduledTaskString(row, "actionArgs")),
			TriggerType: strings.TrimSpace(scheduledTaskString(row, "triggerType")),
			TriggerDesc: strings.TrimSpace(scheduledTaskString(row, "triggerDesc")),
			NextRunTime: strings.TrimSpace(scheduledTaskString(row, "nextRun")),
			LastRunTime: strings.TrimSpace(scheduledTaskString(row, "lastRun")),
			LastResult:  scheduledTaskInt64(row, "lastResult"),
		})
	}
	return tasks
}

func scheduledTaskString(row map[string]any, key string) string {
	v, ok := row[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return strings.TrimSpace(fmt.Sprintf("%v", v))
}

func scheduledTaskInt64(row map[string]any, key string) int64 {
	switch v := row[key].(type) {
	case float64:
		return int64(v)
	case json.Number:
		n, err := v.Int64()
		if err == nil {
			return n
		}
	}
	return 0
}

func normalizeScheduledTaskState(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "disabled":
		return "disabled"
	default:
		return "enabled"
	}
}
