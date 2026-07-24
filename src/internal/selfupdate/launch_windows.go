//go:build windows

package selfupdate

import (
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	seeMaskNoConsole    = 0x00008000
	seeMaskNoZoneChecks = 0x00800000
)

var (
	shell32             = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteExW = shell32.NewProc("ShellExecuteExW")
)

type shellExecuteInfoW struct {
	cbSize       uint32
	fMask        uint32
	hwnd         windows.HWND
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     uintptr
	lpIDList     unsafe.Pointer
	lpClass      *uint16
	hkeyClass    uintptr
	dwHotKey     uint32
	hIcon        windows.Handle
	hProcess     windows.Handle
}

func mustUTF16Ptr(s string) *uint16 {
	if s == "" {
		return nil
	}
	ptr, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return nil
	}
	return ptr
}

// LaunchInstallerElevated lanca o instalador com elevação UAC via ShellExecuteEx("runas").
// Exportado para que o callback PSADT no pacote app possa chamar a elevação UAC
// diretamente de dentro da sessão PSADT.
func LaunchInstallerElevated(exePath string, args string) error {
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exePath)
	params := mustUTF16Ptr(args)

	var sei shellExecuteInfoW
	sei.cbSize = uint32(unsafe.Sizeof(sei))
	sei.fMask = seeMaskNoConsole | seeMaskNoZoneChecks
	sei.lpVerb = verb
	sei.lpFile = file
	sei.lpParameters = params
	sei.nShow = 0 // SW_HIDE

	ret, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&sei)))
	if ret == 0 {
		if callErr != nil && callErr != windows.ERROR_CANCELLED {
			return fmt.Errorf("ShellExecuteEx runas falhou: %v", callErr)
		}
		return fmt.Errorf("ShellExecuteEx runas retornou falso (usuário pode ter negado elevação)")
	}
	return nil
}

// launchInstallerViaExec tenta o launch via exec.Command com SysProcAttr para
// elevação via Token (CreateProcessAsUser) ou DETACHED_PROCESS.
// Se falhar com "required elevation", faz fallback para ShellExecuteEx("runas").
//
// NOTA: O instalador NSIS /S /UPDATE sempre requer elevação admin porque:
// - taskkill precisa encerrar o agente atual (pode rodar como admin)
// - Escreve em C:\Program Files\Discovery\ (requer admin)
// - Registra no HKLM (requer admin)
// Por isso, tentamos ShellExecuteEx("runas") PRIMEIRO, e só usamos exec.Command
// como fallback se ShellExecuteEx falhar (ex.: UAC desabilitado ou já elevado).
func (u *Updater) launchInstaller(exePath string) error {
	exePath, err := validateExecutablePath(exePath)
	if err != nil {
		return err
	}

	// Tenta ShellExecuteEx("runas") primeiro — o instalador NSIS /S /UPDATE
	// sempre requer elevação admin.
	elevateErr := LaunchInstallerElevated(exePath, "/S /UPDATE")
	if elevateErr == nil {
		u.logf("[selfupdate] instalador iniciado com elevacao UAC: %s", exePath)
		return nil
	}

	// Se ShellExecuteEx falhou por ERROR_CANCELLED (usuário negou UAC), não tenta fallback.
	if strings.Contains(elevateErr.Error(), "negado") || strings.Contains(elevateErr.Error(), "ERROR_CANCELLED") {
		return fmt.Errorf("instalador requer elevacao administrativa (UAC negado): %w", elevateErr)
	}

	// Fallback: CreateProcess direto (funciona se o processo já está elevado).
	//
	// CRÍTICO: O instalador NSIS faz taskkill do agente. O processo instalador
	// precisa ser INDEPENDENTE para sobreviver a isso. Flags:
	//   DETACHED_PROCESS         — sem console pai
	//   CREATE_BREAKAWAY_FROM_JOB — escapa do job object do agente
	//   CREATE_NEW_PROCESS_GROUP — Ctrl+C/term não propaga para o grupo
	//   CloseHandle(Process)    — libera o handle, sem vínculo de wait
	u.logf("[selfupdate] ShellExecuteEx runas falhou (%v), tentando CreateProcess independente", elevateErr)

	argv0, argvErr := windows.UTF16PtrFromString(exePath)
	if argvErr != nil {
		return fmt.Errorf("UTF16PtrFromString: %w (ShellExecuteEx: %v)", argvErr, elevateErr)
	}

	cmdLine := windows.StringToUTF16Ptr(fmt.Sprintf(`"%s" /S /UPDATE`, exePath))

	var si windows.StartupInfo
	si.Flags = windows.STARTF_USESHOWWINDOW
	si.ShowWindow = 0 // SW_HIDE

	var pi windows.ProcessInformation
	createFlags := uint32(windows.CREATE_NO_WINDOW | windows.DETACHED_PROCESS)
	createFlags |= breakawayFromJobFlag
	createFlags |= newProcessGroupFlag

	err = windows.CreateProcess(
		argv0,
		cmdLine,
		nil, // lpProcessAttributes
		nil, // lpThreadAttributes
		false,
		createFlags,
		nil, // lpEnvironment
		nil, // lpCurrentDirectory
		&si,
		&pi,
	)
	if err != nil {
		return fmt.Errorf("CreateProcess falhou: %w (ShellExecuteEx: %v)", err, elevateErr)
	}

	// Libera handles — o instalador é independente, não precisamos esperar.
	windows.CloseHandle(pi.Thread)
	windows.CloseHandle(pi.Process)

	u.logf("[selfupdate] instalador iniciado como processo independente (PID=%d)", pi.ProcessId)
	return nil
}

func validateExecutablePath(exePath string) (string, error) {
	if exePath == "" {
		return "", fmt.Errorf("installer path vazio")
	}
	info, err := os.Stat(exePath)
	if err != nil {
		return "", fmt.Errorf("installer nao encontrado: %w", err)
	}
	// Verifica que não é diretório
	if info.IsDir() {
		return "", fmt.Errorf("installer path aponta para diretorio: %s", exePath)
	}
	return exePath, nil
}

// isElevationRequiredError detecta erros de "required elevation" / operação requer elevação.
func isElevationRequiredError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return containsAnyIgnoreCase(errStr,
		"required elevation",
		"requer elevação",
		"requires elevation",
		"elevation required",
		"740", // ERROR_ELEVATION_REQUIRED
	)
}

func containsAnyIgnoreCase(s string, substrs ...string) bool {
	lower := strings.ToLower(s)
	for _, sub := range substrs {
		if strings.Contains(lower, strings.ToLower(sub)) {
			return true
		}
	}
	return false
}
