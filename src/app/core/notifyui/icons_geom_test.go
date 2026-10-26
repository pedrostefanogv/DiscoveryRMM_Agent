package notifyui

import "testing"

// bbox devolve (minX, minY, maxX, maxY) do subpath.
func bbox(sp iconSubpath) (minX, minY, maxX, maxY float64) {
	minX, minY, maxX, maxY = sp[0].X, sp[0].Y, sp[0].X, sp[0].Y
	for _, p := range sp[1:] {
		if p.X < minX {
			minX = p.X
		}
		if p.Y < minY {
			minY = p.Y
		}
		if p.X > maxX {
			maxX = p.X
		}
		if p.Y > maxY {
			maxY = p.Y
		}
	}
	return
}

// Regressão de geometria: em todos os glifos com furos, os subpaths internos
// (bolinhas, exclamações) precisam caber no bounding box do contorno externo —
// senão o fill alternate desenharia peças sólidas fora do corpo do glifo.
func TestGlyphHolesInsideOutline(t *testing.T) {
	for _, name := range []string{IconInfo, IconWarning, IconError, IconQuestion, GlyphChat} {
		sps := parseGlyph(name)
		if len(sps) < 2 {
			t.Fatalf("glifo %q: esperava >=2 subpaths, got %d", name, len(sps))
		}
		// contorno = subpath com maior bbox.
		outerIdx, outerArea := 0, 0.0
		for i, sp := range sps {
			x0, y0, x1, y1 := bbox(sp)
			area := (x1 - x0) * (y1 - y0)
			if area > outerArea {
				outerArea, outerIdx = area, i
			}
		}
		ox0, oy0, ox1, oy1 := bbox(sps[outerIdx])
		for i, sp := range sps {
			if i == outerIdx {
				continue
			}
			x0, y0, x1, y1 := bbox(sp)
			if x0 < ox0-1 || y0 < oy0-1 || x1 > ox1+1 || y1 > oy1+1 {
				t.Fatalf("glifo %q: subpath %d bbox=(%v,%v,%v,%v) fora do contorno (%v,%v,%v,%v)",
					name, i, x0, y0, x1, y1, ox0, oy0, ox1, oy1)
			}
		}
	}
}
