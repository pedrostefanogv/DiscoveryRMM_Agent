package automation

import (
	"context"
	"fmt"
	"runtime"
	"strings"
)

const (
	psadtImportFailureExitCode = 60008
	psadtScriptFailureExitCode = 60001
)

type psadtExitCategory string

const (
	psadtExitSuccess       psadtExitCategory = "success"
	psadtExitSuccessReboot psadtExitCategory = "success_reboot_required"
	psadtExitRecoverable   psadtExitCategory = "recoverable_failure"
	psadtExitUserDenied    psadtExitCategory = "user_denied"
	psadtExitFatal         psadtExitCategory = "fatal_failure"
	psadtExitUnknown       psadtExitCategory = "unknown"
)

func executePSAppDeployToolkit(ctx context.Context, packages PackageManager, packageID, operation string) ExecutionResult {
	if runtime.GOOS != "windows" {
		return ExecutionResult{Success: false, ExitCode: 126, ExitCodeSet: true, ErrorMessage: "PSAppDeployToolkit suportado apenas no Windows"}
	}

	args, err := buildPSADTWingetArguments(packageID, operation)
	if err != nil {
		return ExecutionResult{Success: false, ExitCode: 2, ExitCodeSet: true, ErrorMessage: err.Error()}
	}

	script := buildPSADTScriptForWinget(args)
	result := executeProcess(ctx, "powershell", []string{
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy", "Bypass",
		"-Command", script,
	}, defaultExecutionTimeout)

	result = normalizePSADTExecutionResult(result)
	category := classifyPSADTExitCode(result.ExitCode)
	if result.Success {
		if strings.TrimSpace(result.Output) != "" {
			result.Output = "[psadt] category=" + string(category) + "\n" + result.Output
		}
		return result
	}

	if shouldFallbackFromPSADT(category) {
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

func normalizePSADTExecutionResult(result ExecutionResult) ExecutionResult {
	if !result.ExitCodeSet {
		return result
	}
	category := classifyPSADTExitCode(result.ExitCode)
	if category == psadtExitSuccess || category == psadtExitSuccessReboot {
		result.Success = true
		result.ErrorMessage = ""
	}
	return result
}

func shouldFallbackFromPSADT(category psadtExitCategory) bool {
	return category == psadtExitRecoverable || category == psadtExitUnknown
}

func classifyPSADTExitCode(exitCode int) psadtExitCategory {
	switch exitCode {
	case 0:
		return psadtExitSuccess
	case 1641, 3010:
		return psadtExitSuccessReboot
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
	return psadtExitUnknown
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
