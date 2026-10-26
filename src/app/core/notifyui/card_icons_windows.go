//go:build windows

package notifyui

import (
	_ "embed"
	"math"

	"discovery/app/core/winui"
)

// agentLogoPNG é o logo do agente (o "D", mesmo PNG global do toast), embedado
// para o selo do cabeçalho do cartão.
//
//go:embed assets/agent_logo.png
var agentLogoPNG []byte

// ── Desenho dos ícones do cartão (Windows/GDI+) ──

// DrawKindIcon desenha o glifo do motivo da notificação (Material Symbols,
// viewBox 24x24) escalado para o retângulo, com antialiasing. Kind vazio ou
// desconhecido desenha o de informação (default do cartão).
func DrawKindIcon(hdc uintptr, rect winui.Rect, kind string, color winui.Color) {
	subpaths := parseIconSubpaths(normalizeIconKind(kind))
	if len(subpaths) == 0 || rect.Empty() {
		return
	}
	scale := float64(rect.W) / 24.0
	round := func(v float64) int { return int(math.Round(v)) }
	polys := make([][]winui.Point, 0, len(subpaths))
	for _, sp := range subpaths {
		if len(sp) < 3 {
			continue
		}
		pts := make([]winui.Point, 0, len(sp))
		for _, pt := range sp {
			pts = append(pts, winui.Point{
				X: rect.X + round(pt.X*scale),
				Y: rect.Y + round(pt.Y*scale),
			})
		}
		polys = append(polys, pts)
	}
	if len(polys) == 0 {
		return
	}
	winui.FillPolygonsAA(hdc, polys, color)
}

// DrawAgentLogo desenha o logo do agente ("D", PNG embedado) preenchendo o
// retângulo, preservando a transparência via GDI+.
func DrawAgentLogo(hdc uintptr, rect winui.Rect) bool {
	if len(agentLogoPNG) == 0 || rect.Empty() {
		return false
	}
	return winui.DrawPNGMemory(hdc, agentLogoPNG, rect)
}
