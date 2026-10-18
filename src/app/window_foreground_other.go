//go:build !windows

package app

import "github.com/wailsapp/wails/v3/pkg/application"

// ── Sem forçar primeiro plano fora do Windows ──────────────────────────────
//
// O problema do "foreground lock" é específico do Windows (ver
// window_foreground_windows.go). Nas demais plataformas o Show()+Focus() do
// Wails é o que existe de suporte, então as funções viram no-op/falso e o
// caminho comum de raiseWindowToFront continua funcionando.

func forceWindowToForeground(win application.Window, _ bool) bool {
	if win == nil {
		return false
	}
	return win.IsFocused()
}

func flashWindowUntilFocused(_ application.Window) {}

func stopWindowFlash(_ application.Window) {}
