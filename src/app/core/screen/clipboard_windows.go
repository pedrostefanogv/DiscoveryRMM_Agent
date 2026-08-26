//go:build windows

package screen

import (
	"fmt"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

// Clipboard de texto (CF_UNICODETEXT) — usado pela sessão remota de tela para
// copiar/colar texto entre o viewer e a máquina remota (somente texto, sem
// arquivos/binários).

var (
	kernel32Clip = syscall.NewLazyDLL("kernel32.dll")

	procGlobalAlloc  = kernel32Clip.NewProc("GlobalAlloc")
	procGlobalFree   = kernel32Clip.NewProc("GlobalFree")
	procGlobalLock   = kernel32Clip.NewProc("GlobalLock")
	procGlobalUnlock = kernel32Clip.NewProc("GlobalUnlock")

	procOpenClipboard          = user32.NewProc("OpenClipboard")
	procCloseClipboard         = user32.NewProc("CloseClipboard")
	procEmptyClipboard         = user32.NewProc("EmptyClipboard")
	procSetClipboardData       = user32.NewProc("SetClipboardData")
	procGetClipboardData       = user32.NewProc("GetClipboardData")
	procIsClipboardFormatAvail = user32.NewProc("IsClipboardFormatAvailable")
)

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

// SetClipboardText coloca o texto no clipboard do Windows.
// Após SetClipboardData, o sistema passa a ser dono da memória global — não
// liberar o handle.
//
//go:nocheckptr
func SetClipboardText(text string) error {
	utf16Str, err := syscall.UTF16FromString(text)
	if err != nil {
		return fmt.Errorf("UTF16FromString: %w", err)
	}
	byteLen := len(utf16Str) * 2
	if byteLen == 0 {
		utf16Str = []uint16{0}
		byteLen = 2
	}

	if r, _, _ := procOpenClipboard.Call(0); r == 0 {
		return fmt.Errorf("OpenClipboard falhou")
	}
	defer procCloseClipboard.Call()

	if r, _, _ := procEmptyClipboard.Call(); r == 0 {
		return fmt.Errorf("EmptyClipboard falhou")
	}

	hMem, _, _ := procGlobalAlloc.Call(gmemMoveable, uintptr(byteLen))
	if hMem == 0 {
		return fmt.Errorf("GlobalAlloc falhou")
	}

	p, _, _ := syscall.Syscall(procGlobalLock.Addr(), 1, hMem, 0, 0)
	if p == 0 {
		return fmt.Errorf("GlobalLock falhou")
	}
	// unsafe.Add(nil, p) reconstrói o ponteiro HGLOBAL (endereço 0 + offset)
	// sem a conversão uintptr→unsafe.Pointer (evita go vet unsafeptr).
	dst := unsafe.Slice((*uint16)(unsafe.Add(unsafe.Pointer(nil), p)), len(utf16Str))
	copy(dst, utf16Str)
	syscall.Syscall(procGlobalUnlock.Addr(), 1, hMem, 0, 0)

	if r, _, _ := procSetClipboardData.Call(cfUnicodeText, hMem); r == 0 {
		procGlobalFree.Call(hMem)
		return fmt.Errorf("SetClipboardData falhou")
	}
	return nil
}

// GetClipboardText lê o texto atual do clipboard do Windows.
// Retorna "" (sem erro) quando o clipboard não contém texto.
//
//go:nocheckptr
func GetClipboardText() (string, error) {
	if r, _, _ := procOpenClipboard.Call(0); r == 0 {
		return "", fmt.Errorf("OpenClipboard falhou")
	}
	defer procCloseClipboard.Call()

	if r, _, _ := procIsClipboardFormatAvail.Call(cfUnicodeText); r == 0 {
		return "", nil // clipboard vazio ou sem texto — não é erro
	}

	hData, _, _ := procGetClipboardData.Call(cfUnicodeText)
	if hData == 0 {
		return "", fmt.Errorf("GetClipboardData falhou")
	}

	p, _, _ := syscall.Syscall(procGlobalLock.Addr(), 1, hData, 0, 0)
	if p == 0 {
		return "", fmt.Errorf("GlobalLock falhou")
	}
	defer syscall.Syscall(procGlobalUnlock.Addr(), 1, hData, 0, 0)

	// Lê UTF-16 até o terminador nulo (limite de segurança 1 Mi chars).
	runes := make([]uint16, 0, 256)
	// unsafe.Add(nil, p) reconstrói o ponteiro HGLOBAL (endereço 0 + offset)
	// sem a conversão uintptr→unsafe.Pointer (evita go vet unsafeptr).
	base := unsafe.Add(unsafe.Pointer(nil), p)
	for i := 0; i < 1<<20; i++ {
		u := *(*uint16)(unsafe.Add(base, uintptr(i*2)))
		if u == 0 {
			break
		}
		runes = append(runes, u)
	}
	return string(utf16.Decode(runes)), nil
}
