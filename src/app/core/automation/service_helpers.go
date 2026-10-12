package automation

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"discovery/app/core/database"
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
		"notificationMode": ResolveNotificationMode(task),
		"toastTiming":      ResolveToastTiming(task),
	}
	if result != nil {
		payload["success"] = result.Success
		if result.ExitCodeSet {
			payload["exitCode"] = result.ExitCode
		}
		// Metadados específicos do executor (ex.: wingetDecision) são mesclados no
		// topo do payload enviado ao servidor.
		if extra := strings.TrimSpace(result.MetadataJSON); extra != "" {
			var decoded map[string]any
			if json.Unmarshal([]byte(extra), &decoded) == nil {
				for key, value := range decoded {
					payload[key] = value
				}
			}
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

// Marcador próprio para usar o valor de um campo customizado dentro de um
// comando/argumento: [[campo:anydesk]]. A chave é o "name" da definição.
// Tokens sem campo correspondente (ou com valor nulo) ficam como estão — nunca
// apagamos texto do usuário silenciosamente.
const customFieldTokenPrefix = "[[campo:"
const customFieldTokenSuffix = "]]"

func expandCustomFieldTokens(value string, fields map[string]any) string {
	if value == "" || len(fields) == 0 || !strings.Contains(value, customFieldTokenPrefix) {
		return value
	}

	var builder strings.Builder
	remaining := value
	for {
		start := strings.Index(remaining, customFieldTokenPrefix)
		if start < 0 {
			builder.WriteString(remaining)
			break
		}

		end := strings.Index(remaining[start:], customFieldTokenSuffix)
		if end < 0 {
			builder.WriteString(remaining)
			break
		}

		end += start
		builder.WriteString(remaining[:start])

		key := strings.TrimSpace(remaining[start+len(customFieldTokenPrefix) : end])
		if resolved, ok := resolveCustomFieldToken(fields, key); ok {
			builder.WriteString(resolved)
		} else {
			builder.WriteString(remaining[start : end+len(customFieldTokenSuffix)])
		}

		remaining = remaining[end+len(customFieldTokenSuffix):]
	}

	return builder.String()
}

// buildCustomFieldReportMetadata devolve o bloco "customFields" do relatório da
// execução. Só NOMES: os valores não entram no relatório (evita vazar dados de
// campos, inclusive secretos, para o histórico do portal).
func buildCustomFieldReportMetadata(cfCtx *ExecutionCustomFieldCtx, collected []CollectedValueRequest, collectorFieldID string) string {
	read := make([]string, 0)

	if cfCtx != nil {
		for name := range cfCtx.Fields {
			read = append(read, name)
		}
		sort.Strings(read)
	}

	seen := make(map[string]bool)
	collectedReport := make([]collectedFieldReport, 0)
	for index := range collected {
		name := ""
		if collected[index].Name != nil {
			name = strings.TrimSpace(*collected[index].Name)
		}

		var rawField *RuntimeCustomField
		if collected[index].DefinitionID != nil && cfCtx != nil {
			if found, ok := cfCtx.rawByID[strings.TrimSpace(*collected[index].DefinitionID)]; ok {
				rawField = &found
				if name == "" {
					name = strings.TrimSpace(found.Name)
				}
			}
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true

		// O valor entra truncado; campo secreto nunca vai em claro no relatório.
		value := formatCollectedReportValue(collected[index].Value)
		if rawField != nil && rawField.IsSecret {
			value = "***"
		}
		collectedReport = append(collectedReport, collectedFieldReport{
			Name:  name,
			Value: truncateReportValue(value, 60),
		})
	}

	payload := struct {
		Read           []string               `json:"read"`
		Collected      []collectedFieldReport `json:"collected"`
		CollectorField string                 `json:"collectorField,omitempty"`
	}{Read: read, Collected: collectedReport, CollectorField: strings.TrimSpace(collectorFieldID)}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// collectedFieldReport é um item coletado exibido no relatório da execução.
type collectedFieldReport struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}

func formatCollectedReportValue(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return string(raw)
	}

	switch typed := decoded.(type) {
	case string:
		return typed
	case nil:
		return ""
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if item != nil {
				parts = append(parts, fmt.Sprintf("%v", item))
			}
		}
		return strings.Join(parts, ", ")
	default:
		return fmt.Sprintf("%v", typed)
	}
}

func truncateReportValue(value string, max int) string {
	trimmed := strings.TrimSpace(value)
	if max <= 0 || len(trimmed) <= max {
		return trimmed
	}
	return trimmed[:max] + "..."
}

// buildExecutionNoteContent monta o texto da nota criada no agent quando a
// tarefa tem a opção "gravar saída como nota".
func buildExecutionNoteContent(task AutomationTask, result ExecutionResult) string {
	var builder strings.Builder
	builder.WriteString("Tarefa: ")
	builder.WriteString(strings.TrimSpace(task.Name))
	builder.WriteString("\nResultado: ")
	if result.Success {
		builder.WriteString("sucesso")
	} else {
		builder.WriteString("falha")
	}

	if result.ExitCodeSet {
		builder.WriteString(fmt.Sprintf("\nExitCode: %d", result.ExitCode))
	}
	if message := strings.TrimSpace(result.ErrorMessage); message != "" {
		builder.WriteString("\nErro: ")
		builder.WriteString(message)
	}
	if output := strings.TrimSpace(result.Output); output != "" {
		builder.WriteString("\n\nSaída:\n")
		builder.WriteString(truncateReportValue(output, 4000))
	}

	return builder.String()
}

// mergeJSONMetadata injeta um bloco em um JSON de metadata já existente.
func mergeJSONMetadata(base string, key string, raw string) string {
	if raw == "" {
		return base
	}

	merged := make(map[string]any)
	if trimmed := strings.TrimSpace(base); trimmed != "" && trimmed != "{}" {
		if err := json.Unmarshal([]byte(trimmed), &merged); err != nil {
			return base
		}
	}

	merged[key] = json.RawMessage(raw)
	encoded, err := json.Marshal(merged)
	if err != nil {
		return base
	}
	return string(encoded)
}

// collectorValueFromOutput devolve a ultima linha nao vazia da saida - e o
// valor que uma tarefa coletora grava no campo customizado configurado.
func collectorValueFromOutput(output string) string {
	lines := strings.Split(output, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		if trimmed := strings.TrimSpace(lines[index]); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func resolveCustomFieldToken(fields map[string]any, key string) (string, bool) {
	if key == "" {
		return "", false
	}

	value, ok := fields[key]
	if !ok || value == nil {
		return "", false
	}

	switch typed := value.(type) {
	case string:
		return typed, true
	case bool:
		if typed {
			return "true", true
		}
		return "false", true
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), true
	case int:
		return strconv.Itoa(typed), true
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if item == nil {
				continue
			}
			parts = append(parts, fmt.Sprintf("%v", item))
		}
		return strings.Join(parts, ", "), true
	default:
		return fmt.Sprintf("%v", typed), true
	}
}

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
