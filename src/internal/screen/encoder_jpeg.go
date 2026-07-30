package screen

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"

	"github.com/klauspost/compress/zstd"
)

// Encoder comprime frames em um formato especifico para envio.
type Encoder interface {
	// Encode comprime um Frame raw BGRA em bytes prontos para envio.
	Encode(frame *Frame, quality int) ([]byte, error)
	// Name retorna o nome do codec (jpeg, webp, h264).
	Name() string
}

// ── JPEG Encoder ──

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

	// Aplica ScaleFactor se frame ja foi redimensionado no capturador
	// (w/h ja sao as dimensoes efetivas apos ResizeBGRA)

	// Conversao BGRA→RGBA in-place (swap B↔R) para evitar alocacao extra.
	// O buffer GDI eh alocado fresh a cada AcquireNextFrame, entao modificar
	// in-place eh seguro. Usamos stride para lidar com padding.
	for y := 0; y < h; y++ {
		rowStart := y * stride
		for x := 0; x < w; x++ {
			offset := rowStart + x*4
			bgra[offset], bgra[offset+2] = bgra[offset+2], bgra[offset] // swap B↔R
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

// ResizeBGRA redimensiona um frame BGRA usando nearest-neighbor (rápido).
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

	for y := 0; y < newH; y++ {
		srcY := int(float64(y) / scaleFactor)
		rowDst := y * newStride
		rowSrc := srcY * src.Stride
		for x := 0; x < newW; x++ {
			srcX := int(float64(x) / scaleFactor)
			offsetDst := rowDst + x*4
			offsetSrc := rowSrc + srcX*4
			copy(dst.Data[offsetDst:offsetDst+4], src.Data[offsetSrc:offsetSrc+4])
		}
	}

	return dst
}

func (e *jpegEncoder) Name() string { return "jpeg" }

// ── Compressao adicional ──

// CompressZstd comprime dados binarios com Zstd para reducao adicional de banda.
func CompressZstd(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(data); err != nil {
		w.Close()
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// DecompressZstd descomprime dados com Zstd.
func DecompressZstd(data []byte) ([]byte, error) {
	r, err := zstd.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

var _ Encoder = (*jpegEncoder)(nil)
