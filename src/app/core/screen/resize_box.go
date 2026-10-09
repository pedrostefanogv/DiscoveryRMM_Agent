package screen

// ResizeBGRABox reduz um frame BGRA 8-bit com filtro de ÁREA (box filter): cada
// pixel de destino é a média de TODOS os pixels de origem que ele cobre.
//
// Por que não bilinear: ao reduzir uma tela grande (4K→2K, ultrawide→4K), a
// interpolação bilinear amostra apenas 4 pixels de origem e DESCARTA a maioria —
// linhas de 1px de UI, texto miúdo e bordas de janela somem/trepidam (aliasing).
// A média de área usa todos os pixels, que é o comportamento correto para
// screenshot de interface (equivale ao downscale de alta qualidade dos editores).
//
// Custo: O(pixels de origem), uma passada, sem alocação além do destino.
func ResizeBGRABox(src *Frame, scaleFactor float64) *Frame {
	if src == nil || scaleFactor >= 1.0 {
		return src
	}
	// HDR (scRGB float, 8 bytes/px) ou frame inválido: mantém o caminho antigo.
	if src.ColorSpace != 0 || src.Width <= 0 || src.Height <= 0 {
		return ResizeBGRA(src, scaleFactor)
	}

	newW := int(float64(src.Width) * scaleFactor)
	newH := int(float64(src.Height) * scaleFactor)
	if newW < 1 {
		newW = 1
	}
	if newH < 1 {
		newH = 1
	}
	srcStride := src.Stride
	if srcStride <= 0 {
		srcStride = src.Width * 4
	}
	newStride := newW * 4
	dst := &Frame{
		Data:       make([]byte, newStride*newH),
		Width:      newW,
		Height:     newH,
		Stride:     newStride,
		OriginX:    src.OriginX,
		OriginY:    src.OriginY,
		ColorSpace: src.ColorSpace,
	}

	for y := 0; y < newH; y++ {
		// Faixa de origem do pixel de destino: mapeamento inteiro garante que
		// cada pixel de origem seja contado exatamente uma vez (sem buracos e
		// sem peso duplicado nas bordas).
		y0 := y * src.Height / newH
		y1 := (y + 1) * src.Height / newH
		if y1 <= y0 {
			y1 = y0 + 1
		}
		if y1 > src.Height {
			y1 = src.Height
		}
		for x := 0; x < newW; x++ {
			x0 := x * src.Width / newW
			x1 := (x + 1) * src.Width / newW
			if x1 <= x0 {
				x1 = x0 + 1
			}
			if x1 > src.Width {
				x1 = src.Width
			}

			var sumB, sumG, sumR, sumA uint64
			for sy := y0; sy < y1; sy++ {
				off := sy*srcStride + x0*4
				for sx := x0; sx < x1; sx++ {
					sumB += uint64(src.Data[off])
					sumG += uint64(src.Data[off+1])
					sumR += uint64(src.Data[off+2])
					sumA += uint64(src.Data[off+3])
					off += 4
				}
			}
			count := uint64((y1 - y0) * (x1 - x0))
			if count == 0 {
				continue
			}
			o := y*newStride + x*4
			dst.Data[o] = byte(sumB / count)
			dst.Data[o+1] = byte(sumG / count)
			dst.Data[o+2] = byte(sumR / count)
			dst.Data[o+3] = byte(sumA / count)
		}
	}

	return dst
}
