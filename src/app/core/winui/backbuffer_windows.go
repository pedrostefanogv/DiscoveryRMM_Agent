//go:build windows

package winui

// ── Back buffer reaproveitável (double buffering sem recriar recursos) ──
//
// BufferedPaint cria e destrói DC + bitmap a cada pintura. Isso é adequado
// para janelas estáticas, mas para animação (barra da contagem regressiva a
// ~60 fps) significaria alocar ~1 MB por quadro. O BackBuffer mantém os
// recursos vivos enquanto o tamanho não muda e permite reblitar apenas a
// região que realmente mudou.

// BackBuffer é um DC de memória compatível com a janela, com o bitmap próprio
// já selecionado. O zero value está pronto para uso; libere com Release.
type BackBuffer struct {
	dc        uintptr
	bitmap    uintptr
	oldBitmap uintptr
	width     int
	height    int
}

// Ensure garante um buffer do tamanho pedido, compatível com reference
// (normalmente o HDC de BeginPaint). Recria apenas quando o tamanho muda.
func (b *BackBuffer) Ensure(reference uintptr, width, height int) bool {
	if reference == 0 || width <= 0 || height <= 0 {
		return false
	}
	if b.dc != 0 && b.width == width && b.height == height {
		return true
	}
	b.Release()

	dc, _, _ := procCreateCompatibleDC.Call(reference)
	if dc == 0 {
		return false
	}
	bitmap, _, _ := procCreateCompatibleBitmap.Call(reference, uintptr(width), uintptr(height))
	if bitmap == 0 {
		procDeleteDC.Call(dc)
		return false
	}
	oldBitmap, _, _ := procSelectObject.Call(dc, bitmap)
	b.dc, b.bitmap, b.oldBitmap = dc, bitmap, oldBitmap
	b.width, b.height = width, height
	return true
}

// DC devolve o HDC do buffer (0 quando não inicializado).
func (b *BackBuffer) DC() uintptr { return b.dc }

// Ready informa se o buffer existe.
func (b *BackBuffer) Ready() bool { return b.dc != 0 }

// Size devolve as dimensões atuais do buffer.
func (b *BackBuffer) Size() (width, height int) { return b.width, b.height }

// BlitTo copia a área informada (coordenadas de cliente) do buffer para
// dstDC. Área vazia copia o buffer inteiro; a área é recortada aos limites.
func (b *BackBuffer) BlitTo(dstDC uintptr, area Rect) {
	if b.dc == 0 || dstDC == 0 {
		return
	}
	if area.Empty() {
		area = Rect{W: b.width, H: b.height}
	}
	if area.X < 0 {
		area.W += area.X
		area.X = 0
	}
	if area.Y < 0 {
		area.H += area.Y
		area.Y = 0
	}
	if area.Right() > b.width {
		area.W = b.width - area.X
	}
	if area.Bottom() > b.height {
		area.H = b.height - area.Y
	}
	if area.Empty() {
		return
	}
	procBitBlt.Call(
		dstDC,
		uintptr(area.X), uintptr(area.Y), uintptr(area.W), uintptr(area.H),
		b.dc, uintptr(area.X), uintptr(area.Y),
		srcCopy,
	)
}

// Release libera o bitmap e o DC de memória.
func (b *BackBuffer) Release() {
	if b.dc == 0 {
		return
	}
	if b.oldBitmap != 0 {
		procSelectObject.Call(b.dc, b.oldBitmap)
	}
	if b.bitmap != 0 {
		procDeleteObject.Call(b.bitmap)
	}
	procDeleteDC.Call(b.dc)
	*b = BackBuffer{}
}
