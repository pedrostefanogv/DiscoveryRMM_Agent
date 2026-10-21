//go:build windows

package app

import (
	"fmt"
	"syscall"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// ── Overlay de captura: JANELA DEDICADA cobrindo o desktop virtual ─────────
//
// A janela usada aqui é a janela de overlay criada sob demanda
// (screenshot_overlay_window.go) — NÃO a janela principal. Além de isolar a
// captura do estado do tray, a troca elimina a "tela escura": o Wails mantém o
// controller do WebView2 oculto em janelas escondidas, e o SetWindowPos cru
// (SWP_SHOWWINDOW) mostrava a moldura sem nada pintado. Aqui a janela é sempre
// exibida pelo caminho normal do Wails (Show), que reexibe o controller.

const (
	hwndTopMost    = ^uintptr(0) // HWND_TOPMOST (-1)
	hwndNotTopMost = ^uintptr(1) // HWND_NOTOPMOST (-2)
	swpShowWindow  = 0x0040
	swpNoActivate  = 0x0010
	swpNoSize      = 0x0001
	swpNoMove      = 0x0002
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

// enterOverlayBounds posiciona a janela do overlay sobre o desktop virtual,
// garante o WebView visível e devolve a função de restauração.
func (a *App) enterOverlayBounds(win application.Window, x, y, w, h int) (func(), error) {
	if win == nil {
		return nil, fmt.Errorf("janela do overlay indisponivel")
	}
	hwnd := uintptr(win.NativeWindow())
	if hwnd == 0 {
		return nil, fmt.Errorf("handle nativo da janela indisponivel")
	}
	var prev overlayRect
	procGetWindowRectO.Call(hwnd, uintptr(unsafe.Pointer(&prev)))

	// Durante o overlay a janela cobre o desktop virtual inteiro (inclusive a
	// taskbar): sem moldura para o webview coincidir com a tela e sem o clamp de
	// WorkArea (ver FitWindowToWorkArea).
	a.screenshotOverlayActive.Store(true)
	win.SetFrameless(true)
	win.SetAlwaysOnTop(true)
	ret, _, callErr := procSetWindowPosO.Call(hwnd, hwndTopMost,
		uintptr(int(x)), uintptr(int(y)), uintptr(w), uintptr(h), swpShowWindow|swpNoActivate)
	if ret == 0 {
		win.SetFrameless(false)
		win.SetAlwaysOnTop(false)
		a.screenshotOverlayActive.Store(false)
		return nil, fmt.Errorf("SetWindowPos falhou ao preparar o overlay: %v", callErr)
	}

	// O SetWindowPos cru mostra a moldura, mas NÃO reexibe o controller do
	// WebView2 (uma janela criada/escondida tem chromium.Hide()). Sem este Show
	// a janela apareceria preta — causa raiz do relato da captura pelo chat.
	win.Show()

	// O Show do Wails pode reposicionar a janela na ordem Z (e o foco que o
	// usuário deu a outro app enquanto a captura era preparada empurra o overlay
	// para trás). Reafirma o TOPMOST DEPOIS do Show: sem isso, ao capturar em
	// atraso (3/5/10 s) com outro app em foco, a barra de seleção ficava por baixo.
	// Mantemos SWP_NOACTIVATE: o overlay não deve roubar o foco, só ficar acima.
	if ret2, _, _ := procSetWindowPosO.Call(hwnd, hwndTopMost, 0, 0, 0, 0,
		swpShowWindow|swpNoActivate|swpNoSize|swpNoMove); ret2 == 0 {
		// Falha aqui é apenas log: a janela já está visível dentro dos limites.
		a.Logs.Append("[screenshot] aviso: nao foi possivel reafirmar o topmost do overlay")
	}
	// Fechar/ocultar a janela do overlay durante a sessão encerra a captura e
	// devolve as janelas. O abort roda em goroutine para não chamar a API do
	// Wails dentro do callback do evento.
	unregisterClose := win.RegisterHook(events.Common.WindowClosing, func(*application.WindowEvent) {
		go a.abortScreenshotOverlay("janela de captura fechada")
	})
	unregisterHide := win.RegisterHook(events.Common.WindowHide, func(*application.WindowEvent) {
		go a.abortScreenshotOverlay("janela de captura ocultada")
	})

	restore := func() {
		// Desregistra ANTES de esconder: o hook de WindowHide não pode reabrir a
		// sessão que está sendo encerrada.
		unregisterClose()
		unregisterHide()
		win.Hide()
		procSetWindowPosO.Call(hwnd, hwndNotTopMost,
			uintptr(int(prev.Left)), uintptr(int(prev.Top)),
			uintptr(int(prev.Right-prev.Left)), uintptr(int(prev.Bottom-prev.Top)), 0)
		win.SetAlwaysOnTop(false)
		win.SetFrameless(false)
		a.screenshotOverlayActive.Store(false)
	}
	return restore, nil
}
