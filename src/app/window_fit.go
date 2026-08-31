package app

import (
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// windowFitMargin é a margem lógica (DIP) deixada ao redor da janela quando
// ela precisa ser reduzida para caber na área de trabalho visível.
const windowFitMargin = 16

// FitWindowToWorkArea garante que a janela principal fique totalmente visível
// na área de trabalho (excluindo taskbar), independentemente da escala de DPI
// do Windows (125%, 150%, etc.).
//
// Problema resolvido: em telas 1080p com escala 125%/150%, a janela padrão
// (1280x860) pode ultrapassar a área visível, escondendo o chrome da janela
// (botões minimizar/maximizar/fechar) que fica no topo.
//
// Estratégia:
//  1. Obtém a screen onde a janela está (via GetScreen, que retorna WorkArea
//     em unidades lógicas/DIP já convertidas por applyDPIScaling).
//  2. Se a janela excede a WorkArea, reduz o tamanho para caber (com margem).
//  3. Reposiciona a janela para que a origem fique dentro da WorkArea.
//
//wails:ignore
func (a *App) FitWindowToWorkArea() {
	if a.mainWindow == nil {
		return
	}
	screen, err := a.mainWindow.GetScreen()
	if err != nil || screen == nil {
		log.Printf("[window-fit] screen indisponivel: %v", err)
		return
	}
	fitWindowToScreen(a.mainWindow, screen)
}

// fitWindowToScreen aplica o clamp de tamanho/posição usando a screen dada.
// Ambos WorkArea e SetSize/SetPosition operam em unidades lógicas (DIP),
// então nenhuma conversão manual de DPI é necessária.
func fitWindowToScreen(window application.Window, screen *application.Screen) {
	if window == nil || screen == nil {
		return
	}

	work := screen.WorkArea
	if work.Width <= 0 || work.Height <= 0 {
		log.Printf("[window-fit] work area invalida: %+v", work)
		return
	}

	width, height := window.Size()
	newW, newH := width, height

	// Reduz o tamanho se a janela não couber na área de trabalho.
	maxW := work.Width - 2*windowFitMargin
	maxH := work.Height - 2*windowFitMargin
	if newW > maxW {
		newW = maxW
	}
	if newH > maxH {
		newH = maxH
	}
	if newW < 1 {
		newW = 1
	}
	if newH < 1 {
		newH = 1
	}

	if newW != width || newH != height {
		log.Printf("[window-fit] redimensionando janela %dx%d -> %dx%d (work area %dx%d @ scale %.2f)",
			width, height, newW, newH, work.Width, work.Height, screen.ScaleFactor)
		window.SetSize(newW, newH)
	}

	// Reposiciona se a origem estiver fora da área visível (janela "cortada").
	x, y := window.Position()
	newX, newY := x, y
	if newX < work.X {
		newX = work.X
	}
	if newY < work.Y {
		newY = work.Y
	}
	// Garante que o canto direito/inferior também fique visível.
	if newX+newW > work.X+work.Width {
		newX = work.X + work.Width - newW
	}
	if newY+newH > work.Y+work.Height {
		newY = work.Y + work.Height - newH
	}
	if newX != x || newY != y {
		log.Printf("[window-fit] reposicionando janela (%d,%d) -> (%d,%d)", x, y, newX, newY)
		window.SetPosition(newX, newY)
	}
}
