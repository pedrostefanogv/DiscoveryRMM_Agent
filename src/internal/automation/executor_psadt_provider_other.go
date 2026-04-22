//go:build !windows

package automation

import "context"

func executePSADTWithLibrary(_ context.Context, _, _ string, _ PSADTPolicy) ExecutionResult {
	return ExecutionResult{Success: false, ExitCode: 126, ExitCodeSet: true, ErrorMessage: "PSAppDeployToolkit suportado apenas no Windows"}
}
