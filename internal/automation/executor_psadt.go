package automation

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"
)

const (
	psadtImportFailureExitCode = 60008
	psadtScriptFailureExitCode = 60001
)

type psadtExitCategory string

const (
	psadtExitSuccess       psadtExitCategory = "success"
	psadtExitSuccessReboot psadtExitCategory = "success_reboot_required"
	psadtExitIgnored       psadtExitCategory = "ignored"
	psadtExitRecoverable   psadtExitCategory = "recoverable_failure"
	psadtExitUserDenied    psadtExitCategory = "user_denied"
	psadtExitFatal         psadtExitCategory = "fatal_failure"
	psadtExitUnknown       psadtExitCategory = "unknown"
)

func executePSAppDeployToolkit(ctx context.Context, packages PackageManager, packageID, operation string) ExecutionResult {
	if runtime.GOOS != "windows" {
		return ExecutionResult{Success: false, ExitCode: 126, ExitCodeSet: true, ErrorMessage: "PSAppDeployToolkit suportado apenas no Windows"}
	}

	policy := normalizePSADTPolicy(PSADTPolicy{})
	return executePSAppDeployToolkitWithPolicy(ctx, packages, packageID, operation, policy)
}

func executePSAppDeployToolkitWithPolicy(ctx context.Context, packages PackageManager, packageID, operation string, policy PSADTPolicy) ExecutionResult {
	policy = normalizePSADTPolicy(policy)

	var script string
	allowFallback := true
	if isMSIPackageID(packageID) {
		msiPath := normalizeMSIPackagePath(packageID)
		if strings.TrimSpace(msiPath) == "" {
			return ExecutionResult{Success: false, ExitCode: 2, ExitCodeSet: true, ErrorMessage: "pacote MSI invalido"}
		}
		script = buildPSADTScriptForMSI(msiPath, operation)
		allowFallback = false
	} else {
		args, err := buildPSADTWingetArguments(packageID, operation)
		if err != nil {
			return ExecutionResult{Success: false, ExitCode: 2, ExitCodeSet: true, ErrorMessage: err.Error()}
		}
		script = buildPSADTScriptForWinget(args)
	}

	result := executeProcess(ctx, "powershell", []string{
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy", "Bypass",
		"-Command", script,
	}, time.Duration(policy.ExecutionTimeoutSeconds)*time.Second)

	result = applyPSADTTimeoutAction(result, policy)
	result = normalizePSADTExecutionResult(result, policy)
	category := classifyPSADTExitCodeWithPolicy(result.ExitCode, policy)
	if result.Success {
		if strings.TrimSpace(result.Output) != "" {
			result.Output = "[psadt] category=" + string(category) + "\n" + result.Output
		}
		return result
	}

	if allowFallback && shouldFallbackFromPSADT(category, policy) {
		fallback := executeWingetFallback(ctx, packages, packageID, operation)
		if fallback.Success {
			if strings.TrimSpace(fallback.Output) != "" {
				fallback.Output = "[psadt] category=" + string(category) + " fallback aplicado para winget/choco\n" + fallback.Output
			}
			return fallback
		}
		if strings.TrimSpace(fallback.ErrorMessage) != "" {
			result.ErrorMessage = fmt.Sprintf("%s; fallback=%s", result.ErrorMessage, fallback.ErrorMessage)
		}
	}

	return result
}

func buildPSADTWingetArguments(packageID, operation string) (string, error) {
	id := strings.TrimSpace(packageID)
	if id == "" {
		return "", fmt.Errorf("packageId obrigatorio para acao PSADT")
	}

	escapedID := escapePowerShellSingleQuoted(id)
	switch operation {
	case "install":
		return fmt.Sprintf("install --id '%s' --exact --silent --accept-source-agreements --accept-package-agreements", escapedID), nil
	case "upgrade":
		return fmt.Sprintf("upgrade --id '%s' --exact --silent --accept-source-agreements --accept-package-agreements", escapedID), nil
	case "uninstall":
		return fmt.Sprintf("uninstall --id '%s' --exact --silent", escapedID), nil
	default:
		return "", fmt.Errorf("operacao PSADT invalida")
	}
}

func buildPSADTScriptForWinget(wingetArgs string) string {
	// Keep script compact and explicit for deterministic exit codes.
	return fmt.Sprintf(`$ErrorActionPreference='Stop'
try {
  Import-Module PSAppDeployToolkit -ErrorAction Stop
} catch {
  Write-Output $_.Exception.Message
  exit %d
}

try {
  $result = Start-ADTProcess -FilePath 'winget' -ArgumentList '%s' -WaitForChildProcesses -WindowStyle Hidden -PassThru
  if ($result -and $null -ne $result.ExitCode) {
    exit [int]$result.ExitCode
  }
  exit 0
} catch {
  Write-Output $_.Exception.Message
  exit %d
}
`, psadtImportFailureExitCode, wingetArgs, psadtScriptFailureExitCode)
}

func buildPSADTScriptForMSI(msiPath, operation string) string {
	escapedPath := escapePowerShellSingleQuoted(msiPath)
	msiAction := "Install"
	switch strings.ToLower(strings.TrimSpace(operation)) {
	case "uninstall":
		msiAction = "Uninstall"
	case "upgrade":
		msiAction = "Install"
	}

	return fmt.Sprintf(`$ErrorActionPreference='Stop'
try {
  Import-Module PSAppDeployToolkit -ErrorAction Stop
} catch {
  Write-Output $_.Exception.Message
  exit %d
}

try {
  $result = Start-ADTMsiProcess -Action %s -FilePath '%s' -PassThru
  if ($result -and $null -ne $result.ExitCode) {
    exit [int]$result.ExitCode
  }
  exit 0
} catch {
  Write-Output $_.Exception.Message
  exit %d
}
`, psadtImportFailureExitCode, msiAction, escapedPath, psadtScriptFailureExitCode)
}

