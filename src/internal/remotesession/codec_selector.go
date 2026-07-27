package remotesession

import "discovery/internal/screen"

// CodecSelector seleciona o encoder apropriado baseado no perfil e GPU.
type CodecSelector struct {
	gpu screen.GPUCapability
}

func NewCodecSelector() *CodecSelector {
	return &CodecSelector{
		gpu: screen.DetectGPU(),
	}
}

// Select retorna o encoder ideal para o perfil e codec desejado.
// Prioridade: H.264 (GPU) > WebP (cgo) > JPEG (puro Go).
func (cs *CodecSelector) Select(profile string, preferredCodec string) screen.Encoder {
	switch preferredCodec {
	case "h264":
		if cs.gpu.MediaFoundationH264 {
			enc, err := screen.NewH264Encoder()
			if err == nil {
				return enc
			}
		}
		// Fallback OpenH264
		enc, err := screen.NewOpenH264Encoder()
		if err == nil {
			return enc
		}
		// Fallback WebP se H.264 indisponivel
		return cs.Select(profile, "webp")

	case "webp":
		return screen.NewWebPEncoder()

	default:
		return screen.NewJPEGEncoder()
	}
}

// HasGPU reports whether GPU-accelerated H.264 encoding is available.
func (cs *CodecSelector) HasGPU() bool {
	return cs.gpu.MediaFoundationH264
}
