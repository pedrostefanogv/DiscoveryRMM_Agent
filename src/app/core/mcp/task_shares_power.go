package mcp

import (
	"context"
	"fmt"
	"time"
)

// O atraso/cancelamento do power_action vive no app (PSADT + aviso nativo):
// ver RunPowerAction em app/mcp_power_action.go.

// --- Tarefas agendadas ----------------------------------------------------

const scheduledTasksScript = `$ErrorActionPreference = 'SilentlyContinue'
$tasks = @(Get-ScheduledTask | ForEach-Object {
  $info = $null
  try {
    $info = Get-ScheduledTaskInfo -TaskName $_.TaskName -TaskPath $_.TaskPath -ErrorAction SilentlyContinue
  } catch { $info = $null }
  [PSCustomObject]@{
    name           = $_.TaskName
    path           = $_.TaskPath
    state          = [string]$_.State
    nextRunTime    = if ($info -and $info.NextRunTime) { $info.NextRunTime.ToString('o') } else { $null }
    lastRunTime    = if ($info -and $info.LastRunTime) { $info.LastRunTime.ToString('o') } else { $null }
    lastTaskResult = if ($info) { [int64]$info.LastTaskResult } else { 0 }
  }
})
$tasks | ConvertTo-Json -Compress -Depth 4`

// registerScheduledTaskTool registra a tool "scheduled_task".
func registerScheduledTaskTool(reg *Registry) {
	reg.Register(Tool{
		Name: "scheduled_task",
		Description: "Tarefas agendadas do Windows (familia action-based). action: " +
			"list (nome, caminho, estado e proxima execucao) | " +
			"run (executa por taskName/taskPath; DESTRUTIVA: exige confirm=true).",
		Params: []ToolParam{
			{Name: "action", Type: "string", Description: "Acao: list, run", Required: true},
			{Name: "taskName", Type: "string", Description: "Nome da tarefa (run)", Required: false},
			{Name: "taskPath", Type: "string", Description: "Caminho da tarefa (ex: \\Microsoft\\Windows\\); padrao \\(run)", Required: false},
			{Name: "confirm", Type: "boolean", Description: "Confirmacao explicita do usuario (run)", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			action, err := requireAction(args, "list", "run")
			if err != nil {
				return nil, err
			}
			switch action {
			case "list":
				return runPowerShell(ctx, scheduledTasksScript, 90*time.Second), nil
			case "run":
				if err := requireConfirm(args); err != nil {
					return nil, err
				}
				taskName, err := requiredStringArg(args, "taskName")
				if err != nil {
					return nil, err
				}
				taskPath := optionalStringArg(args, "taskPath")
				if taskPath == "" {
					taskPath = "\\"
				}
				script := "$ErrorActionPreference = 'Stop'\n" +
					"Start-ScheduledTask -TaskName " + eventlogPSLiteral(taskName) +
					" -TaskPath " + eventlogPSLiteral(taskPath)
				raw := runPowerShell(ctx, script, 60*time.Second)
				if errMsg, hasErr := rawHasError(raw); hasErr {
					return nil, fmt.Errorf("falha ao executar a tarefa %q: %s", taskName, errMsg)
				}
				return map[string]any{"ok": true, "taskName": taskName, "taskPath": taskPath}, nil
			}
			return nil, fmt.Errorf("action nao suportada: %s", action)
		},
	})
}

// --- Compartilhamentos ----------------------------------------------------

const sharesScript = `$ErrorActionPreference = 'Stop'
@(Get-SmbShare | Select-Object Name, Path, Description, ShareType, CurrentUsers, EncryptData) |
  ConvertTo-Json -Compress -Depth 4`

const mappedDrivesScript = `$ErrorActionPreference = 'Stop'
@(Get-SmbMapping | Select-Object LocalPath, RemotePath, Status, IsPersistent) |
  ConvertTo-Json -Compress -Depth 4`

// registerSharesTool registra a tool "shares".
func registerSharesTool(reg *Registry) {
	reg.Register(Tool{
		Name: "shares",
		Description: "Compartilhamentos (familia action-based). action: " +
			"shares (Get-SmbShare: pastas compartilhadas) | " +
			"mapped_drives (Get-SmbMapping: unidades de rede mapeadas).",
		Params: []ToolParam{
			{Name: "action", Type: "string", Description: "Acao: shares, mapped_drives", Required: true},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			action, err := requireAction(args, "shares", "mapped_drives")
			if err != nil {
				return nil, err
			}
			switch action {
			case "shares":
				return runPowerShell(ctx, sharesScript, 60*time.Second), nil
			case "mapped_drives":
				return runPowerShell(ctx, mappedDrivesScript, 60*time.Second), nil
			}
			return nil, fmt.Errorf("action nao suportada: %s", action)
		},
	})
}

// --- Energia / sessao -----------------------------------------------------

// registerPowerActionTool registra a tool "power_action". TODAS as acoes
// (restart, shutdown, lock) sao destrutivas e exigem confirm=true.
func registerPowerActionTool(reg *Registry, app AppBridge) {
	reg.Register(Tool{
		Name: "power_action",
		Description: "Energia/sessao do computador (familia action-based). action: " +
			"restart | shutdown | lock. TODAS DESTRUTIVAS: exigem confirm=true apos aprovacao do usuario. " +
			"restart/shutdown exibem um AVISO AO USUARIO com contador (padrao 30s, minimo 10s) e botao CANCELAR " +
			"(PSADT; fallback no aviso nativo do Windows) — se o usuario cancelar, NADA e executado. " +
			"Informe sempre o atraso e que ele pode cancelar. " +
			"IMPORTANTE: restart/shutdown ENCERRAM a conversa no ato (a maquina reinicia/desliga em seguida): " +
			"na ultima mensagem diga apenas o que foi agendado e como cancelar, sem prometer acoes futuras " +
			"nem pedir nova confirmacao. lock e imediato.",
		Params: []ToolParam{
			{Name: "action", Type: "string", Description: "Acao: restart, shutdown, lock", Required: true},
			{Name: "confirm", Type: "boolean", Description: "Confirmacao explicita do usuario", Required: false},
			{Name: "delaySeconds", Type: "integer", Description: "Segundos de contagem antes de reiniciar/desligar (minimo 10, maximo 300; padrao 30)", Required: false},
			{Name: "message", Type: "string", Description: "Mensagem exibida ao usuario no aviso (explique o motivo do reinicio/desligamento)", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			action, err := requireAction(args, "restart", "shutdown", "lock")
			if err != nil {
				return nil, err
			}
			if err := requireConfirm(args); err != nil {
				return nil, err
			}
			if ctx == nil {
				ctx = context.Background()
			}
			return app.RunPowerAction(ctx, action, optionalIntArg(args, "delaySeconds"), optionalStringArg(args, "message"))
		},
	})
}
