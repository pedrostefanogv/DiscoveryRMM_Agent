//go:build windows

package app

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Valores do enum FIRMWARE_TYPE retornado por GetFirmwareType (kernel32).
const (
	firmwareTypeUnknown = 0
	firmwareTypeBios    = 1
	firmwareTypeUefi    = 2
)

// probeFirmwareBootSupport informa se o reinício direto no firmware
// (`shutdown /r /fw`) é suportado nesta máquina. Retorna (true, "") quando é
// UEFI; caso contrário (false, motivo).
func probeFirmwareBootSupport() (bool, string) {
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	proc := kernel32.NewProc("GetFirmwareType")
	if err := proc.Find(); err != nil {
		return false, "GetFirmwareType indisponível neste Windows"
	}

	var firmwareType uint32
	ret, _, callErr := proc.Call(uintptr(unsafe.Pointer(&firmwareType)))
	if ret == 0 {
		return false, fmt.Sprintf("não foi possível detectar o tipo de firmware: %v", callErr)
	}

	switch firmwareType {
	case firmwareTypeUefi:
		return true, ""
	case firmwareTypeBios:
		return false, "computador em BIOS legado (Legacy/CSM); o reinício no firmware exige boot UEFI"
	default:
		return false, "tipo de firmware não suportado para reinício direto no setup"
	}
}
