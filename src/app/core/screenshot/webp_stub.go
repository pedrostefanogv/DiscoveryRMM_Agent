//go:build !cgo

package screenshot

import (
	"fmt"

	"discovery/app/core/screen"
)

// Sem cgo/libwebp não há candidato WebP: o pipeline usa PNG (lossless) e JPEG
// (quando o PNG estoura o teto de payload).

func webpAvailable() bool { return false }

func encodeWebPLossless(frame *screen.Frame) ([]byte, error) {
	return nil, fmt.Errorf("webp indisponivel (cgo desabilitado)")
}

func encodeWebPLossy(frame *screen.Frame, quality int) ([]byte, error) {
	return nil, fmt.Errorf("webp indisponivel (cgo desabilitado)")
}
