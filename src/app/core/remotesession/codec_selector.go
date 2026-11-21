//go:build windows

package remotesession

import (
	"fmt"
	"strings"

	"discovery/app/core/screen"
)

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

// ErrCodecUnavailable é devolvido quando um codec explícito foi pedido e não
// está disponível NESTE build. Ver SelectChecked.
type ErrCodecUnavailable struct {
	Codec  string
	Reason string
}

func (e *ErrCodecUnavailable) Error() string {
	return fmt.Sprintf("codec %q indisponível: %s", e.Codec, e.Reason)
}

// Select retorna o encoder para o codec desejado, com fallback silencioso para
// JPEG. Mantido para os caminhos internos que não têm como reportar erro.
//
// ATENÇÃO: para a APLICAÇÃO de um codec pedido pelo operador use
// SelectChecked. O fallback silencioso é exatamente o antipadrão que já causou
// o incidente do H.264 anunciado sem implementação (o viewer pedia um codec e
// recebia outro sem saber) — ver ANALISE_RMM_OPEN_SOURCE_ACESSO_REMOTO.
func (cs *CodecSelector) Select(profile string, preferredCodec string) screen.Encoder {
	enc, _ := cs.SelectChecked(preferredCodec)
	return enc
}

// SelectChecked resolve o encoder e REPORTA quando o codec pedido não existe
// ou não está disponível neste build.
//
// Contrato:
//   - "auto"/"" → JPEG (o codec universal, sempre disponível).
//   - "jpeg"     → JPEG.
//   - "webp"     → WebP quando o build tem cgo+libwebp; caso contrário erro
//     EXPLÍCITO (nunca JPEG silencioso).
//   - "h264"     → sempre erro, com o motivo real (política de licença).
//   - desconhecido → erro explícito em vez de cair em JPEG.
//
// Nunca devolve encoder nil: o chamador sempre tem algo para usar; o erro
// existe para o operador saber que NÃO recebeu o que pediu.
func (cs *CodecSelector) SelectChecked(preferredCodec string) (screen.Encoder, error) {
	switch strings.ToLower(strings.TrimSpace(preferredCodec)) {
	case "", "auto":
		return screen.NewJPEGEncoder(), nil

	case "jpeg", "jpg":
		return screen.NewJPEGEncoder(), nil

	case "webp":
		if enc := screen.NewWebPEncoder(); enc != nil {
			return enc, nil
		}
		// Build sem cgo/libwebp: o operador PEDIU webp e não tem. Dizer isso é
		// obrigatório; cair em JPEG em silêncio esconde a diferença de banda.
		return screen.NewJPEGEncoder(), &ErrCodecUnavailable{
			Codec:  "webp",
			Reason: "este build do agente não inclui libwebp (compilado sem cgo); usando JPEG",
		}

	case "h264", "avc":
		// H.264 NÃO é implementado no agente por decisão de licenciamento
		// (patentes AVC) — ver ANALISE_RMM_OPEN_SOURCE_ACESSO_REMOTO. É a mesma
		// classe de falha do incidente anterior: anunciar sem implementar. Aqui
		// o pedido é recusado com motivo, em vez de virar JPEG silencioso.
		return screen.NewJPEGEncoder(), &ErrCodecUnavailable{
			Codec:  "h264",
			Reason: "não implementado neste agente (política de licença AVC); usando JPEG",
		}

	default:
		return screen.NewJPEGEncoder(), &ErrCodecUnavailable{
			Codec:  preferredCodec,
			Reason: "codec desconhecido; usando JPEG",
		}
	}
}
