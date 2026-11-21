package screen

import "sort"

// Consolidação de dirty rects de HARDWARE para o protocolo de tiles.
//
// POR QUE ISTO EXISTE (Fase 6, 2026-11-20):
// o DXGI Desktop Duplication devolve as regiões alteradas do jeito que a GPU
// trabalhou — tipicamente MUITOS retângulos pequenos e sobrepostos (uma janela
// arrastada gera um retângulo por apresentação). O contrato do nosso viewer é
// outro: um payload de tiles com header [numRects uint16][flags] e, por tile,
// [x][y][w][h][size] + JPEG. E o decoder do viewer tem um guard explícito:
//
//	RemoteScreenViewer.tsx:298 -> return numRects >= 1 && numRects < 512;
//
// Um numRects >= 512 faz o decoder FALHAR a detecção de tiles e tentar
// interpretar o payload como um JPEG único — tela corrompida/preta. Portanto a
// consolidação não é só otimização: é requisito de correção.
//
// A estratégia tem três etapas, nesta ordem:
//  1. NORMALIZAR: recortar em [0,w)x[0,h) e descartar vazios/negativos.
//  2. UNIR sobrepostos: retângulos que se tocam são fundidos num só. Menos
//     tiles = menos headers (12 B cada) e melhor taxa de compressão, pelo mesmo
//     motivo que o MeshAgent agrupa tiles vizinhos.
//  3. LIMITAR: se ainda passar do teto, os menores são absorvidos pelos maiores
//     (o retângulo absorvente cresce) até caber. Cortar tiles seria PIOR que
//     fundir: cortar deixa a região sem atualização (artefato permanente na
//     tela do viewer), fundir só paga banda extra.

// maxDirtyRectsProtocol é o teto DURO imposto pelo decoder do viewer
// (numRects < 512). Deixamos margem: 256 é o alvo de trabalho.
const maxDirtyRectsProtocol = 256

// CoalesceDirtyRects normaliza, une e limita uma lista de dirty rects para o
// que o protocolo de tiles do viewer aceita.
//
// Devolve uma lista NOVA (não reutiliza o slice de entrada — o chamador pode
// ter recebido o slice do capturer, que é válido só até o próximo frame).
// Devolve nil quando não sobra nenhum rect útil; o chamador trata nil como
// "frame completo".
func CoalesceDirtyRects(rects []DirtyRect, width, height int) []DirtyRect {
	if len(rects) == 0 || width <= 0 || height <= 0 {
		return nil
	}

	// ── 1. Normalizar ──
	norm := make([]DirtyRect, 0, len(rects))
	for _, r := range rects {
		if r.Width <= 0 || r.Height <= 0 {
			continue
		}
		x0, y0 := r.X, r.Y
		x1, y1 := r.X+r.Width, r.Y+r.Height
		if x0 < 0 {
			x0 = 0
		}
		if y0 < 0 {
			y0 = 0
		}
		if x1 > width {
			x1 = width
		}
		if y1 > height {
			y1 = height
		}
		if x1 <= x0 || y1 <= y0 {
			continue
		}
		norm = append(norm, DirtyRect{X: x0, Y: y0, Width: x1 - x0, Height: y1 - y0})
	}
	if len(norm) == 0 {
		return nil
	}

	// ── 2. Unir sobrepostos ──
	merged := mergeOverlapping(norm)

	// ── 3. Limitar ao teto do protocolo ──
	if len(merged) > maxDirtyRectsProtocol {
		merged = absorbSmallest(merged, maxDirtyRectsProtocol)
	}

	// Ordena por área (maiores primeiro), como o mergeTilesFlat do diff de
	// software — o viewer desenha na ordem recebida e uma base maior primeiro
	// reduz trabalho de sobreposição.
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Width*merged[i].Height > merged[j].Width*merged[j].Height
	})
	return merged
}

