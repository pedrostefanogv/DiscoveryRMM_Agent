//go:build windows

package windowsupdate

import (
	"errors"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"discovery/app/core/inventory/native"
)

// Valores de RESTOREPOINTINFO (srrestoreptapi.h).
const (
	srpEventBeginSystemChange         = 0
	srpEventEndSystemChange           = 1
	srpRestorePointApplicationInstall = 1
	restorePointDescriptionMax        = 256
)

// errRestorePointSkippedServer indica que o checkpoint foi pulado por ser
// Windows Server (System Restore não é confiável ali) — não é falha.
var errRestorePointSkippedServer = errors.New("restore point pulado em Windows Server")

type restorePointInfo struct {
	EventType        uint32
	RestorePointType uint32
	Description      [restorePointDescriptionMax]uint16
}

var (
	srclientDLL            = windows.NewLazySystemDLL("srclient.dll")
	procSRSetRestorePointW = srclientDLL.NewProc("SRSetRestorePointW")
)

// isWindowsServer informa se a máquina roda Windows Server.
//
// A decisão é DELEGADA à detecção canônica do inventário
// (native.IsWindowsServer), que consulta ProductType em
// HKLM\SYSTEM\CurrentControlSet\Control\ProductOptions — fonte estável e
// independente do idioma. A heurística antiga (substring "server" em
// InstallationType/ProductName) foi removida daqui para existir uma única
// definição de "é servidor" no agente.
func isWindowsServer() bool {
	return native.IsWindowsServer()
}

// createRestorePoint cria UM checkpoint de sistema antes da instalacao.
//
// SRSetRestorePoint exige o par BEGIN/END: sem o END o Windows mantem a
// transacao aberta. Falha aqui e NAO-FATAL (o chamador apenas registra): um
// checkpoint indisponivel nao pode impedir a correcao de seguranca.
func createRestorePoint(description string) error {
	if isWindowsServer() {
		return errRestorePointSkippedServer
	}

	var info restorePointInfo
	info.EventType = srpEventBeginSystemChange
	info.RestorePointType = srpRestorePointApplicationInstall
	encoded := windows.StringToUTF16(description)
	if len(encoded) > restorePointDescriptionMax {
		encoded = encoded[:restorePointDescriptionMax-1]
		encoded[restorePointDescriptionMax-1] = 0
	}
	copy(info.Description[:], encoded)

	var result uint32
	status, _, callErr := procSRSetRestorePointW.Call(
		uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Pointer(&result)))
	if status == 0 {
		if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
			return callErr
		}
		return errors.New("SRSetRestorePoint(BEGIN) falhou")
	}

	info.EventType = srpEventEndSystemChange
	status, _, callErr = procSRSetRestorePointW.Call(
		uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Pointer(&result)))
	if status == 0 {
		if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
			return callErr
		}
		return errors.New("SRSetRestorePoint(END) falhou")
	}
	return nil
}
