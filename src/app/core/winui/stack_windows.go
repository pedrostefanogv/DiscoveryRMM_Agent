//go:build windows

package winui

import "sync"

// ── Empilhamento vertical de notificações no canto inferior direito ──
//
// Vários cartões nativos (consentimento remoto, convite de chat, PSADT,
// progresso, balão da bandeja) ancoram no canto inferior direito do monitor
// ativo. Este coordenador reserva deslocamentos verticais por HWND/ID para que
// cartões simultâneos empilhem de baixo para cima sem se sobrepor.

type stackSlot struct {
	id     uintptr
	height int
	gap    int
}

var (
	bottomRightMu    sync.Mutex
	bottomRightSlots []stackSlot
)

// BottomRightStackedOnActiveWorkArea calcula a posição (x, y) no canto
// inferior direito da área útil ativa, deslocando para cima conforme a soma das
// alturas dos cartões já visíveis, e registra o slot para o id informado.
// Se id já estiver registrado, atualiza a altura/gap sem duplicar.
func BottomRightStackedOnActiveWorkArea(id uintptr, width, height, margin, gap int) (x, y int) {
	if gap <= 0 {
		gap = ScalePx(12, 1)
	}
	area := ActiveWorkArea()
	x, y = bottomRightInArea(area, width, height, margin)
	if id == 0 {
		return x, y
	}

	bottomRightMu.Lock()
	defer bottomRightMu.Unlock()

	offset := 0
	found := false
	for i := range bottomRightSlots {
		if bottomRightSlots[i].id == id {
			bottomRightSlots[i].height = height
			bottomRightSlots[i].gap = gap
			found = true
			break
		}
		offset += bottomRightSlots[i].height + bottomRightSlots[i].gap
	}
	if !found {
		bottomRightSlots = append(bottomRightSlots, stackSlot{
			id:     id,
			height: height,
			gap:    gap,
		})
	}

	y -= offset
	if y < area.Y+margin {
		y = area.Y + margin
	}
	return x, y
}

// ReleaseBottomRightSlot libera a reserva de empilhamento do id informado.
func ReleaseBottomRightSlot(id uintptr) {
	if id == 0 {
		return
	}
	bottomRightMu.Lock()
	defer bottomRightMu.Unlock()
	for i := range bottomRightSlots {
		if bottomRightSlots[i].id == id {
			bottomRightSlots = append(bottomRightSlots[:i], bottomRightSlots[i+1:]...)
			return
		}
	}
}

// ClampScaleToWorkArea reduz a escala proporcionalmente quando o cartão
// escalado (baseWidth x baseHeight com baseMargin) excede a área útil do
// monitor ativo (ex.: 1024x768 a 200% DPI).
func ClampScaleToWorkArea(area Rect, baseWidth, baseHeight, baseMargin int, scale float64) float64 {
	if scale <= 0 {
		scale = 1
	}
	for i := 0; i < 3; i++ {
		width := ScalePx(baseWidth, scale)
		height := ScalePx(baseHeight, scale)
		margin := ScalePx(baseMargin, scale)
		maxWidth := area.W - 2*margin
		maxHeight := area.H - 2*margin
		if maxWidth <= 0 || maxHeight <= 0 {
			break
		}
		factor := 1.0
		if width > maxWidth {
			factor = float64(maxWidth) / float64(width)
		}
		if height > maxHeight {
			if f := float64(maxHeight) / float64(height); f < factor {
				factor = f
			}
		}
		if factor >= 1 {
			break
		}
		scale *= factor
	}
	return scale
}
