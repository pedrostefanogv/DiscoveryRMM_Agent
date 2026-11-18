//go:build !windows

package app

import (
	"fmt"
)

// enterOverlayBounds não tem equivalente fora do Windows.
func (a *App) enterOverlayBounds(win uiWindow, x, y, w, h int) (func(), error) {
	return nil, fmt.Errorf("captura de tela suportada apenas no Windows")
}
