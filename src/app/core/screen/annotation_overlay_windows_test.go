//go:build windows

package screen

import (
	"image"
	"testing"
	"time"
	"unsafe"

	"discovery/app/core/winui"
)

func TestAnnotationOverlay_ClickThroughAndVisibilityLifecycle(t *testing.T) {
	ov := NewAnnotationOverlay(image.Rect(0, 0, 320, 240))
	defer ov.Close()

	ov.mu.Lock()
	hwnd := ov.hwnd
	ov.mu.Unlock()
	if hwnd == 0 {
		t.Fatalf("esperava hwnd criado")
	}

	const gwlExStyle = ^uintptr(19) // -20
	getWindowLongPtr := annoUser32.NewProc("GetWindowLongPtrW")
	isWindowVisible := annoUser32.NewProc("IsWindowVisible")

	exStyle, _, _ := getWindowLongPtr.Call(hwnd, gwlExStyle)
	required := uintptr(winui.ExStyleLayered | winui.ExStyleTransparent | winui.ExStyleTopmost | winui.ExStyleNoActivate | winui.ExStyleToolWindow)
	if exStyle&required != required {
		t.Fatalf("exStyle=%#x não contém todas as flags click-through/topmost requeridas (%#x)", exStyle, required)
	}

	vis, _, _ := isWindowVisible.Call(hwnd)
	if vis != 0 {
		t.Fatalf("overlay deveria iniciar oculto")
	}

	if err := ov.HandleCommand([]byte(`{"op":"add","id":"s1","tool":"arrow","color":"#FF3B30","width":5,"points":[{"x":0.1,"y":0.1},{"x":0.8,"y":0.8}]}`)); err != nil {
		t.Fatalf("HandleCommand(add) falhou: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		vis, _, _ = isWindowVisible.Call(hwnd)
		if vis != 0 {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if vis == 0 {
		t.Fatalf("overlay deveria ficar visível após adicionar anotação")
	}

	ov.Clear()
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		vis, _, _ = isWindowVisible.Call(hwnd)
		if vis == 0 {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if vis != 0 {
		t.Fatalf("overlay deveria voltar a ocultar após Clear()")
	}

	ov.SetBounds(image.Rect(100, 100, 500, 400))
	ov.Close()
	ov.Close() // idempotente
}

func TestAnnotationOverlay_PARGBPixelsAndWinding(t *testing.T) {
	const w, h = 320, 240
	buf := make([]byte, w*h*4)

	canvas := winui.NewPARGBCanvas(unsafe.Pointer(&buf[0]), w, h)
	if canvas == nil {
		t.Fatalf("NewPARGBCanvas retornou nil")
	}
	defer canvas.Close()

	shapes := []AnnotationShape{
		{
			Tool:     AnnotationToolArrow,
			ColorRGB: 0xFF3B30,
			Width:    6,
			Alpha:    0.75,
			Points:   []AnnotationPoint{{X: 0.1, Y: 0.2}, {X: 0.4, Y: 0.2}},
		},
		{
			Tool:     AnnotationToolRect,
			ColorRGB: 0x34C759,
			Width:    5,
			Alpha:    1.0,
			Points:   []AnnotationPoint{{X: 0.5, Y: 0.1}, {X: 0.8, Y: 0.4}},
		},
		{
			Tool:     AnnotationToolEllipse,
			ColorRGB: 0x0A84FF,
			Width:    5,
			Alpha:    1.0,
			Points:   []AnnotationPoint{{X: 0.1, Y: 0.5}, {X: 0.4, Y: 0.9}},
		},
		{
			Tool:     AnnotationToolFreehand,
			ColorRGB: 0xFFCC00,
			Width:    10,
			Alpha:    1.0,
			Points: []AnnotationPoint{
				{X: 0.5, Y: 0.7},
				{X: 0.7, Y: 0.7},
				{X: 0.6, Y: 0.6},
				{X: 0.6, Y: 0.8},
			},
		},
		{
			Tool:     AnnotationToolLaser,
			ColorRGB: 0xFF3B30,
			Width:    6,
			Alpha:    0.85,
			Points: []AnnotationPoint{
				{X: 0.2, Y: 0.35},
				{X: 0.45, Y: 0.35},
			},
		},
	}

	for _, sh := range shapes {
		drawAnnotationShapeGDIPlus(canvas, w, h, sh)
	}

	nonZeroAlpha := 0
	for i := 0; i < len(buf); i += 4 {
		b, g, r, a := buf[i], buf[i+1], buf[i+2], buf[i+3]
		if a > 0 {
			nonZeroAlpha++
		}
		if b > a || g > a || r > a {
			t.Fatalf("pixel %d viola premultiplied alpha: BGRA=(%d,%d,%d,%d)", i/4, b, g, r, a)
		}
	}
	if nonZeroAlpha == 0 {
		t.Fatalf("nenhum pixel desenhado no buffer PARGB")
	}

	// Verifica que o vértice de junção do freehand (0.7, 0.7) não ficou com buraco (FillModeWinding).
	jx := int(0.7 * w)
	jy := int(0.7 * h)
	idx := (jy*w + jx) * 4
	if buf[idx+3] < 200 {
		t.Fatalf("junção do freehand com alpha baixo (%d) — possível buraco de FillModeAlternate", buf[idx+3])
	}
}
