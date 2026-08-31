package app

import (
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// fakeWindow registra as chamadas feitas pelo clamp para verificação.
type fakeWindow struct {
	w, h           int
	x, y           int
	minW, minH     int
	setSizeCalls   int
	setPosCalls    int
	setMinCalls    int
	finalW, finalH int
	finalX, finalY int
}

func (f *fakeWindow) Size() (int, int)     { return f.w, f.h }
func (f *fakeWindow) Position() (int, int) { return f.x, f.y }
func (f *fakeWindow) SetSize(width, height int) application.Window {
	f.setSizeCalls++
	f.w, f.h = width, height
	f.finalW, f.finalH = width, height
	return nil
}
func (f *fakeWindow) SetPosition(x, y int) {
	f.setPosCalls++
	f.x, f.y = x, y
	f.finalX, f.finalY = x, y
}
func (f *fakeWindow) SetMinSize(minWidth, minHeight int) {
	f.setMinCalls++
	f.minW, f.minH = minWidth, minHeight
}

func TestFitWindowToScreen_NoChangeWhenFits(t *testing.T) {
	fw := &fakeWindow{w: 1280, h: 860, x: 100, y: 100}
	screen := &application.Screen{
		WorkArea: application.Rect{X: 0, Y: 0, Width: 1920, Height: 1040},
	}
	fitWindowToScreen(fw, screen)
	if fw.setSizeCalls != 0 || fw.setPosCalls != 0 {
		t.Fatalf("esperado nenhuma alteracao, got setSize=%d setPos=%d", fw.setSizeCalls, fw.setPosCalls)
	}
}

func TestFitWindowToScreen_ClampsSizeOnSmallScreen(t *testing.T) {
	// Cenário 1080p @150%: WorkArea lógica ~1280x688 (taskbar 32 DIP).
	fw := &fakeWindow{w: 1280, h: 860, x: 0, y: 0}
	screen := &application.Screen{
		WorkArea:    application.Rect{X: 0, Y: 0, Width: 1280, Height: 688},
		ScaleFactor: 1.5,
	}
	fitWindowToScreen(fw, screen)
	wantW := 1280 - 2*windowFitMargin // 1248
	wantH := 688 - 2*windowFitMargin  // 656
	if fw.finalW != wantW || fw.finalH != wantH {
		t.Fatalf("esperado %dx%d, got %dx%d", wantW, wantH, fw.finalW, fw.finalH)
	}
	// MinSize NÃO deve ser restaurado (novo tamanho < mínimo original 980x700
	// em altura), pois SetMinSize redimensionaria de volta e desfaria o clamp.
	if fw.setMinCalls != 0 {
		t.Fatalf("SetMinSize nao deveria ser chamado quando clamp < MinHeight; got %d chamadas", fw.setMinCalls)
	}
}

func TestFitWindowToScreen_RestoresMinSizeWhenPossible(t *testing.T) {
	// Tela grande o suficiente para o clamp respeitar o MinSize original:
	// só a largura é reduzida (ex.: janela mais larga que a tela).
	fw := &fakeWindow{w: 2000, h: 860, x: 0, y: 0}
	screen := &application.Screen{
		WorkArea: application.Rect{X: 0, Y: 0, Width: 1600, Height: 1040},
	}
	fitWindowToScreen(fw, screen)
	wantW := 1600 - 2*windowFitMargin // 1568
	if fw.finalW != wantW || fw.finalH != 860 {
		t.Fatalf("esperado %dx860, got %dx%d", wantW, fw.finalW, fw.finalH)
	}
	// 1568x860 >= 980x700 → MinSize original deve ser restaurado.
	if fw.setMinCalls != 1 || fw.minW != WindowMinWidth || fw.minH != WindowMinHeight {
		t.Fatalf("esperado SetMinSize(%d,%d) 1x, got %dx (%d,%d)",
			WindowMinWidth, WindowMinHeight, fw.setMinCalls, fw.minW, fw.minH)
	}
}

func TestFitWindowToScreen_RepositionsOffscreenWindow(t *testing.T) {
	// Janela posicionada acima do topo do monitor (y negativo).
	fw := &fakeWindow{w: 1280, h: 860, x: 0, y: -400}
	screen := &application.Screen{
		WorkArea: application.Rect{X: 0, Y: 0, Width: 1920, Height: 1040},
	}
	fitWindowToScreen(fw, screen)
	if fw.setSizeCalls != 0 {
		t.Fatalf("tamanho nao deveria mudar; got %d chamadas", fw.setSizeCalls)
	}
	if fw.finalY != 0 {
		t.Fatalf("esperado y=0, got %d", fw.finalY)
	}
}

func TestFitWindowToScreen_RepositionsRightOverflow(t *testing.T) {
	// Janela com canto direito fora da WorkArea.
	fw := &fakeWindow{w: 1280, h: 860, x: 1800, y: 100}
	screen := &application.Screen{
		WorkArea: application.Rect{X: 0, Y: 0, Width: 1920, Height: 1040},
	}
	fitWindowToScreen(fw, screen)
	wantX := 1920 - 1280 // 640
	if fw.finalX != wantX {
		t.Fatalf("esperado x=%d, got %d", wantX, fw.finalX)
	}
}

func TestFitWindowToScreen_InvalidWorkArea(t *testing.T) {
	fw := &fakeWindow{w: 1280, h: 860, x: 0, y: 0}
	screen := &application.Screen{
		WorkArea: application.Rect{X: 0, Y: 0, Width: 0, Height: 0},
	}
	fitWindowToScreen(fw, screen)
	if fw.setSizeCalls != 0 || fw.setPosCalls != 0 {
		t.Fatalf("work area invalida nao deveria alterar a janela")
	}
}

func TestFitWindowToScreen_NilArgs(t *testing.T) {
	// Não deve panicking com argumentos nulos.
	fitWindowToScreen(nil, nil)
	fitWindowToScreen(&fakeWindow{}, nil)
	fitWindowToScreen(nil, &application.Screen{})
}
