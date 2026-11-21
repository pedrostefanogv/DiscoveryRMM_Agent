//go:build cgo
// +build cgo

package screen

import (
	"bytes"
	"fmt"
	"image"

	// cgo: libwebp
	"github.com/chai2010/webp"
)

// webpEncoder comprime frames via libwebp (cgo).
// Reutiliza rgbaScratch entre frames (mesmo padrão do jpegEncoder) para evitar
// alocar ~20 MB por quadro em resoluções ultrawide.
type webpEncoder struct {
	rgbaScratch []byte
}

func NewWebPEncoder() Encoder {
	return &webpEncoder{}
}

func (e *webpEncoder) Encode(frame *Frame, quality int) ([]byte, error) {
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

	if cap(e.rgbaScratch) < len(bgra) {
		e.rgbaScratch = make([]byte, len(bgra))
	}
	rgba := e.rgbaScratch[:len(bgra)]

	if stride%4 == 0 && len(bgra)%4 == 0 {
		CopyBGRAtoRGBA(rgba, bgra)
	} else {
		for y := 0; y < h; y++ {
			rowSrc := y * stride
			rowDst := y * stride
			for x := 0; x < w; x++ {
				offset := rowSrc + x*4
				dst := rowDst + x*4
				rgba[dst] = bgra[offset+2]   // R
				rgba[dst+1] = bgra[offset+1] // G
				rgba[dst+2] = bgra[offset]   // B
				rgba[dst+3] = 255            // A opaco
			}
		}
	}

	img := &image.RGBA{
		Pix:    rgba,
		Stride: stride,
		Rect:   image.Rect(0, 0, w, h),
	}

	var buf bytes.Buffer
	if err := webp.Encode(&buf, img, &webp.Options{Lossless: false, Quality: float32(quality)}); err != nil {
		return nil, fmt.Errorf("webp encode: %w", err)
	}
	return buf.Bytes(), nil
}

func encodeRGBAWebP(buf *bytes.Buffer, img *image.RGBA, quality int) error {
	return webp.Encode(buf, img, &webp.Options{Lossless: false, Quality: float32(quality)})
}

func (e *webpEncoder) Name() string { return "webp" }

var _ Encoder = (*webpEncoder)(nil)
