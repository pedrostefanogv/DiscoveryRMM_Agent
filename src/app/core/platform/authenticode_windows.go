//go:build windows

package platform

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modWintrust        = windows.NewLazySystemDLL("wintrust.dll")
	procWinVerifyTrust = modWintrust.NewProc("WinVerifyTrust")

	// WINTRUST_ACTION_GENERIC_VERIFY_V2: {00AAC56B-CD44-11d0-8CC2-00C04FC295EE}
	wintrustActionGenericVerifyV2 = windows.GUID{
		Data1: 0x00AAC56B,
		Data2: 0xCD44,
		Data3: 0x11d0,
		Data4: [8]byte{0x8C, 0xC2, 0x00, 0xC0, 0x4F, 0xC2, 0x95, 0xEE},
	}
)

const (
	wtdUINone               = 2
	wtdRevokeNone           = 0
	wtdChoiceFile           = 1
	wtdStateActionVerify    = 1
	wtdStateActionClose     = 2
	wtdCacheOnlyURLRetrieve = 0x00001000
	invalidHandleValue      = ^uintptr(0)
)

type winTrustFileInfo struct {
	cbStruct       uint32
	pcwszFilePath  *uint16
	hFile          windows.Handle
	pgKnownSubject *windows.GUID
}

type winTrustData struct {
	cbStruct            uint32
	pPolicyCallbackData uintptr
	pSIPClientData      uintptr
	dwUIChoice          uint32
	fdwRevocationChecks uint32
	dwUnionChoice       uint32
	pInfo               uintptr
	dwStateAction       uint32
	hWVTStateData       windows.Handle
	pwszURLReference    *uint16
	dwProvFlags         uint32
	dwUIContext         uint32
	pSignatureSettings  uintptr
}

// VerifyAuthenticode verifica a assinatura Authenticode PE embutida de um arquivo
// usando WinVerifyTrust (wintrust.dll), sem spawnar PowerShell.
func VerifyAuthenticode(path string) error {
	clean := strings.TrimSpace(path)
	if clean == "" {
		return errors.New("caminho vazio para verificacao Authenticode")
	}
	info, err := os.Stat(clean)
	if err != nil {
		return fmt.Errorf("arquivo inacessivel para Authenticode: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("caminho Authenticode aponta para diretorio: %s", clean)
	}

	pathUTF16, err := windows.UTF16PtrFromString(clean)
	if err != nil {
		return fmt.Errorf("caminho invalido para Authenticode: %w", err)
	}

	fileInfo := winTrustFileInfo{
		cbStruct:      uint32(unsafe.Sizeof(winTrustFileInfo{})),
		pcwszFilePath: pathUTF16,
	}
	trustData := winTrustData{
		cbStruct:            uint32(unsafe.Sizeof(winTrustData{})),
		dwUIChoice:          wtdUINone,
		fdwRevocationChecks: wtdRevokeNone,
		dwUnionChoice:       wtdChoiceFile,
		pInfo:               uintptr(unsafe.Pointer(&fileInfo)),
		dwStateAction:       wtdStateActionVerify,
		dwProvFlags:         wtdCacheOnlyURLRetrieve,
	}
	action := wintrustActionGenericVerifyV2

	var pinner runtime.Pinner
	pinner.Pin(pathUTF16)
	pinner.Pin(&fileInfo)
	pinner.Pin(&trustData)
	pinner.Pin(&action)
	defer pinner.Unpin()

	ret, _, callErr := procWinVerifyTrust.Call(
		invalidHandleValue,
		uintptr(unsafe.Pointer(&action)),
		uintptr(unsafe.Pointer(&trustData)),
	)

	// Sempre fecha o state handle se aberto.
	trustData.dwStateAction = wtdStateActionClose
	_, _, _ = procWinVerifyTrust.Call(
		invalidHandleValue,
		uintptr(unsafe.Pointer(&action)),
		uintptr(unsafe.Pointer(&trustData)),
	)

	status := uint32(ret)
	if status != 0 {
		if callErr != nil && callErr != windows.ERROR_SUCCESS && ret == 0 {
			return fmt.Errorf("WinVerifyTrust falhou: %w", callErr)
		}
		return fmt.Errorf("assinatura Authenticode invalida ou ausente (status=0x%08x)", status)
	}
	return nil
}
