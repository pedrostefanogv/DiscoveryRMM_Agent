package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"discovery/internal/processutil"
)

const (
	defaultExecutionTimeout = 10 * time.Minute
	maxStoredOutputBytes    = 64 * 1024
)

type PackageAuthorizationFunc func(ctx context.Context, installationType AppInstallationType, packageID, operation string) error

func executeTask(ctx context.Context, packages PackageManager, authorize PackageAuthorizationFunc, task AutomationTask, psadtPolicy PSADTPolicy, customFields map[string]any) ExecutionResult {
	if task.RequiresApproval {
		return ExecutionResult{Success: false, ExitCode: 10, ExitCodeSet: true, ErrorMessage: "tarefa exige aprovacao e nao pode ser executada automaticamente"}
	}

	switch task.ActionType {
	case ActionInstallPackage:
		return executePackageAction(ctx, packages, authorize, task, "install", psadtPolicy)
	case ActionUpdatePackage:
		return executePackageAction(ctx, packages, authorize, task, "upgrade", psadtPolicy)
	case ActionRemovePackage:
		return executePackageAction(ctx, packages, authorize, task, "uninstall", psadtPolicy)
	case ActionUpdateOrInstallPackage:
		if result := executePackageAction(ctx, packages, authorize, task, "upgrade", psadtPolicy); result.Success {
			return result
		}
		return executePackageAction(ctx, packages, authorize, task, "install", psadtPolicy)
	case ActionRunScript:
		return executeScript(ctx, task, customFields)
	case ActionCustomCommand:
		return executeCustomCommand(ctx, task, customFields)
	default:
		return ExecutionResult{Success: false, ExitCode: 2, ExitCodeSet: true, ErrorMessage: "actionType nao suportado"}
	}
}

func executePackageAction(ctx context.Context, packages PackageManager, authorize PackageAuthorizationFunc, task AutomationTask, operation string, psadtPolicy PSADTPolicy) ExecutionResult {
	packageID := strings.TrimSpace(task.PackageID)
	if packageID == "" {
		return ExecutionResult{Success: false, ExitCode: 2, ExitCodeSet: true, ErrorMessage: "packageId obrigatorio para acao de pacote"}
	}

	installationType := task.InstallationType
	if installationType == "" {
		installationType = InstallationWinget
	}

	if authorize != nil {
		if err := authorize(ctx, installationType, packageID, operation); err != nil {
			return ExecutionResult{Success: false, ExitCode: 13, ExitCodeSet: true, ErrorMessage: err.Error()}
		}
	}

	switch installationType {
	case InstallationWinget:
		if packages == nil {
			return ExecutionResult{Success: false, ExitCode: 2, ExitCodeSet: true, ErrorMessage: "gerenciador Winget indisponivel"}
		}
		var out string
		var err error
		switch operation {
		case "install":
			out, err = packages.Install(ctx, packageID)
		case "upgrade":
			out, err = packages.Upgrade(ctx, packageID)
		case "uninstall":
			out, err = packages.Uninstall(ctx, packageID)
		default:
			return ExecutionResult{Success: false, ExitCode: 2, ExitCodeSet: true, ErrorMessage: "operacao de pacote invalida"}
		}
		return resultFromCommand(out, err)
	case InstallationChocolatey:
		return executeChocolatey(ctx, packageID, operation)
	case InstallationPSAppDeployToolkit:
		return executePSAppDeployToolkitWithPolicy(ctx, packages, packageID, operation, psadtPolicy)
	default:
		return ExecutionResult{Success: false, ExitCode: 2, ExitCodeSet: true, ErrorMessage: "installationType nao suportado"}
	}
}

