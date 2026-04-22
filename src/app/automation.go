package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"discovery/internal/automation"
)

func (a *App) GetAutomationState() AutomationStateView {
	if a.automationSvc == nil {
		return AutomationStateView{}
	}
	return mapAutomationState(a.automationSvc.GetState())
}

func (a *App) RefreshAutomationPolicy(includeScriptContent bool) (AutomationStateView, error) {
	if a.automationSvc == nil {
		return AutomationStateView{}, nil
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	state, err := a.automationSvc.RefreshPolicy(ctx, includeScriptContent)
	return mapAutomationState(state), err
}

func mapAutomationState(state automation.State) AutomationStateView {
	view := AutomationStateView{
		Available:            state.Available,
		Connected:            state.Connected,
		LoadedFromCache:      state.LoadedFromCache,
		UpToDate:             state.UpToDate,
		IncludeScriptContent: state.IncludeScriptContent,
		PolicyFingerprint:    state.PolicyFingerprint,
		GeneratedAt:          state.GeneratedAt,
		LastSyncAt:           state.LastSyncAt,
		LastAttemptAt:        state.LastAttemptAt,
		LastError:            state.LastError,
		CorrelationID:        state.CorrelationID,
		TaskCount:            state.TaskCount,
		PendingCallbacks:     state.PendingCallbacks,
		Tasks:                make([]AutomationTaskView, 0, len(state.Tasks)),
		RecentExecutions:     make([]AutomationExecutionView, 0, len(state.RecentExecutions)),
	}

	for _, task := range state.Tasks {
		item := AutomationTaskView{
			CommandID:             task.CommandID,
			TaskID:                task.TaskID,
			Name:                  task.Name,
			Description:           task.Description,
			ActionType:            string(task.ActionType),
			ActionLabel:           automationActionLabel(string(task.ActionType)),
			InstallationType:      string(task.InstallationType),
			InstallationLabel:     automationInstallationLabel(string(task.InstallationType)),
			PackageID:             task.PackageID,
			ScriptID:              task.ScriptID,
			CommandPayload:        task.CommandPayload,
			ScopeType:             string(task.ScopeType),
			ScopeLabel:            automationScopeLabel(string(task.ScopeType)),
			RequiresApproval:      task.RequiresApproval,
			TriggerImmediate:      task.TriggerImmediate,
			TriggerRecurring:      task.TriggerRecurring,
			TriggerOnUserLogin:    task.TriggerOnUserLogin,
			TriggerOnAgentCheckIn: task.TriggerOnAgentCheckIn,
			ScheduleCron:          task.ScheduleCron,
			IncludeTags:           append([]string(nil), task.IncludeTags...),
			ExcludeTags:           append([]string(nil), task.ExcludeTags...),
			LastUpdatedAt:         task.LastUpdatedAt,
		}
		if task.Script != nil {
			item.ScriptName = task.Script.Name
			item.ScriptVersion = task.Script.Version
			item.ScriptType = string(task.Script.ScriptType)
			item.ScriptTypeLabel = automationScriptLabel(string(task.Script.ScriptType))
		}
		view.Tasks = append(view.Tasks, item)
	}

	for _, execution := range state.RecentExecutions {
		item := AutomationExecutionView{
			ExecutionID:        execution.ExecutionID,
			CommandID:          execution.CommandID,
			TaskID:             execution.TaskID,
			TaskName:           execution.TaskName,
			ActionType:         string(execution.ActionType),
			ActionLabel:        automationActionLabel(string(execution.ActionType)),
			InstallationType:   string(execution.InstallationType),
			InstallationLabel:  automationInstallationLabel(string(execution.InstallationType)),
			SourceType:         string(execution.SourceType),
			SourceLabel:        automationSourceLabel(string(execution.SourceType)),
			TriggerType:        string(execution.TriggerType),
			TriggerLabel:       automationTriggerLabel(string(execution.TriggerType)),
			Status:             string(execution.Status),
			StatusLabel:        automationStatusLabel(string(execution.Status)),
			Success:            execution.Success,
			ExitCode:           execution.ExitCode,
			ExitCodeSet:        execution.ExitCodeSet,
			ErrorMessage:       execution.ErrorMessage,
			Output:             execution.Output,
			PackageID:          execution.PackageID,
			ScriptID:           execution.ScriptID,
			CorrelationID:      execution.CorrelationID,
			StartedAt:          execution.StartedAt,
			FinishedAt:         execution.FinishedAt,
			MetadataJSON:       execution.MetadataJSON,
			DurationLabel:      automationDurationLabel(execution.StartedAt, execution.FinishedAt),
			SummaryLine:        automationExecutionSummary(execution),
			HasPendingCallback: state.PendingCallbacks > 0 && strings.TrimSpace(execution.CommandID) != "" && (execution.Status == automation.ExecutionStatusDispatched || execution.Status == automation.ExecutionStatusAcknowledged || execution.Status == automation.ExecutionStatusCompleted || execution.Status == automation.ExecutionStatusFailed),
		}
		view.RecentExecutions = append(view.RecentExecutions, item)
	}

	return view
}

func automationScopeLabel(value string) string {
	switch strings.TrimSpace(value) {
	case "Global":
		return "Global"
	case "Client":
		return "Cliente"
	case "Site":
		return "Site"
	case "Agent":
		return "Agent"
	default:
		return value
	}
}

func automationInstallationLabel(value string) string {
	switch strings.TrimSpace(value) {
	case "Winget":
		return "Winget"
	case "Chocolatey":
		return "Chocolatey"
	case "Custom":
		return "Custom"
	default:
		return value
	}
}

func automationActionLabel(value string) string {
	switch strings.TrimSpace(value) {
	case "InstallPackage":
		return "Instalar pacote"
	case "UpdatePackage":
		return "Atualizar pacote"
	case "RemovePackage":
		return "Remover pacote"
	case "UpdateOrInstallPackage":
		return "Atualizar ou instalar"
	case "RunScript":
		return "Executar script"
	case "CustomCommand":
		return "Comando customizado"
	default:
		return value
	}
}

func automationScriptLabel(value string) string {
	switch strings.TrimSpace(value) {
	case "PowerShell":
		return "PowerShell"
	case "Shell":
		return "Shell"
	case "Python":
		return "Python"
	case "Batch":
		return "Batch"
	case "Custom":
		return "Custom"
	default:
		return value
	}
}

func automationSourceLabel(value string) string {
	switch strings.TrimSpace(value) {
	case "RunNow":
		return "Execucao imediata"
	case "Scheduled":
		return "Agendado"
	case "ForceSync":
		return "Force sync"
	case "AgentManual":
		return "Manual no agent"
	default:
		return value
	}
}

func automationTriggerLabel(value string) string {
	switch strings.TrimSpace(value) {
	case "Immediate":
		return "Immediate"
	case "Recurring":
		return "Recurring"
	case "UserLogin":
		return "UserLogin"
	case "AgentCheckIn":
		return "AgentCheckIn"
	case "Manual":
		return "Manual"
	default:
		return value
	}
}

func automationStatusLabel(value string) string {
	switch strings.TrimSpace(value) {
	case "Dispatched":
		return "Despachado"
	case "Acknowledged":
		return "ACK enviado"
	case "Completed":
		return "Concluido"
	case "Failed":
		return "Falhou"
	default:
		return value
	}
}

func automationDurationLabel(startedAt, finishedAt string) string {
	start, err := time.Parse(time.RFC3339, strings.TrimSpace(startedAt))
	if err != nil {
		return ""
	}
	end, err := time.Parse(time.RFC3339, strings.TrimSpace(finishedAt))
	if err != nil {
		return "Em andamento"
	}
	duration := end.Sub(start)
	if duration < time.Second {
		return "< 1s"
	}
	return duration.Round(time.Second).String()
}

func automationExecutionSummary(execution automation.ExecutionRecord) string {
	parts := make([]string, 0, 3)
	if action := automationActionLabel(string(execution.ActionType)); strings.TrimSpace(action) != "" {
		parts = append(parts, action)
	}
	if pkg := strings.TrimSpace(execution.PackageID); pkg != "" {
		parts = append(parts, pkg)
	} else if scriptID := strings.TrimSpace(execution.ScriptID); scriptID != "" {
		parts = append(parts, scriptID)
	}
	if len(parts) == 0 {
		return "Execucao registrada"
	}
	return fmt.Sprintf("%s", strings.Join(parts, " • "))
}
