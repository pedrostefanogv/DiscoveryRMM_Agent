package screen

import "testing"

// ── Regressão: invalidação do cache por STRIDE (revisão de bugs 2026-11-19) ──
//
// O cache do detector guardava width/height mas NÃO o stride. Com o mesmo W/H e
// stride diferente (troca de capturer GDI↔DXGI com padding distinto), a
// comparação linha-a-linha passa a confrontar posições de tela distintas.
//
// Severidade REAL medida (não presumida): o defeito era LATENTE, não ativo. Com
// o stride maior, len(cur) > len(prev), e a leitura desalinhada encontra bytes
// diferentes em praticamente todos os tiles — o detector marcava a tela inteira
// como suja e o viewer recebia um key frame válido por acidente. Medido em
// 1920x1080 com fundo uniforme E com gradiente: 100% de cobertura nos dois casos.
//
// O valor da correção é tornar a invalidação EXPLÍCITA e determinística, em vez
// de depender de "o desalinhamento suja tudo por sorte". Um detector que acerta
// por acidente é um detector que pode passar a errar em silêncio.
func TestDetectInvalidaCachePorStride(t *testing.T) {
	const w, h = 1920, 1080
	mkGrad := func(stride int) *Frame {
		d := make([]byte, stride*h)
		for y := 0; y < h; y++ {
			v := byte(y * 7)
			for x := 0; x < w*4; x++ {
				d[y*stride+x] = v
			}
		}
		return &Frame{Data: d, Width: w, Height: h, Stride: stride}
	}

	d := NewDirtyDetector(32)
	d.Detect(mkGrad(w * 4))

	// Mesmo W/H, stride com padding: o cache PRECISA ser invalidado e a
	// resposta tem de ser a tela inteira (contrato de key frame).
	rects := d.Detect(mkGrad(w*4 + 256))
	if len(rects) != 1 {
		t.Fatalf("mudanca de stride deve devolver 1 rect, veio %d: %+v", len(rects), rects)
	}
	if rects[0].X != 0 || rects[0].Y != 0 || rects[0].Width != w || rects[0].Height != h {
		t.Fatalf("invalidação deve cobrir a tela inteira, veio %+v", rects[0])
	}

	// Depois de realinhado, o cache está no layout NOVO: frame idêntico -> 0 rects.
	// ESTA é a parte que o código antigo NÃO garantia de forma determinística.
	same := mkGrad(w*4 + 256)
	if r := d.Detect(same); len(r) != 0 {
		t.Fatalf("apos realinhar o cache, frame identico deve dar 0 rects, veio %d: %+v", len(r), r)
	}

	// E voltar ao stride compacto também invalida (a checagem é simétrica).
	back := d.Detect(mkGrad(w * 4))
	if len(back) != 1 || back[0].Width != w {
		t.Fatalf("volta ao stride compacto deve invalidar, veio %+v", back)
	}
}
