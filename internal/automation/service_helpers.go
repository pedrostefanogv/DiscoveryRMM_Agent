package automation

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"discovery/internal/database"
)

func cloneState(in State) State {
	out := in
	out.Tasks = cloneTasks(in.Tasks)
	out.RecentExecutions = append([]ExecutionRecord(nil), in.RecentExecutions...)
	return out
}

func cloneTasks(in []AutomationTask) []AutomationTask {
	if len(in) == 0 {
		return nil
	}
	out := make([]AutomationTask, len(in))
	copy(out, in)
	for i := range out {
		out[i].IncludeTags = append([]string(nil), in[i].IncludeTags...)
		out[i].ExcludeTags = append([]string(nil), in[i].ExcludeTags...)
		if in[i].Script != nil {
			scriptCopy := *in[i].Script
			out[i].Script = &scriptCopy
		}
	}
	return out
}

func mapExecutionEntries(entries []database.AutomationExecutionEntry) []ExecutionRecord {
	out := make([]ExecutionRecord, 0, len(entries))
	for _, entry := range entries {
		out = append(out, ExecutionRecord{
			ExecutionID:      entry.ExecutionID,
			CommandID:        entry.CommandID,
			TaskID:           entry.TaskID,
			TaskName:         entry.TaskName,
			ActionType:       AutomationTaskActionType(entry.ActionType),
			InstallationType: AppInstallationType(entry.InstallationType),
			SourceType:       AutomationExecutionSourceType(entry.SourceType),
			TriggerType:      TriggerType(entry.TriggerType),
			Status:           AutomationExecutionStatus(entry.Status),
			Success:          entry.Success,
			ExitCode:         entry.ExitCode,
			ExitCodeSet:      entry.ExitCodeSet,
			ErrorMessage:     entry.ErrorMessage,
			Output:           entry.Output,
			PackageID:        entry.PackageID,
			ScriptID:         entry.ScriptID,
			CorrelationID:    entry.CorrelationID,
			StartedAt:        formatTime(entry.StartedAt),
			FinishedAt:       formatTime(entry.FinishedAt),
			MetadataJSON:     entry.MetadataJSON,
		})
	}
	return out
}

func sourceForTrigger(triggerType TriggerType) AutomationExecutionSourceType {
	switch triggerType {
	case TriggerTypeImmediate, TriggerTypeAgentCheckIn:
		return ExecutionSourceForceSync
	case TriggerTypeRecurring, TriggerTypeUserLogin:
		return ExecutionSourceScheduled
	case TriggerTypeManual:
		return ExecutionSourceAgentManual
	default:
		return ExecutionSourceRunNow
	}
}

func buildExecutionMetadata(task AutomationTask, triggerType TriggerType, stage string, result *ExecutionResult, policy *PSADTPolicy) string {
	payload := map[string]any{
		"stage":            stage,
		"triggerType":      string(triggerType),
		"taskName":         task.Name,
		"actionType":       string(task.ActionType),
		"packageId":        task.PackageID,
		"scriptId":         task.ScriptID,
		"requiresApproval": task.RequiresApproval,
	}
	if result != nil {
		payload["success"] = result.Success
		if result.ExitCodeSet {
			payload["exitCode"] = result.ExitCode
		}
	}
	if policy != nil {
		payload["psadtPolicy"] = map[string]any{
			"executionTimeoutSeconds": policy.ExecutionTimeoutSeconds,
			"fallbackPolicy":          policy.FallbackPolicy,
			"timeoutAction":           policy.TimeoutAction,
			"unknownExitCodePolicy":   policy.UnknownExitCodePolicy,
			"successExitCodes":        append([]int(nil), policy.SuccessExitCodes...),
			"rebootExitCodes":         append([]int(nil), policy.RebootExitCodes...),
			"ignoreExitCodes":         append([]int(nil), policy.IgnoreExitCodes...),
		}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(data)
}

func isPackageAction(actionType AutomationTaskActionType) bool {
	switch actionType {
	case ActionInstallPackage, ActionUpdatePackage, ActionRemovePackage, ActionUpdateOrInstallPackage:
		return true
	default:
		return false
	}
}

func callbackBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		attempt = 1
	}
	backoff := callbackRetryBase
	for i := 1; i < attempt; i++ {
		backoff *= 2
		if backoff >= callbackRetryMax {
			return callbackRetryMax
		}
	}
	if backoff > callbackRetryMax {
		return callbackRetryMax
	}
	return backoff
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func formatMessage(format string, args ...any) string {
	if len(args) == 0 {
		return format
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(fmt.Sprintf(format, args...)), "\n"))
}

func nextRunInterval(state State) time.Duration {
	if !state.Available || !state.Connected {
		return policyRetryInterval
	}
	return policySyncInterval
}
