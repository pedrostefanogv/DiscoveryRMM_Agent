//go:build windows

package winui

import (
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ── Ícones de executáveis (lista de processos do Welcome) ──
//
// O prompt nativo de automação mostra os programas que serão fechados com o
// MESMO ícone que o Windows associa ao executável (SHGetFileInfoW lê o ícone
// direto da imagem no disco, sem depender da janela do processo).

var shell32 = windows.NewLazySystemDLL("shell32.dll")

var (
	procSHGetFileInfoW = shell32.NewProc("SHGetFileInfoW")
	procDrawIconEx     = user32.NewProc("DrawIconEx")
	procDestroyIcon    = user32.NewProc("DestroyIcon")
)

const (
	shgfiIcon      = 0x000000100
	shgfiLargeIcon = 0x000000000
	diNormal       = 0x3
)

// shFileInfoW espelha SHFILEINFOW do shell32.
type shFileInfoW struct {
	HIcon         uintptr
	ISysIconIndex int32
	DwAttributes  uint32
	SzDisplayName [260]uint16
	SzTypeName    [80]uint16
}

// ExtractFileIcon devolve o HICON (32px) associado ao executável informado.
// ok=false quando o caminho é vazio, o arquivo não existe ou não tem ícone —
// o chamador usa um fallback vetorial.
func ExtractFileIcon(imagePath string) (uintptr, bool) {
	trimmed := strings.TrimSpace(imagePath)
	if trimmed == "" {
		return 0, false
	}
	path, err := syscall.UTF16PtrFromString(trimmed)
	if err != nil {
		return 0, false
	}
	var info shFileInfoW
	r, _, _ := procSHGetFileInfoW.Call(
		uintptr(unsafe.Pointer(path)),
		0,
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
		shgfiIcon|shgfiLargeIcon,
	)
	if r == 0 || info.HIcon == 0 {
		return 0, false
	}
	return info.HIcon, true
}

// DrawIconAt desenha o HICON escalado ao retângulo (DrawIconEx com DI_NORMAL).
func DrawIconAt(hdc, hIcon uintptr, rect Rect) {
	if hIcon == 0 || rect.Empty() {
		return
	}
	procDrawIconEx.Call(
		hdc,
		uintptr(rect.X), uintptr(rect.Y),
		hIcon,
		uintptr(rect.W), uintptr(rect.H),
		0, 0, diNormal,
	)
}

// DestroyIconHandle libera o HICON devolvido por ExtractFileIcon.
func DestroyIconHandle(hIcon uintptr) {
	if hIcon != 0 {
		procDestroyIcon.Call(hIcon)
	}
}
