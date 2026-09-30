//go:build windows

package app

import (
	"fmt"
	"syscall"
	"unsafe"
)

// ── Overlay de captura: janela principal cobrindo o desktop virtual ────────
//
// Em vez de criar uma segunda janela Wails (custosa e com estado próprio), o
// overlay reaproveita a janela principal: ela é redimensionada (coordenadas
// FÍSICAS, DPI-aware pelo manifest PerMonitorV2) para cobrir todo o desktop
// virtual, sempre-no-topo, e o frontend desenha a tela congelada por cima.

const (
	hwndTopMost    = ^uintptr(0) // HWND_TOPMOST (-1)
	hwndNotTopMost = ^uintptr(1) // HWND_NOTOPMOST (-2)
	swpShowWindow  = 0x0040
	swpNoActivate  = 0x0010
)

type overlayRect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

var (
	user32Overlay      = syscall.NewLazyDLL("user32.dll")
	procGetWindowRectO = user32Overlay.NewProc("GetWindowRect")
	procSetWindowPosO  = user32Overlay.NewProc("SetWindowPos")
)

// enterScreenshotOverlayBounds posiciona a janela principal sobre o desktop
// virtual e devolve a função de restauração.
func (a *App) enterScreenshotOverlayBounds(x, y, w, h int) (func(), error) {
	win := a.mainWindow
	if win == nil {
		return nil, fmt.Errorf("janela principal indisponivel")
	}
	hwnd := uintptr(win.NativeWindow())
	if hwnd == 0 {
		return nil, fmt.Errorf("handle nativo da janela indisponivel")
	}
	var prev overlayRect
	procGetWindowRectO.Call(hwnd, uintptr(unsafe.Pointer(&prev)))
	wasMaximised := win.IsMaximised()
	if win.IsMinimised() {
		win.UnMinimise()
	}
	win.UnMaximise()
	win.SetAlwaysOnTop(true)
	ret, _, callErr := procSetWindowPosO.Call(hwnd, hwndTopMost,
		uintptr(int(x)), uintptr(int(y)), uintptr(w), uintptr(h), swpShowWindow|swpNoActivate)
	if ret == 0 {
		return nil, fmt.Errorf("SetWindowPos falhou ao preparar o overlay: %v", callErr)
	}
	restore := func() {
		procSetWindowPosO.Call(hwnd, hwndNotTopMost,
			uintptr(int(prev.Left)), uintptr(int(prev.Top)),
			uintptr(int(prev.Right-prev.Left)), uintptr(int(prev.Bottom-prev.Top)), swpShowWindow)
		win.SetAlwaysOnTop(false)
		if wasMaximised {
			win.Maximise()
		}
	}
	return restore, nil
}
