//go:build cgo

package screenshot

import (
	"fmt"
	"image"

	"github.com/chai2010/webp"

	"discovery/app/core/screen"
)

// ── WebP (libwebp via cgo) ────────────────────────────────────────────────
//
// WebP lossless entrega o MESMO conteúdo do PNG com 20-35% menos bytes em
// capturas de UI/texto — é o formato preferido do pipeline. Sem cgo os
// candidatos WebP simplesmente não existem e o pipeline cai para PNG/JPEG.

// init registra o decoder WebP no image.Decode: o canvas do overlay pode
// enviar a imagem anotada em image/webp (menor que PNG) e o backend precisa
// decodificá-la para normalizar/reencodar.
func init() {
	image.RegisterFormat("webp", "RIFF????WEBP", webp.Decode, webp.DecodeConfig)
}

// webpAvailable informa se o encoder/decoder WebP está disponível.
func webpAvailable() bool { return true }

// encodeWebPLossless codifica o frame sem perdas (mesma qualidade do PNG).
func encodeWebPLossless(frame *screen.Frame) ([]byte, error) {
	if frame == nil || len(frame.Data) == 0 {
		return nil, fmt.Errorf("frame vazio")
	}
	data, err := webp.EncodeLosslessRGBA(bgraToRGBA(frame))
	if err != nil {
		return nil, fmt.Errorf("webp lossless: %w", err)
	}
	return data, nil
}

// encodeWebPLossy codifica o frame com perdas na qualidade informada.
func encodeWebPLossy(frame *screen.Frame, quality int) ([]byte, error) {
	if frame == nil || len(frame.Data) == 0 {
		return nil, fmt.Errorf("frame vazio")
	}
	if quality <= 0 {
		quality = defaultJPEGQuality
	}
	if quality > 100 {
		quality = 100
	}
	data, err := webp.EncodeRGBA(bgraToRGBA(frame), float32(quality))
	if err != nil {
		return nil, fmt.Errorf("webp lossy: %w", err)
	}
	return data, nil
}
