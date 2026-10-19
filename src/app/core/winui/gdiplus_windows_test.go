//go:build windows

package winui

import (
	"syscall"
	"testing"
)

var (
	testGdiplusUser32 = syscall.NewLazyDLL("user32.dll")
	testGdiplusGdi32  = syscall.NewLazyDLL("gdi32.dll")

	testGpGetDC           = testGdiplusUser32.NewProc("GetDC")
	testGpReleaseDC       = testGdiplusUser32.NewProc("ReleaseDC")
	testGpCreateCompatDC  = testGdiplusGdi32.NewProc("CreateCompatibleDC")
	testGpCreateCompatBmp = testGdiplusGdi32.NewProc("CreateCompatibleBitmap")
	testGpSelectObject    = testGdiplusGdi32.NewProc("SelectObject")
	testGpDeleteObject    = testGdiplusGdi32.NewProc("DeleteObject")
	testGpDeleteDC        = testGdiplusGdi32.NewProc("DeleteDC")
	testGpGetPixel        = testGdiplusGdi32.NewProc("GetPixel")
)

// TestRoundRectPolygonShape valida o contorno usado no desenho antialiased:
// quantidade de pontos, limites do retângulo e raio efetivo.
func TestRoundRectPolygonShape(t *testing.T) {
	r := Rect{X: 10, Y: 20, W: 100, H: 40}
	radius := 8

	pts := roundRectPolygon(r, radius)
	if want := 4 * (roundRectArcSegments + 1); len(pts) != want {
		t.Fatalf("pontos = %d, esperado %d", len(pts), want)
	}
	for _, p := range pts {
		x, y := int(p.X), int(p.Y)
		if x < r.X || x > r.Right() || y < r.Y || y > r.Bottom() {
			t.Fatalf("ponto (%d,%d) fora do retângulo %+v", x, y, r)
		}
	}
	// Canto superior esquerdo cortado: nenhum ponto pode cair no quadrado do
	// canto (fora do arco de raio 8 centrado em (18,28)).
	for _, p := range pts {
		dx := float64(radius) - (float64(p.X) - float64(r.X))
		dy := float64(radius) - (float64(p.Y) - float64(r.Y))
		if dx > 0.5 && dy > 0.5 {
			// O polígono aproxima o arco por cordas e os pontos são arredondados
			// para inteiros: tolerância de ~2px cobre esse desvio.
			limit := float64((radius + 2) * (radius + 2))
			if dx*dx+dy*dy > limit {
				t.Fatalf("ponto (%d,%d) fora do arco do canto", p.X, p.Y)
			}
		}
	}

	// Raio zero: quatro cantos exatos.
	rectPts := roundRectPolygon(r, 0)
	if len(rectPts) != 4 {
		t.Fatalf("raio 0 deveria gerar 4 pontos, gerou %d", len(rectPts))
	}
	if rectPts[0].X != int32(r.X) || rectPts[0].Y != int32(r.Y) {
		t.Fatalf("primeiro ponto = %+v, esperado canto superior esquerdo", rectPts[0])
	}
}

// TestFillRoundRectAASmoke garante que o caminho GDI+ (ou o fallback GDI)
// realmente preenche o interior do retângulo arredondado.
func TestFillRoundRectAASmoke(t *testing.T) {
	screenDC, _, _ := testGpGetDC.Call(0)
	if screenDC == 0 {
		t.Skip("GetDC falhou neste ambiente")
	}
	defer testGpReleaseDC.Call(0, screenDC)

	memDC, _, _ := testGpCreateCompatDC.Call(screenDC)
	if memDC == 0 {
		t.Skip("CreateCompatibleDC falhou neste ambiente")
	}
	defer testGpDeleteDC.Call(memDC)

	bitmap, _, _ := testGpCreateCompatBmp.Call(screenDC, 60, 40)
	if bitmap == 0 {
		t.Skip("CreateCompatibleBitmap falhou neste ambiente")
	}
	defer testGpDeleteObject.Call(bitmap)
	old, _, _ := testGpSelectObject.Call(memDC, bitmap)
	defer testGpSelectObject.Call(memDC, old)

	fill := RGB(10, 200, 30)
	FillRoundRectAA(memDC, Rect{X: 5, Y: 5, W: 50, H: 30}, 10, fill)

	pixel, _, _ := testGpGetPixel.Call(memDC, 30, 20)
	if pixel == 0xFFFFFFFF {
		t.Fatal("GetPixel falhou")
	}
	got := Color(uint32(pixel) & 0x00FFFFFF)
	if got != fill {
		t.Fatalf("pixel central = %#x, esperado %#x (fill)", got, fill)
	}

	// Canto do bounding box deve continuar sem pintura (fora do arco).
	corner, _, _ := testGpGetPixel.Call(memDC, 5, 5)
	if Color(uint32(corner)&0x00FFFFFF) == fill {
		t.Fatalf("canto (5,5) foi pintado — contorno arredondado não está sendo respeitado")
	}
}
