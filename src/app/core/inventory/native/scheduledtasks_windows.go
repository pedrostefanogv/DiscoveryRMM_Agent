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

$dayMap = @{
    'Sunday' = 'Dom'; 'Monday' = 'Seg'; 'Tuesday' = 'Ter'; 'Wednesday' = 'Qua';
    'Thursday' = 'Qui'; 'Friday' = 'Sex'; 'Saturday' = 'Sab'
}
$dayFlags = @(
    @{ Bit = 1; Name = 'Dom' }, @{ Bit = 2; Name = 'Seg' }, @{ Bit = 4; Name = 'Ter' },
    @{ Bit = 8; Name = 'Qua' }, @{ Bit = 16; Name = 'Qui' }, @{ Bit = 32; Name = 'Sex' },
    @{ Bit = 64; Name = 'Sab' }
)

$result = @(Get-ScheduledTask | ForEach-Object {
    $t = $_
    try {
        $a = $t.Actions | Select-Object -First 1
        $tr = $t.Triggers | Select-Object -First 1

        $triggerType = 'none'
        $start = ''
        $days = ''
        $daysInterval = 0

        if ($tr) {
            $start = [string]$tr.StartBoundary
            $days = [string]$tr.DaysOfWeek
            $daysInterval = [int]$tr.DaysInterval

            switch ([string]$tr.CimClass.CimClassName) {
                'MSFT_TaskBootTrigger'   { $triggerType = 'boot' }
                'MSFT_TaskLogonTrigger'  { $triggerType = 'logon' }
                'MSFT_TaskDailyTrigger'  { $triggerType = 'daily' }
                'MSFT_TaskWeeklyTrigger' { $triggerType = 'weekly' }
                'MSFT_TaskTimeTrigger'   { $triggerType = 'once' }
                'MSFT_TaskIdleTrigger'   { $triggerType = 'idle' }
                'MSFT_TaskEventTrigger'  { $triggerType = 'event' }
                default                  { $triggerType = 'other' }
            }

            if ($triggerType -eq 'other') {
                $node = $null
                $el = ''
                $xml = ''
                try { $xml = Export-ScheduledTask -TaskPath $t.TaskPath -TaskName $t.TaskName } catch { $xml = '' }
                if ($xml) {
                    try {
                        $doc = [xml]$xml
                        $node = $doc.Task.Triggers.ChildNodes | Select-Object -First 1
                        if ($node) { $el = [string]$node.LocalName }
                    } catch {
                        $node = $null
                    }
                }

                switch ($el) {
                    'BootTrigger'               { $triggerType = 'boot' }
                    'LogonTrigger'              { $triggerType = 'logon' }
                    'RegistrationTrigger'       { $triggerType = 'registration' }
                    'SessionStateChangeTrigger' { $triggerType = 'session' }
                    'IdleTrigger'               { $triggerType = 'idle' }
                    'EventTrigger'              { $triggerType = 'event' }
                    'TimeTrigger'               { $triggerType = 'once' }
                    'CalendarTrigger' {
                        if ($node.ScheduleByWeek) { $triggerType = 'weekly' }
                        elseif ($node.ScheduleByMonth) { $triggerType = 'monthly' }
                        elseif ($node.ScheduleByDay) { $triggerType = 'daily' }
                        else { $triggerType = 'calendar' }
                    }
                    'WnfStateChangeTrigger'     { $triggerType = 'wnf' }
                    ''                          { $triggerType = 'none' }
                    default                     { $triggerType = 'custom' }
                }

                if ($node) {
                    if (-not $start -and $node.StartBoundary) { $start = [string]$node.StartBoundary }
                    if ($daysInterval -le 1 -and $node.ScheduleByDay.DaysInterval) {
                        $daysInterval = [int]$node.ScheduleByDay.DaysInterval
                    }
                }
                # Dias do XML cru: o adapter XML do PowerShell não expõe os
                # filhos de <DaysOfWeek>, então lemos o bloco como texto.
                if (-not $days -and $xml) {
                    $dm = [regex]::Match($xml, '(?s)<DaysOfWeek>(.*?)</DaysOfWeek>')
                    if ($dm.Success) { $days = $dm.Groups[1].Value }
                }
            }
        }

        $triggerDesc = 'Outro'
        switch ($triggerType) {
            'boot'         { $triggerDesc = 'Na inicializacao do sistema' }
            'logon'        { $triggerDesc = 'Ao fazer logon'; if ($tr -and $tr.UserId) { $triggerDesc = "Ao fazer logon ($($tr.UserId))" } }
            'daily'        { $triggerDesc = 'Diario' }
            'weekly'       { $triggerDesc = 'Semanal' }
            'once'         { $triggerDesc = 'Uma vez' }
            'idle'         { $triggerDesc = 'Quando o computador estiver ocioso' }
            'event'        { $triggerDesc = 'Por evento' }
            'monthly'      { $triggerDesc = 'Mensal' }
            'calendar'     { $triggerDesc = 'Calendario' }
            'registration' { $triggerDesc = 'No registro da tarefa' }
            'session'      { $triggerDesc = 'Mudanca de estado da sessao' }
            'wnf'          { $triggerDesc = 'Mudanca de estado do sistema (WNF)' }
            'custom'       { $triggerDesc = 'Gatilho personalizado' }
            'none'         { $triggerDesc = 'Sem gatilho (execucao sob demanda)' }
            default        { $triggerDesc = 'Outro' }
        }

        $time = ''
        if ($start) {
            $dt = [datetime]::MinValue
            if ([datetime]::TryParse($start, [ref]$dt)) {
                if ($triggerType -eq 'once' -or $triggerType -eq 'monthly' -or $triggerType -eq 'calendar') {
                    $time = $dt.ToString('yyyy-MM-dd HH:mm')
                } else {
                    $time = $dt.ToString('HH:mm')
                }
            }
        }

        $dayNames = @()
        if ($days) {
            foreach ($en in @('Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday')) {
                if ($days -match $en) { $dayNames += $dayMap[$en] }
            }
            if ($dayNames.Count -eq 0) {
                $num = 0
                if ([int]::TryParse($days, [ref]$num)) {
                    foreach ($f in $dayFlags) { if ($num -band [int]$f.Bit) { $dayNames += $f.Name } }
                }
            }
        }

        if ($triggerType -eq 'weekly' -and $dayNames.Count -gt 0) {
            $triggerDesc = "Semanal ($($dayNames -join ', '))"
        }
        if ($triggerType -eq 'daily' -and $daysInterval -gt 1) {
            $triggerDesc = "A cada $daysInterval dias"
        }
        if ($time) {
            if ($triggerType -eq 'once') { $triggerDesc = "Uma vez em $time" }
            elseif ($triggerType -eq 'daily') { $triggerDesc = "$triggerDesc as $time" }
            elseif ($triggerType -eq 'weekly' -or $triggerType -eq 'monthly' -or $triggerType -eq 'calendar') {
                if ($triggerType -eq 'calendar') { $triggerDesc = "$triggerDesc as $time" }
            }
        }

        $info = $null
        try { $info = $t | Get-ScheduledTaskInfo } catch { $info = $null }
        $state = [string]$t.State
        if ($state -eq 'Disabled') { $state = 'disabled' } else { $state = 'enabled' }
        $next = ''; $last = ''; $lastResult = 0; $status = ''
        if ($info) {
            if ($info.NextRunTime) { $next = $info.NextRunTime.ToString('yyyy-MM-ddTHH:mm:ssZ') }
            if ($info.LastRunTime) { $last = $info.LastRunTime.ToString('yyyy-MM-ddTHH:mm:ssZ') }
            $status = [string]$info.Status
            $lastResult = [int64]$info.LastTaskResult
        }

        [PSCustomObject]@{
            taskPath    = $t.TaskPath
            taskName    = $t.TaskName
            state       = $state
            status      = $status
            author      = [string]$t.Author
            actionPath  = [string]$a.Execute
            actionArgs  = [string]$a.Arguments
            triggerType = $triggerType
            triggerDesc = [string]$triggerDesc
            nextRun     = $next
            lastRun     = $last
            lastResult  = $lastResult
        }
    } catch {
        # Tarefa problemática não interrompe a coleta das demais.
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