// mergeOverlapping funde retângulos que se sobrepõem (ou se tocam) até estabilizar.
//
// Implementação: varredura repetida (O(n²) por passada, com poucas passadas na
// prática). O caso comum do DXGI são dezenas de rects; o guard de teto em
// CoalesceDirtyRects limita o pior caso antes de chegar aqui quando a lista é
// gigante.
func mergeOverlapping(in []DirtyRect) []DirtyRect {
	cur := in
	for pass := 0; pass < 8; pass++ {
		merged := make([]DirtyRect, 0, len(cur))
		used := make([]bool, len(cur))
		changed := false

		for i := 0; i < len(cur); i++ {
			if used[i] {
				continue
			}
			acc := cur[i]
			used[i] = true
			// Absorve tudo que toca o acumulado (uma passada basta para o caso
			// comum; o laço externo repete enquanto houver fusão).
			for j := i + 1; j < len(cur); j++ {
				if used[j] {
					continue
				}
				if rectsTouch(acc, cur[j]) {
					acc = unionRect(acc, cur[j])
					used[j] = true
					changed = true
				}
			}
			merged = append(merged, acc)
		}

		cur = merged
		if !changed {
			break
		}
	}
	return cur
}

// rectsTouch informa se dois retângulos se sobrepõem ou se tocam nas bordas.
// Tocar conta como unir: dois tiles adjacentes viram um só JPEG com header
// menor e compressão melhor (mesma lógica da coalescência do MeshAgent).
func rectsTouch(a, b DirtyRect) bool {
	return a.X <= b.X+b.Width && b.X <= a.X+a.Width &&
		a.Y <= b.Y+b.Height && b.Y <= a.Y+a.Height
}

func unionRect(a, b DirtyRect) DirtyRect {
	x0 := min(a.X, b.X)
	y0 := min(a.Y, b.Y)
	x1 := max(a.X+a.Width, b.X+b.Width)
	y1 := max(a.Y+a.Height, b.Y+b.Height)
	return DirtyRect{X: x0, Y: y0, Width: x1 - x0, Height: y1 - y0}
}

// absorbSmallest reduz a lista ao teto fundindo os MENORES retângulos no
// retângulo que menos área adiciona. Fundir é sempre preferível a descartar:
// descartar deixa aquela região da tela do viewer desatualizada para sempre.
func absorbSmallest(rects []DirtyRect, limit int) []DirtyRect {
	if limit < 1 {
		limit = 1
	}
	out := make([]DirtyRect, len(rects))
	copy(out, rects)

	for len(out) > limit {
		// Índice do menor por área.
		smallest := 0
		for i := 1; i < len(out); i++ {
			if area(out[i]) < area(out[smallest]) {
				smallest = i
			}
		}
		// Escolhe o absorvente que gera a MENOR união em área.
		best := -1
		bestArea := 0
		for i := range out {
			if i == smallest {
				continue
			}
			u := area(unionRect(out[i], out[smallest]))
			if best == -1 || u < bestArea {
				best = i
				bestArea = u
			}
		}
		if best == -1 {
			break
		}
		out[best] = unionRect(out[best], out[smallest])
		// Remove o menor (swap-and-truncate, ordem não importa aqui).
		out[smallest] = out[len(out)-1]
		out = out[:len(out)-1]
	}
	return out
}

func area(r DirtyRect) int { return r.Width * r.Height }

// DirtyRectsCoverage devolve a fração da tela coberta pela união dos rects
// (0..1). Usado para decidir entre frame completo e tiles: se os rects cobrem
// quase tudo, um único JPEG da tela inteira sai mais barato que N tiles.
//
// A conta é feita por grade: marcar as células cobertas e contá-las. Aproximada
// de propósito (serve para decisão, não para correção) e barata mesmo com
// centenas de rects.
func DirtyRectsCoverage(rects []DirtyRect, width, height int) float64 {
	if len(rects) == 0 || width <= 0 || height <= 0 {
		return 0
	}
	// Grade de 64x36 é suficiente para a decisão (passos de ~1,5% de área).
	const cols, rows = 64, 36
	var grid [cols * rows]bool
	for _, r := range rects {
		c0 := r.X * cols / width
		c1 := (r.X + r.Width) * cols / width
		r0 := r.Y * rows / height
		r1 := (r.Y + r.Height) * rows / height
		if c1 >= cols {
			c1 = cols - 1
		}
		if r1 >= rows {
			r1 = rows - 1
		}
		for rr := r0; rr <= r1; rr++ {
			for cc := c0; cc <= c1; cc++ {
				if rr >= 0 && rr < rows && cc >= 0 && cc < cols {
					grid[rr*cols+cc] = true
				}
			}
		}
	}
	hit := 0
	for _, v := range grid {
		if v {
			hit++
		}
	}
	return float64(hit) / float64(cols*rows)
}
