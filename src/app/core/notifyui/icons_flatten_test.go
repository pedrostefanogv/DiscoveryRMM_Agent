package notifyui

import "testing"

// Regressão: os comandos S/T (curva suave) precisam produzir pontos
// interpolados — antes eles eram descartados por append sem reatribuição.
func TestFlattenSVGPathSmoothCurves(t *testing.T) {
	// "M0 0 S10 0 20 0": a cúbica refletida gera pontos intermediários.
	sps := flattenSVGPath("M0 0S10 0 20 0z")
	if len(sps) != 1 {
		t.Fatalf("subpaths = %d, want 1", len(sps))
	}
	// Penas da curva: 16 segmentos geram 15 pontos + endpoints.
	if len(sps[0]) < 17 {
		t.Fatalf("pontos = %d, want >= 17 (curva não interpolada?)", len(sps[0]))
	}
	// O glifo "question" usa s/l/s/C: garante muitos pontos interpolados.
	total := 0
	for _, sp := range parseIconSubpaths(IconQuestion) {
		total += len(sp)
	}
	if total < 100 {
		t.Fatalf("glifo question com apenas %d pontos — curvas foram perdidas", total)
	}
}
