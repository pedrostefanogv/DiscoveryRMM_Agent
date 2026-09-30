//go:build windows

package app

import (
	"fmt"
	"syscall"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
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
	// Durante o overlay a janela cobre o desktop virtual inteiro (inclusive
	// taskbar): sem moldura para o webview coincidir com a tela e sem o clamp de
	// WorkArea (ver FitWindowToWorkArea). Ambas as mudanças são restauradas no
	// fim da sessão.
	a.screenshotOverlayActive.Store(true)
	win.SetFrameless(true)
	win.SetAlwaysOnTop(true)
	ret, _, callErr := procSetWindowPosO.Call(hwnd, hwndTopMost,
		uintptr(int(x)), uintptr(int(y)), uintptr(w), uintptr(h), swpShowWindow|swpNoActivate)
	if ret == 0 {
		win.SetFrameless(a.mainWindowFrameless)
		a.screenshotOverlayActive.Store(false)
		return nil, fmt.Errorf("SetWindowPos falhou ao preparar o overlay: %v", callErr)
	}

	// Se a janela principal for fechada (close-to-tray) ou ocultada durante o
	// overlay, a sessão precisa ser encerrada e os bounds restaurados — senão ao
	// reabrir a janela ela reapareceria cobrindo o desktop virtual. O abort roda
	// em goroutine para não fazer chamadas Wails dentro do callback de evento.
	unregisterClose := win.RegisterHook(events.Common.WindowClosing, func(*application.WindowEvent) {
		go a.abortScreenshotOverlay("janela principal fechada")
	})
	unregisterHide := win.RegisterHook(events.Common.WindowHide, func(*application.WindowEvent) {
		go a.abortScreenshotOverlay("janela principal ocultada")
	})

	restore := func() {
		unregisterClose()
		unregisterHide()
		// Sem SWP_SHOWWINDOW: se a janela foi ocultada (tray), não forçamos
		// reexibição — apenas restauramos posição/tamanho/z-order.
		procSetWindowPosO.Call(hwnd, hwndNotTopMost,
			uintptr(int(prev.Left)), uintptr(int(prev.Top)),
			uintptr(int(prev.Right-prev.Left)), uintptr(int(prev.Bottom-prev.Top)), 0)
		win.SetAlwaysOnTop(false)
		win.SetFrameless(a.mainWindowFrameless)
		a.screenshotOverlayActive.Store(false)
		if wasMaximised {
			win.Maximise()
		}
	}
	return restore, nil
}