func executeChocolatey(ctx context.Context, packageID, operation string) ExecutionResult {
	if _, err := exec.LookPath("choco"); err != nil {
		return ExecutionResult{Success: false, ExitCode: 127, ExitCodeSet: true, ErrorMessage: "Chocolatey nao encontrado no host"}
	}

	args := []string{operation, packageID, "-y", "--no-progress"}
	if operation == "upgrade" {
		args = []string{"upgrade", packageID, "-y", "--no-progress"}
	}
	if operation == "install" {
		args = []string{"install", packageID, "-y", "--no-progress"}
	}
	if operation == "uninstall" {
		args = []string{"uninstall", packageID, "-y", "--no-progress"}
	}
	return executeProcess(ctx, "choco", args, defaultExecutionTimeout)
}

func executeScript(ctx context.Context, task AutomationTask, customFields map[string]any) ExecutionResult {
	if task.Script == nil {
		return ExecutionResult{Success: false, ExitCode: 2, ExitCodeSet: true, ErrorMessage: "script nao resolvido para a tarefa"}
	}
	content := strings.TrimSpace(task.Script.Content)
	if content == "" {
		return ExecutionResult{Success: false, ExitCode: 2, ExitCodeSet: true, ErrorMessage: "conteudo do script nao disponivel; refaca o sync com IncludeScriptContent"}
	}

	extraEnv := buildCustomFieldsEnv(customFields)
	switch task.Script.ScriptType {
	case ScriptPowerShell:
		return executeProcessWithEnv(ctx, "powershell", []string{"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", content}, defaultExecutionTimeout, extraEnv)
	case ScriptShell, ScriptBatch, ScriptCustom:
		return executeProcessWithEnv(ctx, "cmd", []string{"/C", content}, defaultExecutionTimeout, extraEnv)
	case ScriptPython:
		return executeProcessWithEnv(ctx, "python", []string{"-c", content}, defaultExecutionTimeout, extraEnv)
	default:
		return ExecutionResult{Success: false, ExitCode: 2, ExitCodeSet: true, ErrorMessage: "scriptType nao suportado"}
	}
}

func executeCustomCommand(ctx context.Context, task AutomationTask, customFields map[string]any) ExecutionResult {
	commandType, command, args, timeout, err := parseCommandPayload(task.CommandPayload)
	if err != nil {
		return ExecutionResult{Success: false, ExitCode: 2, ExitCodeSet: true, ErrorMessage: err.Error()}
	}

	extraEnv := buildCustomFieldsEnv(customFields)
	switch commandType {
	case "powershell", "ps":
		return executeProcessWithEnv(ctx, "powershell", []string{"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", command}, timeout, extraEnv)
	case "cmd", "shell":
		return executeProcessWithEnv(ctx, "cmd", []string{"/C", command}, timeout, extraEnv)
	case "exec", "process", "winget":
		return executeProcessWithEnv(ctx, command, args, timeout, extraEnv)
	default:
		return executeProcessWithEnv(ctx, "cmd", []string{"/C", command}, timeout, extraEnv)
	}
}

func parseCommandPayload(payload string) (string, string, []string, time.Duration, error) {
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" {
		return "", "", nil, 0, fmt.Errorf("commandPayload obrigatorio para CustomCommand")
	}

	var asString string
	if err := json.Unmarshal([]byte(trimmed), &asString); err == nil {
		trimmed = strings.TrimSpace(asString)
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(trimmed), &raw); err == nil {
		cmdType := strings.ToLower(strings.TrimSpace(anyString(raw["cmdType"], raw["type"])))
		command := strings.TrimSpace(anyString(raw["command"], raw["script"], raw["file"]))
		if command == "" {
			return "", "", nil, 0, fmt.Errorf("commandPayload sem comando")
		}
		args := anyStringSlice(raw["args"])
		timeout := parseTimeout(raw["timeoutSec"], raw["timeoutSeconds"])
		if timeout <= 0 {
			timeout = defaultExecutionTimeout
		}
		if cmdType == "" {
			cmdType = inferCommandType(command)
		}
		return cmdType, command, args, timeout, nil
	}

	return "cmd", trimmed, nil, defaultExecutionTimeout, nil
}

