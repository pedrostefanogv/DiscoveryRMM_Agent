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

	// Conversao BGRA→RGBA in-place (swap B↔R) para evitar alocacao extra.
	// O buffer GDI eh alocado fresh a cada AcquireNextFrame, entao modificar
	// in-place eh seguro. Usamos stride para lidar com padding.
	bgra := frame.Data
	stride := frame.Stride
	for y := 0; y < frame.Height; y++ {
		rowStart := y * stride
		for x := 0; x < frame.Width; x++ {
			offset := rowStart + x*4
			bgra[offset], bgra[offset+2] = bgra[offset+2], bgra[offset] // swap B↔R
		}
	}

	img := &image.RGBA{
		Pix:    frame.Data,
		Stride: stride,
		Rect:   image.Rect(0, 0, frame.Width, frame.Height),
	}

	var buf bytes.Buffer
	err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality})
	if err != nil {
		return nil, fmt.Errorf("jpeg encode: %w", err)
	}
	return buf.Bytes(), nil
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
