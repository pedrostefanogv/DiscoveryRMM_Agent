//go:build windows

package winui

import (
	"math"
	"testing"
)

// TestRectFRoundAndBounds cobre a aritmética fracionária usada pela animação.
func TestRectFRoundAndBounds(t *testing.T) {
	r := RectF{X: 10.4, Y: 20.6, W: 30.2, H: 8.5}
	if got := r.Right(); math.Abs(got-40.6) > 1e-9 {
		t.Fatalf("Right = %v, esperado 40.6", got)
	}
	if got := r.Bottom(); math.Abs(got-29.1) > 1e-9 {
		t.Fatalf("Bottom = %v, esperado 29.1", got)
	}
	round := r.Round()
	if round.X != 10 || round.Y != 21 || round.W != 30 || round.H != 9 {
		t.Fatalf("Round = %+v, esperado {10 21 30 9}", round)
	}
	if !(RectF{W: 0, H: 10}).Empty() || !(RectF{W: 10, H: -1}).Empty() {
		t.Fatal("retângulo sem área deveria ser Empty")
	}
}

// TestRoundRectPolygonFPreservesFractions garante que os pontos do contorno
// mantêm as coordenadas fracionárias (o desenho inteiro só arredondava) e que
// ficam dentro do retângulo com o raio limitado.
func TestRoundRectPolygonFPreservesFractions(t *testing.T) {
	r := RectF{X: 10.5, Y: 20.25, W: 100.5, H: 40.5}
	points := roundRectPolygonF(r, 8.5)
	if want := 4 * (roundRectArcSegments + 1); len(points) != want {
		t.Fatalf("pontos = %d, esperado %d", len(points), want)
	}
	fractional := 0
	for _, p := range points {
		x, y := float64(p.X), float64(p.Y)
		if x < r.X-0.01 || x > r.Right()+0.01 || y < r.Y-0.01 || y > r.Bottom()+0.01 {
			t.Fatalf("ponto (%v,%v) fora do retângulo %+v", x, y, r)
		}
		if x != math.Trunc(x) || y != math.Trunc(y) {
			fractional++
		}
	}
	if fractional == 0 {
		t.Fatal("nenhum ponto fracionário: o contorno foi arredondado para inteiros")
	}

	// Raio maior que a metade da altura é limitado (senão o contorno degenera).
	narrow := RectF{X: 0, Y: 0, W: 100, H: 10}
	for _, p := range roundRectPolygonF(narrow, 40) {
		if float64(p.Y) < -0.01 || float64(p.Y) > 10.01 {
			t.Fatalf("ponto Y=%v fora do retângulo estreito", p.Y)
		}
	}
}

// testFloatDC cria um DC de memória compatível com a tela (mesmo padrão do
// gdiplus_windows_test.go) e devolve também os finalizadores.
func testFloatDC(t *testing.T, width, height int) (dc uintptr) {
	t.Helper()
	screenDC, _, _ := testGpGetDC.Call(0)
	if screenDC == 0 {
		t.Skip("GetDC falhou neste ambiente")
	}
	memDC, _, _ := testGpCreateCompatDC.Call(screenDC)
	if memDC == 0 {
		testGpReleaseDC.Call(0, screenDC)
		t.Skip("CreateCompatibleDC falhou neste ambiente")
	}
	bitmap, _, _ := testGpCreateCompatBmp.Call(screenDC, uintptr(width), uintptr(height))
	if bitmap == 0 {
		testGpDeleteDC.Call(memDC)
		testGpReleaseDC.Call(0, screenDC)
		t.Skip("CreateCompatibleBitmap falhou neste ambiente")
	}
	old, _, _ := testGpSelectObject.Call(memDC, bitmap)
	t.Cleanup(func() {
		testGpSelectObject.Call(memDC, old)
		testGpDeleteObject.Call(bitmap)
		testGpDeleteDC.Call(memDC)
		testGpReleaseDC.Call(0, screenDC)
	})
	return memDC
}

func testPixelColor(t *testing.T, dc uintptr, x, y int) Color {
	t.Helper()
	pixel, _, _ := testGpGetPixel.Call(dc, uintptr(x), uintptr(y))
	if pixel == 0xFFFFFFFF {
		t.Fatalf("GetPixel(%d,%d) falhou", x, y)
	}
	return Color(uint32(pixel) & 0x00FFFFFF)
}

