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

	// Converte BGRA para RGBA (JPEG espera RGB)
	img := image.NewRGBA(image.Rect(0, 0, frame.Width, frame.Height))
	for y := 0; y < frame.Height; y++ {
		for x := 0; x < frame.Width; x++ {
			offset := y*frame.Stride + x*4
			dst := y*img.Stride + x*4
			img.Pix[dst] = frame.Data[offset+2]   // R ← B
			img.Pix[dst+1] = frame.Data[offset+1] // G ← G
			img.Pix[dst+2] = frame.Data[offset]   // B ← R
			img.Pix[dst+3] = 255                   // A = 255
		}
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