func inferCommandType(command string) string {
	trimmed := strings.TrimSpace(strings.ToLower(command))
	if strings.HasSuffix(trimmed, ".exe") {
		return "exec"
	}
	if strings.HasPrefix(trimmed, "winget ") {
		return "cmd"
	}
	return "cmd"
}

func executeProcess(parent context.Context, executable string, args []string, timeout time.Duration) ExecutionResult {
	return executeProcessWithEnv(parent, executable, args, timeout, nil)
}

// executeProcessWithEnv executa um processo adicionando extraEnv ao ambiente herdado do processo pai.
func executeProcessWithEnv(parent context.Context, executable string, args []string, timeout time.Duration, extraEnv []string) ExecutionResult {
	if strings.TrimSpace(executable) == "" {
		return ExecutionResult{Success: false, ExitCode: 2, ExitCodeSet: true, ErrorMessage: "executavel obrigatorio"}
	}
	if timeout <= 0 {
		timeout = defaultExecutionTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, executable, args...)
	processutil.HideWindow(cmd)
	processutil.ApplyUserContext(ctx, cmd)
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	output, err := cmd.CombinedOutput()
	text := truncateOutput(string(output))
	if err == nil {
		return ExecutionResult{Success: true, ExitCode: 0, ExitCodeSet: true, Output: text}
	}

	exitCode := 1
	if exitErr, ok := err.(*exec.ExitError); ok {
		exitCode = exitErr.ExitCode()
	}
	errText := err.Error()
	if ctx.Err() == context.DeadlineExceeded {
		errText = "timeout excedido"
	}
	return ExecutionResult{Success: false, ExitCode: exitCode, ExitCodeSet: true, Output: text, ErrorMessage: errText}
}

func resultFromCommand(output string, err error) ExecutionResult {
	text := truncateOutput(output)
	if err == nil {
		return ExecutionResult{Success: true, ExitCode: 0, ExitCodeSet: true, Output: text}
	}
	return ExecutionResult{Success: false, ExitCode: 1, ExitCodeSet: true, Output: text, ErrorMessage: err.Error()}
}

func truncateOutput(output string) string {
	if len(output) <= maxStoredOutputBytes {
		return strings.TrimSpace(output)
	}
	return strings.TrimSpace(output[:maxStoredOutputBytes]) + "\n... output truncado ..."
}

// buildCustomFieldsEnv serializa o mapa de custom fields como variável de ambiente MDZ_CUSTOM_FIELDS.
// Retorna nil quando o mapa estiver vazio para não poluir o ambiente do processo.
func buildCustomFieldsEnv(customFields map[string]any) []string {
	if len(customFields) == 0 {
		return nil
	}
	data, err := json.Marshal(customFields)
	if err != nil {
		return nil
	}
	return []string{"MDZ_CUSTOM_FIELDS=" + string(data)}
}

func anyString(values ...any) string {
	for _, value := range values {
		switch typed := value.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				return typed
			}
		case fmt.Stringer:
			if strings.TrimSpace(typed.String()) != "" {
				return typed.String()
			}
		}
	}
	return ""
}

func anyStringSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text := strings.TrimSpace(anyString(item))
		if text != "" {
			out = append(out, text)
		}
	}
	return out
}

func parseTimeout(values ...any) time.Duration {
	for _, value := range values {
		switch typed := value.(type) {
		case float64:
			if typed > 0 {
				return time.Duration(int(typed)) * time.Second
			}
		case int:
			if typed > 0 {
				return time.Duration(typed) * time.Second
			}
		case string:
			var seconds int
			if _, err := fmt.Sscanf(strings.TrimSpace(typed), "%d", &seconds); err == nil && seconds > 0 {
				return time.Duration(seconds) * time.Second
			}
		}
	}
	return 0
}
