//go:build windows

package winregistry

import (
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// ─────────────────────────────────────────────────────────────────────────────
// GRAVACAO COM TIPO CRU
//
// O pacote golang.org/x/sys/windows/registry expoe apenas setters TIPADOS
// (SetStringValue, SetDWordValue, ...) e NAO expoe o ID cru do Windows. Isso
// cria um problema real na COPIA de subarvore (usada por rename e export):
// um valor REG_RESOURCE_LIST (id 8) nao tem setter tipado e, ao cair no caminho
// binario, era gravado como REG_BINARY (id 3) — o TIPO do valor mudava em
// silencio, corrompendo a chave.
//
// Esta funcao chama RegSetValueExW direto para preservar o tipo original.
// E o unico ponto do pacote que usa syscall crua, e esta isolado aqui de
// proposito: o resto (inclusive o guard) permanece puro e testavel.
// ─────────────────────────────────────────────────────────────────────────────

var (
	modAdvapi32        = windows.NewLazySystemDLL("advapi32.dll")
	procRegSetValueExW = modAdvapi32.NewProc("RegSetValueExW")
)

// setValueRawType grava (name, typeID, data) preservando o tipo original.
func setValueRawType(key registry.Key, name string, typeID uint32, data []byte) error {
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}

	var dataPtr *byte
	if len(data) > 0 {
		dataPtr = &data[0]
	}

	ret, _, _ := procRegSetValueExW.Call(
		uintptr(key),
		uintptr(unsafe.Pointer(namePtr)),
		0, // Reserved
		uintptr(typeID),
		uintptr(unsafe.Pointer(dataPtr)),
		uintptr(len(data)),
	)
	if ret != 0 {
		return windows.Errno(ret)
	}
	return nil
}
