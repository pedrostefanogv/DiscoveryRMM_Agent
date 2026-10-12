//go:build !windows

package app

import (
	"fmt"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// enterOverlayBounds não tem equivalente fora do Windows.
func (a *App) enterOverlayBounds(win application.Window, x, y, w, h int) (func(), error) {
	return nil, fmt.Errorf("captura de tela suportada apenas no Windows")
}