// TestFillRoundRectAAfFractionalEdge confirma o ganho principal: a borda
// esquerda em x=5.5 deixa o pixel 5 parcialmente coberto (blend) e o 6 cheio —
// é esse meio-pixel que faz o movimento da barra ser contínuo.
func TestFillRoundRectAAfFractionalEdge(t *testing.T) {
	dc := testFloatDC(t, 60, 40)
	fill := RGB(20, 140, 240)
	FillRoundRectAAf(dc, RectF{X: 5.5, Y: 5.5, W: 40.5, H: 20.5}, 8, fill)

	if got := testPixelColor(t, dc, 25, 15); got != fill {
		t.Fatalf("pixel central = %#x, esperado %#x", got, fill)
	}
	edge := testPixelColor(t, dc, 5, 15)
	inner := testPixelColor(t, dc, 6, 15)
	if edge == fill {
		t.Fatalf("borda fracionária ficou sólida em x=5 (%#x): sem antialiasing sub-pixel", edge)
	}
	if inner != fill {
		t.Fatalf("pixel interno x=6 = %#x, esperado %#x", inner, fill)
	}
}

// TestFillRoundRectGradientAAf segue o sentido do gradiente (esquerda→direita)
// e cobre os limites explícitos + fallback quando o gradiente é inválido.
func TestFillRoundRectGradientAAf(t *testing.T) {
	dc := testFloatDC(t, 80, 30)
	FillRoundRectGradientAAf(dc, RectF{X: 2, Y: 2, W: 76, H: 26}, 6, RGB(0, 0, 255), RGB(255, 0, 0), 2, 78)

	left := testPixelColor(t, dc, 6, 15)
	right := testPixelColor(t, dc, 73, 15)
	if left.B() <= left.R() {
		t.Fatalf("lado esquerdo deveria puxar para o azul: %#x", left)
	}
	if right.R() <= right.B() {
		t.Fatalf("lado direito deveria puxar para o vermelho: %#x", right)
	}

	// Limites invertidos (gradTo <= gradFrom): cai no sólido AA.
	dc2 := testFloatDC(t, 80, 30)
	FillRoundRectGradientAAf(dc2, RectF{X: 2, Y: 2, W: 76, H: 26}, 6, RGB(0, 0, 255), RGB(255, 0, 0), 50, 10)
	center := testPixelColor(t, dc2, 40, 15)
	if center != RGB(0, 0, 255) {
		t.Fatalf("gradiente invertido deveria usar a cor inicial: %#x", center)
	}
}

// TestBackBufferReuseAndBlit valida o reaproveitamento do buffer e a cópia de
// apenas uma região (base do repinte incremental da animação).
func TestBackBufferReuseAndBlit(t *testing.T) {
	src := testFloatDC(t, 90, 50)
	dst := testFloatDC(t, 90, 50)

	var buf BackBuffer
	if !buf.Ensure(src, 90, 50) {
		t.Skip("BackBuffer.Ensure falhou neste ambiente")
	}
	defer buf.Release()

	firstDC := buf.DC()
	if firstDC == 0 || !buf.Ready() {
		t.Fatal("buffer deveria estar pronto")
	}
	// Mesmo tamanho: não recria os recursos.
	if !buf.Ensure(src, 90, 50) || buf.DC() != firstDC {
		t.Fatal("Ensure recriou o buffer sem mudança de tamanho")
	}
	// Tamanho diferente: recria.
	if !buf.Ensure(src, 40, 20) {
		t.Fatal("Ensure com novo tamanho falhou")
	}
	if w, h := buf.Size(); w != 40 || h != 20 {
		t.Fatalf("tamanho = %dx%d, esperado 40x20", w, h)
	}

	marker := RGB(0, 200, 0)
	FillRect(buf.DC(), Rect{X: 10, Y: 10, W: 8, H: 8}, marker)
	buf.BlitTo(dst, Rect{X: 10, Y: 10, W: 8, H: 8})
	if got := testPixelColor(t, dst, 12, 12); got != marker {
		t.Fatalf("pixel copiado = %#x, esperado %#x", got, marker)
	}
	// Fora da região copiada o destino continua limpo (diferente do marcador).
	if got := testPixelColor(t, dst, 30, 30); got == marker {
		t.Fatal("BlitTo copiou fora da região pedida")
	}

	buf.Release()
	if buf.Ready() {
		t.Fatal("Release deveria limpar o buffer")
	}
}
