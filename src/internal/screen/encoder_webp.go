//go:build webp
// +build webp

package screen

import (
	"bytes"
	"fmt"
	"image"

	// cgo: libwebp
	"github.com/chai2010/webp"
)

// webpEncoder comprime frames via libwebp (cgo).
type webpEncoder struct{}

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

	img := image.NewRGBA(image.Rect(0, 0, frame.Width, frame.Height))
	for y := 0; y < frame.Height; y++ {
		for x := 0; x < frame.Width; x++ {
			offset := y*frame.Stride + x*4
			dst := y*img.Stride + x*4
			img.Pix[dst] = frame.Data[offset+2]   // R
			img.Pix[dst+1] = frame.Data[offset+1] // G
			img.Pix[dst+2] = frame.Data[offset]   // B
			img.Pix[dst+3] = 255
		}
	}

	var buf bytes.Buffer
	if err := webp.Encode(&buf, img, &webp.Options{Lossless: false, Quality: float32(quality)}); err != nil {
		return nil, fmt.Errorf("webp encode: %w", err)
	}
	return buf.Bytes(), nil
}

func (e *webpEncoder) Name() string { return "webp" }

var _ Encoder = (*webpEncoder)(nil)