func isMSIPackageID(packageID string) bool {
	text := strings.ToLower(strings.TrimSpace(packageID))
	if text == "" {
		return false
	}
	if strings.HasPrefix(text, "msi:") {
		return true
	}
	return strings.HasSuffix(text, ".msi")
}

func normalizeMSIPackagePath(packageID string) string {
	trimmed := strings.TrimSpace(packageID)
	if strings.HasPrefix(strings.ToLower(trimmed), "msi:") {
		return strings.TrimSpace(trimmed[4:])
	}
	return trimmed
}

func normalizePSADTExecutionResult(result ExecutionResult, policy PSADTPolicy) ExecutionResult {
	if !result.ExitCodeSet {
		return result
	}
	category := classifyPSADTExitCodeWithPolicy(result.ExitCode, policy)
	if category == psadtExitSuccess || category == psadtExitSuccessReboot || category == psadtExitIgnored {
		result.Success = true
		result.ErrorMessage = ""
	}
	return result
}

func shouldFallbackFromPSADT(category psadtExitCategory, policy PSADTPolicy) bool {
	if !isPSADTFallbackEnabled(policy.FallbackPolicy) {
		return false
	}
	return category == psadtExitRecoverable || category == psadtExitUnknown
}

func classifyPSADTExitCode(exitCode int) psadtExitCategory {
	return classifyPSADTExitCodeWithPolicy(exitCode, normalizePSADTPolicy(PSADTPolicy{}))
}

func classifyPSADTExitCodeWithPolicy(exitCode int, policy PSADTPolicy) psadtExitCategory {
	policy = normalizePSADTPolicy(policy)

	if containsInt(policy.RebootExitCodes, exitCode) {
		return psadtExitSuccessReboot
	}
	if containsInt(policy.SuccessExitCodes, exitCode) {
		return psadtExitSuccess
	}
	if containsInt(policy.IgnoreExitCodes, exitCode) {
		return psadtExitIgnored
	}

	switch exitCode {
	case 1602:
		return psadtExitUserDenied
	case 1618:
		return psadtExitRecoverable
	}

	if exitCode >= 60000 && exitCode <= 68999 {
		return psadtExitRecoverable
	}
	if exitCode >= 69000 && exitCode <= 69999 {
		return psadtExitRecoverable
	}
	if exitCode >= 70000 && exitCode <= 79999 {
		return psadtExitRecoverable
	}

	switch exitCode {
	case 1, 2, 126, 127:
		return psadtExitRecoverable
	case 3, 4, 5:
		return psadtExitFatal
	}

	if exitCode < 0 {
		return psadtExitFatal
	}
	if isPSADTUnknownExitFatal(policy.UnknownExitCodePolicy) {
		return psadtExitFatal
	}
	return psadtExitRecoverable
}

func applyPSADTTimeoutAction(result ExecutionResult, policy PSADTPolicy) ExecutionResult {
	if !strings.Contains(strings.ToLower(strings.TrimSpace(result.ErrorMessage)), "timeout") {
		return result
	}

	switch strings.ToLower(strings.TrimSpace(policy.TimeoutAction)) {
	case "ignore", "continue":
		result.Success = true
		result.ErrorMessage = ""
		if !result.ExitCodeSet {
			result.ExitCode = 0
			result.ExitCodeSet = true
		}
		return result
	case "fallback", "retry", "retry_then_fallback":
		result.Success = false
		result.ExitCode = 1618
		result.ExitCodeSet = true
		return result
	default:
		return result
	}
}

func normalizePSADTPolicy(policy PSADTPolicy) PSADTPolicy {
	if policy.ExecutionTimeoutSeconds <= 0 {
		policy.ExecutionTimeoutSeconds = int(defaultExecutionTimeout.Seconds())
	}
	if len(policy.SuccessExitCodes) == 0 {
		policy.SuccessExitCodes = []int{0, 3010}
	}
	if len(policy.RebootExitCodes) == 0 {
		policy.RebootExitCodes = []int{1641, 3010}
	}
	if strings.TrimSpace(policy.FallbackPolicy) == "" {
		policy.FallbackPolicy = "winget_then_choco"
	}
	if strings.TrimSpace(policy.TimeoutAction) == "" {
		policy.TimeoutAction = "fail"
	}
	if strings.TrimSpace(policy.UnknownExitCodePolicy) == "" {
		policy.UnknownExitCodePolicy = "recoverable_failure"
	}
	return policy
}

func isPSADTFallbackEnabled(policy string) bool {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "no_fallback", "none", "disabled", "off", "fail_fast":
		return false
	default:
		return true
	}
}

func isPSADTUnknownExitFatal(policy string) bool {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "fatal", "fatal_failure", "fail":
		return true
	default:
		return false
	}
}

func containsInt(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func executeWingetFallback(ctx context.Context, packages PackageManager, packageID, operation string) ExecutionResult {
	if packages != nil {
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
			return ExecutionResult{Success: false, ExitCode: 2, ExitCodeSet: true, ErrorMessage: "operacao de fallback invalida"}
		}
		res := resultFromCommand(out, err)
		if res.Success {
			return res
		}
	}

	return executeChocolatey(ctx, packageID, operation)
}

func escapePowerShellSingleQuoted(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}
