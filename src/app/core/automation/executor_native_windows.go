//go:build windows

package automation

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"os/exec"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"discovery/app/core/processutil"
)

// ── Executor de deploy 100% NATIVO (substituição do PSADT) ──
//
// A função mantém o nome executePSADTWithLibrary por compatibilidade com o
// executor (executor_psadt.go), mas não usa mais a lib go-psadt nem o módulo
// PSAppDeployToolkit: pré-checas (reboot pendente, rede, bateria) são lidas
// direto do SO e a execução roda msiexec.exe/winget.exe com kill de árvore
// via Win32 Job Object (equivalente ao Abort() da lib).

// executePSADTWithLibrary executa o deploy do pacote (MSI ou winget) com
// pré-checas nativas e captura de stdout/stderr.
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

	// Pré-condições de deploy (equivalentes às da sessão go-psadt).
	if rebootPendingNative() {
		return ExecutionResult{
			Success: false, ExitCode: 1641, ExitCodeSet: true,
			ErrorMessage: "reinicializacao pendente — deploy adiado",
		}
	}
	if !networkOnlineNative() {
		return ExecutionResult{
			Success: false, ExitCode: 2, ExitCodeSet: true,
			ErrorMessage: "sem conectividade de rede — deploy adiado",
		}
	}
	// Deploy inteligente: em laptops na bateria, adia para preservar energia.
	if onBattery, hasBattery := powerStatusNative(); hasBattery && onBattery {
		return ExecutionResult{
			Success: false, ExitCode: 1618, ExitCodeSet: true,
			ErrorMessage: "laptop em bateria — deploy adiado para preservar energia",
		}
	}

	var cmd *exec.Cmd
	switch {
	case isMSIPackageID(id):
		action := "/i"
		if strings.EqualFold(strings.TrimSpace(operation), "uninstall") {
			action = "/x"
		}
		// /qn silencioso; /norestart evita reboot inesperado no meio do deploy
		// (3010/1641 são tratados pela classificação de exit codes).
		cmd = exec.CommandContext(runCtx, "msiexec.exe", action, id, "/qn", "/norestart")
	default:
		args, argErr := wingetArgsForOperation(id, operation)
		if argErr != nil {
			return ExecutionResult{Success: false, ExitCode: 2, ExitCodeSet: true, ErrorMessage: argErr.Error()}
		}
		cmd = exec.CommandContext(runCtx, "winget", args...)
	}
	processutil.HideWindow(cmd)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	job, err := processutil.NewJobObject()
	if err != nil {
		slog.Warn("executor nativo: job object indisponivel (kill de arvore limitado ao processo principal)", "error", err)
		job = nil
	}

	if err := cmd.Start(); err != nil {
		return ExecutionResult{Success: false, ExitCode: 1, ExitCodeSet: true, ErrorMessage: err.Error()}
	}
	if job != nil {
		if assignErr := job.Assign(cmd.Process); assignErr != nil {
			slog.Warn("executor nativo: falha ao colocar processo no job", "error", assignErr)
		}
	}
	// Cancelamento/timeout: mata a ARVORE inteira (instalador + filhos).
	go func() {
		<-runCtx.Done()
		if job != nil {
			_ = job.Terminate()
		}
	}()

	waitErr := cmd.Wait()
	output := strings.TrimSpace(stdout.String())
	if stderrText := strings.TrimSpace(stderr.String()); stderrText != "" {
		if output == "" {
			output = stderrText
		} else {
			output = output + "\n" + stderrText
		}
	}

	exitCode := 0
	exitCodeSet := false
	success := true
	errorMessage := ""
	switch {
	case waitErr == nil:
	case runCtx.Err() != nil:
		// Timeout/cancelamento: arvore morta pelo job.
		success = false
		exitCode = 1
		exitCodeSet = true
		errorMessage = fmt.Sprintf("timeout: %v", runCtx.Err())
	default:
		success = false
		exitCodeSet = true
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
		errorMessage = waitErr.Error()
	}

	if job != nil {
		job.Close()
	}

	res := ExecutionResult{
		Success:      success,
		ExitCode:     exitCode,
		ExitCodeSet:  exitCodeSet,
		Output:       output,
		ErrorMessage: errorMessage,
	}
	// Sucesso sem output: garante algo informativo para o frontend.
	if res.Success && strings.TrimSpace(res.Output) == "" {
		res.Output = "[native] deploy concluido (exit 0, sem output)"
	}
	return res
}

// wingetArgsForOperation monta os argumentos winget para a operação
// (install/upgrade/uninstall) — movido do provider antigo go-psadt.
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

// rebootPendingNative verifica no registro se há reinicialização pendente
// (CBS, Windows Update e PendingFileRenameOperations).
func rebootPendingNative() bool {
	keys := []string{
		"SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\WindowsUpdate\\Auto Update\\RebootRequired",
		"SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Component Based Servicing\\RebootPending",
	}
	for _, path := range keys {
		key, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE)
		if err == nil {
			key.Close()
			return true
		}
	}
	sm, err := registry.OpenKey(registry.LOCAL_MACHINE, "SYSTEM\\CurrentControlSet\\Control\\Session Manager", registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer sm.Close()
	vals, err := sm.ReadValueNames(-1)
	if err != nil {
		return false
	}
	for _, name := range vals {
		if strings.EqualFold(name, "PendingFileRenameOperations") {
			return true
		}
	}
	return false
}

// networkOnlineNative sonda conectividade com DNS públicos.
func networkOnlineNative() bool {
	probes := []string{"1.1.1.1:53", "8.8.8.8:53"}
	for _, addr := range probes {
		conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
		if err == nil {
			_ = conn.Close()
			return true
		}
	}
	return false
}

// systemPowerStatus espelha SYSTEMPOWERSTATUS (kernel32).
type systemPowerStatus struct {
	ACLineStatus        byte
	BatteryFlag         byte
	BatteryLifePercent  byte
	Reserved1           byte
	BatteryLifeTime     uint32
	BatteryFullLifeTime uint32
}

var procGetSystemPowerStatus = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetSystemPowerStatus")

// powerStatusNative devolve (emBateria, temBateria) via GetSystemPowerStatus.
// Sem bateria (desktop) temBateria=false — nunca adia por energia.
func powerStatusNative() (onBattery, hasBattery bool) {
	var ps systemPowerStatus
	ret, _, _ := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&ps)))
	if ret == 0 {
		return false, false
	}
	// ACLineStatus: 0 = offline (bateria), 1 = AC, 255 = desconhecido.
	hasBattery = ps.BatteryFlag != 128 // 128 = NO SYSTEM BATTERY
	onBattery = ps.ACLineStatus == 0
	return onBattery, hasBattery
}