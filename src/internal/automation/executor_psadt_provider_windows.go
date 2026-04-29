//go:build windows

package automation

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	psadt "github.com/pedrostefanogv/go-psadt"
	pstypes "github.com/pedrostefanogv/go-psadt/types"
)

func executePSADTWithLibrary(ctx context.Context, packageID, operation string, policy PSADTPolicy) ExecutionResult {
	id := strings.TrimSpace(packageID)
	if id == "" {
		return ExecutionResult{Success: false, ExitCode: 2, ExitCodeSet: true, ErrorMessage: "packageId obrigatorio para acao PSADT"}
	}

	timeout := time.Duration(policy.ExecutionTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = defaultExecutionTimeout
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client, err := psadt.NewClient(
		psadt.WithTimeout(timeout),
		psadt.WithMinModuleVersion(strings.TrimSpace(policy.RequiredVersion)),
		psadt.WithLogger(slog.Default()),
	)
	if err != nil {
		return psadtExecutionErrorResult(err)
	}
	defer client.Close()

	session, err := client.OpenSessionWithContext(runCtx, pstypes.SessionConfig{
		AppVendor:           "Meduza",
		AppName:             "Discovery Agent",
		AppVersion:          "1.0.0",
		DeploymentType:      deploymentTypeForOperation(operation),
		DeployMode:          pstypes.DeployModeSilent,
		AppSuccessExitCodes: append([]int(nil), policy.SuccessExitCodes...),
		AppRebootExitCodes:  append([]int(nil), policy.RebootExitCodes...),
	})
	if err != nil {
		return psadtExecutionErrorResult(err)
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer closeCancel()
		_ = session.CloseWithContext(closeCtx, 0)
	}()

	session = session.WithContext(runCtx)

	if isMSIPackageID(id) {
		result, runErr := session.StartMsiProcess(pstypes.MsiProcessOptions{
			Action:   msiActionForOperation(operation),
			FilePath: id,
			PassThru: true,
		})
		if runErr != nil {
			return psadtExecutionErrorResult(runErr)
		}
		return executionResultFromPSADTProcess(result)
	}

	args, argErr := wingetArgsForOperation(id, operation)
	if argErr != nil {
		return ExecutionResult{Success: false, ExitCode: 2, ExitCodeSet: true, ErrorMessage: argErr.Error()}
	}

	result, runErr := session.StartProcess(pstypes.StartProcessOptions{
		FilePath:     "winget",
		ArgumentList: args,
		WindowStyle:  pstypes.WindowHidden,
		PassThru:     true,
	})
	if runErr != nil {
		return psadtExecutionErrorResult(runErr)
	}

	select {
	case <-ctx.Done():
		return ExecutionResult{Success: false, ExitCode: 1, ExitCodeSet: true, ErrorMessage: ctx.Err().Error()}
	default:
	}

	return executionResultFromPSADTProcess(result)
}

func deploymentTypeForOperation(operation string) pstypes.DeploymentType {
	switch strings.ToLower(strings.TrimSpace(operation)) {
	case "install":
		return pstypes.DeployInstall
	case "upgrade":
		return pstypes.DeployInstall
	case "uninstall":
		return pstypes.DeployUninstall
	default:
		return pstypes.DeployInstall
	}
}

func msiActionForOperation(operation string) pstypes.MsiAction {
	switch strings.ToLower(strings.TrimSpace(operation)) {
	case "uninstall":
		return pstypes.MsiUninstall
	default:
		return pstypes.MsiInstall
	}
}

func wingetArgsForOperation(packageID, operation string) ([]string, error) {
	id := strings.TrimSpace(packageID)
	if id == "" {
		return nil, fmt.Errorf("packageId obrigatorio para acao PSADT")
	}

	switch strings.ToLower(strings.TrimSpace(operation)) {
	case "install":
		return []string{"install", "--id", id, "--exact", "--silent", "--accept-source-agreements", "--accept-package-agreements"}, nil
	case "upgrade":
		return []string{"upgrade", "--id", id, "--exact", "--silent", "--accept-source-agreements", "--accept-package-agreements"}, nil
	case "uninstall":
		return []string{"uninstall", "--id", id, "--exact", "--silent"}, nil
	default:
		return nil, fmt.Errorf("operacao PSADT invalida")
	}
}

func executionResultFromPSADTProcess(result *pstypes.ProcessResult) ExecutionResult {
	if result == nil {
		return ExecutionResult{Success: true, ExitCode: 0, ExitCodeSet: true}
	}

	output := strings.TrimSpace(result.Interleaved)
	if output == "" {
		output = strings.TrimSpace(result.StdOut)
	}
	stderr := strings.TrimSpace(result.StdErr)
	if output == "" {
		output = stderr
	} else if stderr != "" {
		output = output + "\n" + stderr
	}

	exitCode := result.ExitCode
	execResult := ExecutionResult{
		Success:     exitCode == 0,
		ExitCode:    exitCode,
		ExitCodeSet: true,
		Output:      output,
	}
	if exitCode != 0 {
		execResult.ErrorMessage = output
	}
	return execResult
}

func psadtExecutionErrorResult(err error) ExecutionResult {
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		msg = "falha na execucao PSADT"
	}

	code := psadtScriptFailureExitCode
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "module") && strings.Contains(lower, "psappdeploytoolkit") {
		code = psadtImportFailureExitCode
	}

	return ExecutionResult{Success: false, ExitCode: code, ExitCodeSet: true, ErrorMessage: msg, Output: msg}
}
