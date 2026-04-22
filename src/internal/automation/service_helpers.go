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

// --- Custom Fields helpers ---

const collectPrefix = "MDZ_COLLECT:"

// buildExecutionCustomFieldCtx converte a lista de campos de runtime em um contexto de execução.
// Campos com isSecret=true são excluídos do mapa Fields, mas indexados para validação de escrita.
func buildExecutionCustomFieldCtx(fields []RuntimeCustomField) *ExecutionCustomFieldCtx {
	ctx := &ExecutionCustomFieldCtx{
		Fields:    make(map[string]any, len(fields)),
		rawByName: make(map[string]RuntimeCustomField, len(fields)),
		rawByID:   make(map[string]RuntimeCustomField, len(fields)),
	}
	for _, f := range fields {
		name := strings.TrimSpace(f.Name)
		id := strings.TrimSpace(f.DefinitionID)
		if name != "" {
			ctx.rawByName[name] = f
		}
		if id != "" {
			ctx.rawByID[id] = f
		}
		if f.IsSecret {
			continue // não expõe valor secreto no runtime map
		}
		if len(f.ValueJson) == 0 || string(f.ValueJson) == "null" {
			continue // omite valores null
		}
		var decoded any
		if err := json.Unmarshal(f.ValueJson, &decoded); err != nil {
			continue // valor malformado - omite silenciosamente
		}
		if name != "" {
			ctx.Fields[name] = decoded
		}
	}
	return ctx
}

// validateCollectedWrite verifica localmente se o campo pode ser escrito na execução atual.
// Retorna ErrCustomFieldWrite se a validação falhar (fail-fast, sem round-trip HTTP).
func validateCollectedWrite(cfCtx *ExecutionCustomFieldCtx, req CollectedValueRequest) error {
	if cfCtx == nil {
		return &ErrCustomFieldWrite{Code: WriteErrorContextDenied, Message: "contexto de custom fields nao disponivel para esta execucao"}
	}

	var field RuntimeCustomField
	found := false

	if req.DefinitionID != nil && strings.TrimSpace(*req.DefinitionID) != "" {
		if f, ok := cfCtx.rawByID[strings.TrimSpace(*req.DefinitionID)]; ok {
			field = f
			found = true
		}
	}
	if !found && req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		if f, ok := cfCtx.rawByName[strings.TrimSpace(*req.Name)]; ok {
			field = f
			found = true
		}
	}
	if !found {
		return &ErrCustomFieldWrite{Code: WriteErrorNotFound, Message: "campo nao encontrado no runtime cache desta execucao"}
	}
	if field.IsSecret {
		return &ErrCustomFieldWrite{Code: WriteErrorNotAllowed, Message: "escrita de campo secreto nao permitida pelo agent"}
	}
	return nil
}

// parseCollectedValues varre o output de um script em busca de linhas "MDZ_COLLECT: <json>".
// Retorna os itens encontrados e o output sem as linhas de coleta.
func parseCollectedValues(output string) (items []CollectedValueRequest, cleanedOutput string) {
	lines := strings.Split(output, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, collectPrefix) {
			raw := strings.TrimSpace(trimmed[len(collectPrefix):])
			var item CollectedValueRequest
			if err := json.Unmarshal([]byte(raw), &item); err == nil {
				if len(item.Value) > 0 && string(item.Value) != "null" {
					items = append(items, item)
				}
			}
			// não inclui a linha no output limpo
			continue
		}
		kept = append(kept, line)
	}
	cleanedOutput = strings.TrimSpace(strings.Join(kept, "\n"))
	return items, cleanedOutput
}

// sanitizeCustomFieldErrForLog retorna uma mensagem de erro sanitizada para log,
// evitando vazar payloads de valores.
func sanitizeCustomFieldErrForLog(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	// Remove possíveis fragmentos JSON do erro
	if idx := strings.Index(msg, "{"); idx > 0 {
		msg = strings.TrimSpace(msg[:idx])
	}
	return msg
}
