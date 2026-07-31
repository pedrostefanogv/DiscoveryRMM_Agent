package screen

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"unsafe"
)

// Encoder comprime frames em um formato especifico para envio.
type Encoder interface {
	// Encode comprime um Frame raw BGRA em bytes prontos para envio.
	Encode(frame *Frame, quality int) ([]byte, error)
	// Name retorna o nome do codec (jpeg, webp, h264).
	Name() string
}

// ── JPEG Encoder ──
// Usa image/jpeg da stdlib. Compatível com o viewer sem alterações.

type jpegEncoder struct{}

func NewJPEGEncoder() Encoder {
	return &jpegEncoder{}
}

func (e *jpegEncoder) Encode(frame *Frame, quality int) ([]byte, error) {
	if quality < 1 {
		quality = 50
	}
	if quality > 100 {
		quality = 100
	}

	bgra := frame.Data
	stride := frame.Stride
	w := frame.Width
	h := frame.Height

	// Conversao BGRA→RGBA in-place (swap B↔R) usando uint32 cast.
	// ~4x mais rapido que byte-by-byte: 1 operacao por pixel em vez de 3.
	// Requer que stride seja multiplo de 4 (sempre verdade para BGRA).
	if stride%4 == 0 {
		pixels := unsafe.Slice((*uint32)(unsafe.Pointer(&bgra[0])), len(bgra)/4)
		for i := range pixels {
			// BGRA: 0xAARRGGBB → RGBA: 0xAABBGGRR
			// Swap bytes 0 (B) e 2 (R) mantendo A e G
			c := pixels[i]
			pixels[i] = (c & 0xFF00FF00) | // A e G nos lugares certos
				((c & 0x000000FF) << 16) | // B → R
				((c & 0x00FF0000) >> 16) // R → B
		}
	} else {
		// Fallback: stride com padding — swap byte-by-byte
		for y := 0; y < h; y++ {
			rowStart := y * stride
			for x := 0; x < w; x++ {
				offset := rowStart + x*4
				bgra[offset], bgra[offset+2] = bgra[offset+2], bgra[offset]
			}
		}
	}

	img := &image.RGBA{
		Pix:    frame.Data,
		Stride: stride,
		Rect:   image.Rect(0, 0, w, h),
	}

	var buf bytes.Buffer
	err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality})
	if err != nil {
		return nil, fmt.Errorf("jpeg encode: %w", err)
	}
	return buf.Bytes(), nil
}

// ResizeBGRA redimensiona um frame BGRA.
// Usa interpolação bilinear (qualidade superior ao nearest-neighbor, comparável
// ao StretchBlt HALFTONE do MeshAgent) para scaleFactor < 1.
// Retorna novo buffer com as dimensões reduzidas. O original não é alterado.
func ResizeBGRA(src *Frame, scaleFactor float64) *Frame {
	if scaleFactor >= 1.0 {
		return src
	}

	newW := int(float64(src.Width) * scaleFactor)
	newH := int(float64(src.Height) * scaleFactor)
	if newW < 1 { newW = 1 }
	if newH < 1 { newH = 1 }

	newStride := newW * 4
	dst := &Frame{
		Data:   make([]byte, newStride*newH),
		Width:  newW,
		Height: newH,
		Stride: newStride,
	}

	srcStride := src.Stride
	srcW := src.Width
	srcH := src.Height
	inv := 1.0 / scaleFactor

	for y := 0; y < newH; y++ {
		// Posição em float na imagem original
		srcYf := float64(y) * inv
		y0 := int(srcYf)
		if y0 > srcH-1 { y0 = srcH - 1 }
		y1 := y0 + 1
		if y1 > srcH-1 { y1 = srcH - 1 }
		fy := srcYf - float64(y0)

		rowDst := y * newStride
		rowY0 := y0 * srcStride
		rowY1 := y1 * srcStride

		for x := 0; x < newW; x++ {
			srcXf := float64(x) * inv
			x0 := int(srcXf)
			if x0 > srcW-1 { x0 = srcW - 1 }
			x1 := x0 + 1
			if x1 > srcW-1 { x1 = srcW - 1 }
			fx := srcXf - float64(x0)

			offsetDst := rowDst + x*4

			// 4 pixels vizinhos
			o00 := rowY0 + x0*4
			o10 := rowY0 + x1*4
			o01 := rowY1 + x0*4
			o11 := rowY1 + x1*4

			// Bilinear por canal (B,G,R,A)
			for c := 0; c < 4; c++ {
				top := float64(src.Data[o00+c])*(1-fx) + float64(src.Data[o10+c])*fx
				bot := float64(src.Data[o01+c])*(1-fx) + float64(src.Data[o11+c])*fx
				val := top*(1-fy) + bot*fy
				if val < 0 { val = 0 }
				if val > 255 { val = 255 }
				dst.Data[offsetDst+c] = byte(val)
			}
		}
	}

	return dst
}

func (e *jpegEncoder) Name() string { return "jpeg" }

var _ Encoder = (*jpegEncoder)(nil)
