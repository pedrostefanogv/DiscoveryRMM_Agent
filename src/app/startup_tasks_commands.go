// Package app — comandos de controle de inicialização e tarefas agendadas.
//
// Comandos aceitos (via NATS/WS → HandleCommand):
//   - "startupitem": habilita/desabilita um item de inicialização
//     (registro Run/RunOnce, pasta Startup ou serviço automático).
//   - "scheduledtask": habilita/desabilita/executa/exclui/edita uma tarefa
//     agendada do Task Scheduler.
//
// Após a ação bem-sucedida o agente re-coleta e re-envia startup items +
// tarefas agendadas para a API (sync parcial), para que o dashboard reflita
// o novo estado sem aguardar o próximo ciclo de inventário.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"discovery/app/agentcommands"
	"discovery/app/core/inventory/native"
)

// IsStartupItemCommandType verifica se o cmdType é de item de inicialização.
func IsStartupItemCommandType(cmdType string) bool {
	switch strings.ToLower(strings.TrimSpace(cmdType)) {
	case "startupitem", "startup_item", "startup-item":
		return true
	default:
		return false
	}
}

// IsScheduledTaskCommandType verifica se o cmdType é de tarefa agendada.
func IsScheduledTaskCommandType(cmdType string) bool {
	switch strings.ToLower(strings.TrimSpace(cmdType)) {
	case "scheduledtask", "scheduled_task", "scheduled-task":
		return true
	default:
		return false
	}
}

// handleStartupItemCommand processa um comando de habilitar/desabilitar item
// de inicialização. Payload esperado:
//
//	{"action":"enable|disable","type":"registry|folder|service",
//	 "name":"...","source":"HKLM Run","username":""}
func (a *App) handleStartupItemCommand(ctx context.Context, payload any) (bool, int, string, string) {
	payloadJSON, err := agentcommands.NormalizePayloadJSON(payload)
	if err != nil {
		return true, 2, "", "payload startupitem inválido: " + err.Error()
	}

	action := strings.ToLower(strings.TrimSpace(agentcommands.GetStringField(payloadJSON, "action")))
	if action != "enable" && action != "disable" {
		return true, 2, "", "ação startupitem inválida (use enable ou disable)"
	}
	name := strings.TrimSpace(agentcommands.GetStringField(payloadJSON, "name"))
	if name == "" {
		return true, 2, "", "campo 'name' é obrigatório"
	}
	target := native.StartupItemTarget{
		Type:   agentcommands.GetStringField(payloadJSON, "type"),
		Name:   name,
		Source: agentcommands.GetStringField(payloadJSON, "source"),
	}

	enable := action == "enable"
	a.Logs.Append(fmt.Sprintf("[agent] startupitem %s: type=%s name=%s source=%s",
		action, target.Type, target.Name, target.Source))

	if err := native.SetStartupItemEnabled(enable, target); err != nil {
		a.Logs.Append("[agent] startupitem falhou: " + err.Error())
		return true, 1, "", err.Error()
	}

	result := map[string]any{
		"success": true,
		"action":  action,
		"type":    target.Type,
		"name":    target.Name,
	}
	body, _ := json.Marshal(result)

	// Re-sync dos dados (best-effort) para refletir o novo estado no dashboard.
	if err := a.SyncStartupAndScheduledTasks(); err != nil {
		a.Logs.Append("[agent] aviso: re-sync pós-startupitem falhou: " + err.Error())
	}
	return true, 0, string(body), ""
}

// handleScheduledTaskCommand processa ações sobre tarefas agendadas.
// Payload esperado:
//
//	{"action":"enable|disable|run|delete|edit","taskPath":"\...","taskName":"...",
//	 "edit":{"triggerType":"daily|weekly|once|logon|boot","time":"HH:mm",
//	         "daysOfWeek":[0..6],"daysInterval":1,"actionPath":"","actionArgs":""}}
func (a *App) handleScheduledTaskCommand(ctx context.Context, payload any) (bool, int, string, string) {
	payloadJSON, err := agentcommands.NormalizePayloadJSON(payload)
	if err != nil {
		return true, 2, "", "payload scheduledtask inválido: " + err.Error()
	}

	action := strings.ToLower(strings.TrimSpace(agentcommands.GetStringField(payloadJSON, "action")))
	taskPath := agentcommands.GetStringField(payloadJSON, "taskPath")
	taskName := strings.TrimSpace(agentcommands.GetStringField(payloadJSON, "taskName"))
	if taskName == "" {
		return true, 2, "", "campo 'taskName' é obrigatório"
	}
	a.Logs.Append(fmt.Sprintf("[agent] scheduledtask %s: name=%s path=%s", action, taskName, taskPath))

	switch action {
	case "enable":
		if err := native.SetScheduledTaskState(ctx, true, taskPath, taskName); err != nil {
			return true, 1, "", err.Error()
		}
	case "disable":
		if err := native.SetScheduledTaskState(ctx, false, taskPath, taskName); err != nil {
			return true, 1, "", err.Error()
		}
	case "run":
		if err := native.RunScheduledTask(ctx, taskPath, taskName); err != nil {
			return true, 1, "", err.Error()
		}
	case "delete":
		if err := native.DeleteScheduledTask(ctx, taskPath, taskName); err != nil {
			return true, 1, "", err.Error()
		}
	case "edit":
		editPayload, ok := payloadJSON["edit"].(map[string]any)
		if !ok {
			return true, 2, "", "campo 'edit' é obrigatório para ação 'edit'"
		}
		edit := native.ScheduledTaskEdit{
			TriggerType:  agentcommands.GetStringField(editPayload, "triggerType"),
			Time:         agentcommands.GetStringField(editPayload, "time"),
			DaysOfWeek:   intSliceField(editPayload, "daysOfWeek"),
			DaysInterval: intField(editPayload, "daysInterval"),
			ActionPath:   agentcommands.GetStringField(editPayload, "actionPath"),
			ActionArgs:   agentcommands.GetStringField(editPayload, "actionArgs"),
		}
		if err := native.EditScheduledTask(ctx, taskPath, taskName, edit); err != nil {
			return true, 1, "", err.Error()
		}
	default:
		return true, 2, "", "ação scheduledtask inválida (use enable, disable, run, delete ou edit)"
	}

	result := map[string]any{
		"success":  true,
		"action":   action,
		"taskName": taskName,
		"taskPath": taskPath,
	}
	body, _ := json.Marshal(result)

	// Re-sync dos dados (best-effort) para refletir o novo estado no dashboard.
	if err := a.SyncStartupAndScheduledTasks(); err != nil {
		a.Logs.Append("[agent] aviso: re-sync pós-scheduledtask falhou: " + err.Error())
	}
	return true, 0, string(body), ""
}

// intSliceField extrai []int de um campo array do payload.
func intSliceField(m map[string]any, key string) []int {
	raw, ok := m[key]
	if !ok || raw == nil {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	result := make([]int, 0, len(arr))
	for _, v := range arr {
		switch t := v.(type) {
		case float64:
			result = append(result, int(t))
		case int:
			result = append(result, t)
		}
	}
	return result
}

// intField extrai um int tolerante a tipos de um map de payload.
func intField(m map[string]any, key string) int {
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	if s, ok := m[key].(string); ok {
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n); err == nil {
			return n
		}
	}
	return 0
}
