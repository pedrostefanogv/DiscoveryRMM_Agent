//go:build windows

package remotesession

import "discovery/app/core/screen"

// CodecSelector seleciona o encoder apropriado baseado no codec solicitado.
type CodecSelector struct {
	preferredCodec string
}

func NewCodecSelector() CodecSelector {
	return CodecSelector{
		preferredCodec: "auto",
	}
}

// SetPreferred define o codec preferido (auto/jpeg/webp).
func (cs *CodecSelector) SetPreferred(codec string) {
	cs.preferredCodec = codec
}

// Select retorna o encoder para o codec desejado.
// Suporta: webp (build tag webp, cgo libwebp) e jpeg (padrão, puro Go).
// H.264 foi desativado (licença/patente AVC) — cai para WebP/JPEG.
// Nunca retorna nil.
func (cs *CodecSelector) Select(profile string, preferredCodec string) screen.Encoder {
	switch preferredCodec {
	case "webp":
		if enc := screen.NewWebPEncoder(); enc != nil {
			return enc
		}
		return screen.NewJPEGEncoder()
	default:
		return screen.NewJPEGEncoder()
	}
}
