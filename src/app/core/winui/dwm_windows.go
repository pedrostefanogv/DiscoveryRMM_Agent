//go:build windows

package winui

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// ── Cantos arredondados do compositor (DWM) ──
//
// SetWindowRgn recorta a janela com uma região binária: o contorno do canto
// fica serrilhado (sem antialiasing) e a sombra acompanha esse recorte. No
// Windows 11 o DWM arredonda a janela com antialiasing; este helper pede esse
// arredondamento e devolve true quando o compositor aceitou o pedido — nesse
// caso a janela NÃO deve receber uma região de recorte.

var (
	dwmapi                      = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetWindowAttribute   = dwmapi.NewProc("DwmSetWindowAttribute")
	procDwmIsCompositionEnabled = dwmapi.NewProc("DwmIsCompositionEnabled")
)

const (
	// dwmwaWindowCornerPreference é DWMWA_WINDOW_CORNER_PREFERENCE (Windows 11).
	dwmwaWindowCornerPreference = 33
	// dwmWindowCornerRound é DWMWCP_ROUND (raio padrão do sistema).
	dwmWindowCornerRound = 2
)

// SetSystemRoundedCorners pede ao DWM para arredondar os cantos da janela com
// antialiasing. Retorna false em Windows sem suporte (Windows 10 e anteriores):
// o chamador deve então usar SetRoundedRegion como fallback.
func SetSystemRoundedCorners(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	if err := procDwmSetWindowAttribute.Find(); err != nil {
		return false
	}
	// Sem composição DWM (alguns VMs/sessões) o atributo não tem efeito:
	// melhor cair na região binária, que ao menos arredonda.
	var composition int32
	if ret, _, _ := procDwmIsCompositionEnabled.Call(uintptr(unsafe.Pointer(&composition))); ret != 0 || composition == 0 {
		return false
	}
	preference := int32(dwmWindowCornerRound)
	ret, _, _ := procDwmSetWindowAttribute.Call(
		hwnd,
		uintptr(dwmwaWindowCornerPreference),
		uintptr(unsafe.Pointer(&preference)),
		unsafe.Sizeof(preference),
	)
	// S_OK (0) = compositor aplicou o arredondamento.
	return ret == 0
}
