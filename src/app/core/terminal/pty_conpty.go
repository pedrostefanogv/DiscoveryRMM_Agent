//go:build windows

package terminal

import (
	"fmt"
	"syscall"
	"unsafe"
)

// ── ConPTY Windows API (kernel32.dll) ──
//
// CreatePseudoConsole está disponível a partir do Windows 10 1809 (build 17763).
// https://docs.microsoft.com/en-us/windows/console/createpseudoconsole

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procCreatePseudoConsole = kernel32.NewProc("CreatePseudoConsole")
	procResizePseudoConsole = kernel32.NewProc("ResizePseudoConsole")
	procClosePseudoConsole  = kernel32.NewProc("ClosePseudoConsole")

	procInitializeProcThreadAttributeList = kernel32.NewProc("InitializeProcThreadAttributeList")
	procUpdateProcThreadAttribute         = kernel32.NewProc("UpdateProcThreadAttribute")
	procDeleteProcThreadAttributeList     = kernel32.NewProc("DeleteProcThreadAttributeList")
)

// HPCON é um handle de pseudo console (opaco).
type HPCON uintptr

// COORD é a estrutura de coordenadas do Windows.
type COORD struct {
	X int16
	Y int16
}

// Constantes para CreatePseudoConsole.
const (
	// PSEUDOCONSOLE_PIPE_READ / WRITE — flags internas, passamos 0 (PSEUDOCONSOLE_RESIZE_QUIRK não é necessário).
	PSEUDOCONSOLE_FLAG_NONE = 0x0

	// PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE — atributo para STARTUPINFOEX.
	PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE = 0x00020016
)

// CreatePseudoConsole cria um pseudo console com as dimensões especificadas.
//
// Parâmetros:
//   - size: COORD{X: cols, Y: rows} — tamanho inicial do console
//   - hInput: handle de leitura do pipe de entrada (stdin do processo filho)
//   - hOutput: handle de escrita do pipe de saída (stdout do processo filho)
//
// Retorna o HPCON e qualquer erro.
func CreatePseudoConsole(size COORD, hInput, hOutput syscall.Handle) (HPCON, error) {
	var hpc HPCON

	r1, _, e1 := procCreatePseudoConsole.Call(
		uintptr(unsafe.Pointer(&size)),
		uintptr(hInput),
		uintptr(hOutput),
		uintptr(PSEUDOCONSOLE_FLAG_NONE),
		uintptr(unsafe.Pointer(&hpc)),
	)

	if r1 != 0 { // HRESULT != S_OK
		return 0, fmt.Errorf("CreatePseudoConsole failed: HRESULT=0x%x (%w)", r1, e1)
	}

	return hpc, nil
}

// ResizePseudoConsole redimensiona o pseudo console.
func ResizePseudoConsole(hpc HPCON, size COORD) error {
	r1, _, e1 := procResizePseudoConsole.Call(
		uintptr(hpc),
		uintptr(unsafe.Pointer(&size)),
	)

	if r1 != 0 {
		return fmt.Errorf("ResizePseudoConsole failed: HRESULT=0x%x (%w)", r1, e1)
	}

	return nil
}

// ClosePseudoConsole fecha o pseudo console e libera recursos.
func ClosePseudoConsole(hpc HPCON) {
	procClosePseudoConsole.Call(uintptr(hpc))
}

// initializeProcThreadAttributeList envolve InitializeProcThreadAttributeList.
// A primeira chamada (list == nil) retorna false e preenche *size com o tamanho
// necessário — comportamento esperado (ERROR_INSUFFICIENT_BUFFER).
func initializeProcThreadAttributeList(list unsafe.Pointer, count, flags uint32, size *uintptr) error {
	r1, _, e1 := procInitializeProcThreadAttributeList.Call(
		uintptr(list),
		uintptr(count),
		uintptr(flags),
		uintptr(unsafe.Pointer(size)),
	)
	if r1 == 0 {
		return e1
	}
	return nil
}

// updateProcThreadAttribute envolve UpdateProcThreadAttribute.
func updateProcThreadAttribute(list unsafe.Pointer, flags, attr, value, size uintptr, prevValue unsafe.Pointer, returnedSize *uintptr) error {
	r1, _, e1 := procUpdateProcThreadAttribute.Call(
		uintptr(list),
		flags,
		attr,
		value,
		size,
		uintptr(prevValue),
		uintptr(unsafe.Pointer(returnedSize)),
	)
	if r1 == 0 {
		return e1
	}
	return nil
}

// deleteProcThreadAttributeList envolve DeleteProcThreadAttributeList.
func deleteProcThreadAttributeList(list unsafe.Pointer) {
	procDeleteProcThreadAttributeList.Call(uintptr(list))
}

// IsConPTYAvailable verifica se a API ConPTY está disponível no sistema.
// Requer Windows 10 1809 (build 17763) ou superior.
func IsConPTYAvailable() bool {
	return procCreatePseudoConsole.Find() == nil &&
		procResizePseudoConsole.Find() == nil &&
		procClosePseudoConsole.Find() == nil &&
		procInitializeProcThreadAttributeList.Find() == nil &&
		procUpdateProcThreadAttribute.Find() == nil
}
