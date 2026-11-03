//go:build windows

package winui

import (
	"sync"
	"testing"

	"golang.org/x/sys/windows"
)

// ── Regressão do layout de RECT (2026-11-03) ──
//
// Rect tem quatro int de 64 bits (32 B em amd64) e RECT do Win32 tem quatro LONG
// (16 B). Passar &Rect direto para GetWindowRect fazia a API escrever os LONGs
// por cima de X/Y e deixar W/H zerados: WindowRect/VisibleRect devolviam
// ok=false SEMPRE. Consequências em produção: a máscara do banner nunca era
// capturada e a emenda por região da captura nunca ligava.
//
// Este teste cria uma janela real e exige um retângulo coerente — se o layout
// regredir, ele falha (antes ele ficaria "invisível" porque o chamador só via
// ok=false).

var rectTestClassOnce sync.Once

func rectTestWindow(t *testing.T, x, y, w, h int) uintptr {
	t.Helper()
	var err error
	rectTestClassOnce.Do(func() {
		err = RegisterWindowClass(
			"DiscoveryWinuiRectTest",
			windows.NewCallback(func(hwnd, msg, wparam, lparam uintptr) uintptr {
				return DefWindowProc(hwnd, msg, wparam, lparam)
			}),
			0,
		)
	})
	if err != nil {
		t.Skipf("registro da classe falhou: %v", err)
	}
	hwnd, err := CreateWindow(
		"DiscoveryWinuiRectTest", "rect test",
		ExStyleToolWindow|ExStyleNoActivate,
		WinStylePopup,
		x, y, w, h,
	)
	if err != nil {
		t.Skipf("criação da janela falhou: %v", err)
	}
	t.Cleanup(func() { DestroyWindow(hwnd) })
	return hwnd
}

func TestWindowRectLayout(t *testing.T) {
	const x, y, w, h = 120, 90, 320, 120
	hwnd := rectTestWindow(t, x, y, w, h)

	got, ok := WindowRect(hwnd)
	if !ok {
		t.Fatal("WindowRect devolveu ok=false para uma janela válida (layout de RECT quebrado?)")
	}
	if got.W != w || got.H != h {
		t.Fatalf("WindowRect = %dx%d, esperado %dx%d (RECT interpretado errado)", got.W, got.H, w, h)
	}
	if got.X != x || got.Y != y {
		t.Fatalf("WindowRect origem = (%d,%d), esperado (%d,%d)", got.X, got.Y, x, y)
	}
}

func TestVisibleRectLayout(t *testing.T) {
	const x, y, w, h = 140, 110, 260, 100
	hwnd := rectTestWindow(t, x, y, w, h)

	ShowWindow(hwnd, SwShowNormal)
	UpdateWindow(hwnd)

	got, ok := VisibleRect(hwnd)
	if !ok {
		t.Fatal("VisibleRect devolveu ok=false para uma janela visível e válida")
	}
	if got.W != w || got.H != h {
		t.Fatalf("VisibleRect = %dx%d, esperado %dx%d", got.W, got.H, w, h)
	}
	if got.X != x || got.Y != y {
		t.Fatalf("VisibleRect origem = (%d,%d), esperado (%d,%d)", got.X, got.Y, x, y)
	}
}
