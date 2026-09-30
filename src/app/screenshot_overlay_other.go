//go:build !windows

package app

import "fmt"

// enterScreenshotOverlayBounds não tem equivalente fora do Windows.
func (a *App) enterScreenshotOverlayBounds(x, y, w, h int) (func(), error) {
	return nil, fmt.Errorf("captura de tela suportada apenas no Windows")
}
