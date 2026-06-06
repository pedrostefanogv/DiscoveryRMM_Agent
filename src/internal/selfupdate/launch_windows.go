//go:build windows

package selfupdate

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"unsafe"

	"discovery/internal/processutil"

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

// launchInstallerElevated lanca o instalador com elevação UAC via ShellExecuteEx("runas").
// Este é o fallback quando exec.Command falha com "required elevation".
// Retorna nil em caso de sucesso (o processo foi iniciado com UAC).
func launchInstallerElevated(exePath string, args string) error {
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
func (u *Updater) launchInstaller(exePath string) error {
	exePath, err := validateExecutablePath(exePath)
	if err != nil {
		return err
	}

	// Primeiro tenta via exec.Command (funciona se já elevado ou sem exigência UAC)
	cmd := exec.Command(exePath, "/S", "/UPDATE")
	processutil.HideWindow(cmd)
	if cmd.SysProcAttr != nil {
		setSysProcCreationFlags(cmd.SysProcAttr, detachedProcessFlag)
	}
	startErr := cmd.Start()
	if startErr == nil {
		return nil
	}

	// Se falhou por elevação, tenta via ShellExecuteEx("runas")
	if isElevationRequiredError(startErr) {
		u.logf("[selfupdate] launch via exec.Command falhou (requer elevacao), tentando ShellExecuteEx runas: %s", exePath)
		return launchInstallerElevated(exePath, "/S /UPDATE")
	}

	return startErr
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
