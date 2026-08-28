//go:build windows

package automation

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
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

	cfgBuilder := pstypes.NewSessionConfig().
		App("Meduza", "Discovery Agent", "1.0.0").
		Silent().
		SuccessExitCodes(policy.SuccessExitCodes...).
		RebootExitCodes(policy.RebootExitCodes...)
	if deploymentTypeForOperation(operation) == pstypes.DeployUninstall {
		cfgBuilder = cfgBuilder.Uninstall()
	} else {
		cfgBuilder = cfgBuilder.Install()
	}

	session, err := client.OpenSessionWithContext(runCtx, cfgBuilder.Build())
	if err != nil {
		return psadtExecutionErrorResult(err)
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer closeCancel()
		_ = session.CloseWithContext(closeCtx, 0)
	}()

	session = session.WithContext(runCtx)

	// Live output streaming: consome as linhas de log do PSADT em tempo real
	// e as acumula para incluir no resultado final (visível no frontend).
	liveCh := session.LiveOutput()
	var liveMu sync.Mutex
	var liveLines []string
	stopLive := make(chan struct{})
	var liveWG sync.WaitGroup
	liveWG.Add(1)
	go func() {
		defer liveWG.Done()
		for {
			select {
			case <-stopLive:
				return
			case line, ok := <-liveCh:
				if !ok {
					return
				}
				if strings.TrimSpace(line) == "" {
					continue
				}
				liveMu.Lock()
				liveLines = append(liveLines, line)
				liveMu.Unlock()
			}
		}
	}()
	stopLiveCollect := func() {
		close(stopLive)
		liveWG.Wait()
	}
	defer stopLiveCollect()

	// Pré-condições de deploy
	if rebootInfo, err := session.GetPendingReboot(); err == nil && rebootInfo != nil && rebootInfo.IsSystemRebootPending {
		return ExecutionResult{
			Success: false, ExitCode: 1641, ExitCodeSet: true,
			ErrorMessage: "reinicializacao pendente — deploy adiado",
		}
	}

	if online, err := session.TestNetworkConnection(); err == nil && !online {
		return ExecutionResult{
			Success: false, ExitCode: 2, ExitCodeSet: true,
			ErrorMessage: "sem conectividade de rede — deploy adiado",
		}
	}

	// Deploy inteligente: em laptops, adia se estiver em bateria para evitar
	// interrupções por falta de energia durante instalações longas.
	if isLaptop, err := session.TestIsLaptop(); err == nil && isLaptop {
		if battInfo, battErr := session.TestBattery(); battErr == nil && battInfo != nil && !battInfo.IsUsingACPower {
			return ExecutionResult{
				Success: false, ExitCode: 1618, ExitCodeSet: true,
				ErrorMessage: "laptop em bateria — deploy adiado para preservar energia",
			}
		}
	}

	mergeLive := func(res ExecutionResult) ExecutionResult {
		liveMu.Lock()
		lines := append([]string(nil), liveLines...)
		liveMu.Unlock()
		if len(lines) == 0 {
			return res
		}
		streamed := strings.Join(lines, "\n")
		if strings.TrimSpace(res.Output) != "" {
			res.Output = streamed + "\n" + res.Output
		} else {
			res.Output = streamed
		}
		return res
	}

	if isMSIPackageID(id) {
		result, runErr := session.StartMsiProcess(pstypes.MsiProcessOptions{
			Action:   msiActionForOperation(operation),
			FilePath: id,
			PassThru: true,
		})
		if runErr != nil {
			return psadtExecutionErrorResult(runErr)
		}
		return mergeLive(executionResultFromPSADTProcess(result))
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

	return mergeLive(executionResultFromPSADTProcess(result))
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

	// Classificação de erros tipados (equivalente ao parser.Is* da lib,
	// que vive em internal/parser e não é importável externamente).
	switch {
	case isPSADTNetworkError(msg):
		// Sem conectividade — exit code 2 (recoverable, sem fallback de rede).
		return ExecutionResult{Success: false, ExitCode: 2, ExitCodeSet: true, ErrorMessage: "sem conectividade de rede: " + msg, Output: msg}
	case isPSADTTimeoutError(msg):
		// Timeout — exit code 1618 (recoverable, permite retry/fallback).
		return ExecutionResult{Success: false, ExitCode: 1618, ExitCodeSet: true, ErrorMessage: "timeout na execucao PSADT: " + msg, Output: msg}
	case isPSADTAccessDeniedError(msg):
		// Acesso negado — exit code 5 (fatal).
		return ExecutionResult{Success: false, ExitCode: 5, ExitCodeSet: true, ErrorMessage: "acesso negado: " + msg, Output: msg}
	case isPSADTFileNotFoundError(msg):
		// Arquivo não encontrado — exit code 2 (recoverable).
		return ExecutionResult{Success: false, ExitCode: 2, ExitCodeSet: true, ErrorMessage: "arquivo nao encontrado: " + msg, Output: msg}
	}

	return ExecutionResult{Success: false, ExitCode: code, ExitCodeSet: true, ErrorMessage: msg, Output: msg}
}

// isPSADTNetworkError detecta falhas de rede no texto do erro.
func isPSADTNetworkError(msg string) bool {
	lower := strings.ToLower(msg)
	for _, sub := range []string{
		"webexception", "httprequestexception", "network", "conexao", "connection",
		"dns", "no route", "unreachable", "timed out", "socket",
	} {
		if strings.Contains(lower, sub) {
			return true
		}
	}
	return false
}

// isPSADTTimeoutError detecta timeouts no texto do erro.
func isPSADTTimeoutError(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline exceeded") ||
		strings.Contains(lower, "context deadline")
}

// isPSADTAccessDeniedError detecta acesso negado no texto do erro.
func isPSADTAccessDeniedError(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "access denied") || strings.Contains(lower, "accessdenied") ||
		strings.Contains(lower, "unauthorized") || strings.Contains(lower, "acesso negado") ||
		strings.Contains(lower, "permission denied")
}

// isPSADTFileNotFoundError detecta arquivo não encontrado no texto do erro.
func isPSADTFileNotFoundError(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "filenotfoundexception") || strings.Contains(lower, "itemnotfoundexception") ||
		strings.Contains(lower, "file not found") || strings.Contains(lower, "does not exist") ||
		strings.Contains(lower, "nao encontrado") || strings.Contains(lower, "não encontrado")
}
