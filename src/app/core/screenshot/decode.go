package screenshot

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	// Registra os decoders PNG/JPEG usados por image.Decode.
	_ "image/jpeg"
	_ "image/png"
	"strings"
)

// decodedImageMaxBase64 limita o tamanho da data URL anotada aceita do overlay.
// Com o overlay em resolução ORIGINAL o PNG do canvas pode ficar grande
// (desktop 4K/5K anotado) — o teto protege contra payload absurdo, sem rejeitar
// capturas legítimas. A imagem final enviada ao modelo continua limitada por
// maxPNGBytes, que é o teto que o servidor exige.
const decodedImageMaxBase64 = 16 << 20

// DecodeDataURLImage decodifica a imagem anotada gerada pelo canvas do overlay
// (data:image/png ou data:image/webp, base64) e devolve bytes normalizados para
// visão do LLM: downscale até maxDim e reencode no formato pedido ("auto"
// prefere WebP lossless quando menor; "png" força PNG). Aceita apenas data URLs
// de imagem — nunca URLs externas.
func DecodeDataURLImage(dataURL string, maxDim, quality int, format string) ([]byte, string, int, int, error) {
	img, err := DecodeDataURLToImage(dataURL)
	if err != nil {
		return nil, "", 0, 0, err
	}
	frame := imageToBGRAFrame(downscaleImage(img, maxDim))
	if frame == nil {
		return nil, "", 0, 0, fmt.Errorf("imagem anotada vazia")
	}
	data, mime, err := EncodeFrameFormat(frame, quality, -1, format)
	if err != nil {
		return nil, "", 0, 0, err
	}
	return data, mime, frame.Width, frame.Height, nil
}

// DecodeDataURLToImage decodifica uma data URL de imagem (base64) em
// image.Image. Mesmas validações de DecodeDataURLImage (prefixo data:image/,
// base64 obrigatório e teto de tamanho) — usado pelo "copiar para a área de
// transferência".
func DecodeDataURLToImage(dataURL string) (image.Image, error) {
	value := strings.TrimSpace(dataURL)
	if !strings.HasPrefix(value, "data:image/") {
		return nil, fmt.Errorf("data URL de imagem invalida")
	}
	if len(value) > decodedImageMaxBase64 {
		return nil, fmt.Errorf("imagem grande demais (%d bytes)", len(value))
	}
	comma := strings.Index(value, ",")
	if comma < 0 {
		return nil, fmt.Errorf("data URL de imagem sem payload")
	}
	if !strings.Contains(strings.ToLower(value[:comma]), ";base64") {
		return nil, fmt.Errorf("data URL de imagem nao esta em base64")
	}
	raw, err := base64.StdEncoding.DecodeString(value[comma+1:])
	if err != nil {
		return nil, fmt.Errorf("base64 invalido: %w", err)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("formato de imagem nao suportado: %w", err)
	}
	return img, nil
}

// NormalizeEncodedImage decodifica bytes de imagem já codificados (ex.: o PNG
// devolvido pelo processo auxiliar de PrintWindow) e reencoda dentro dos limites:
// downscale até maxDim, PNG quando couber em maxBytes e JPEG caso contrário.
// Devolve bytes, mime e dimensões finais.
func NormalizeEncodedImage(data []byte, maxDim, quality, maxBytes int) ([]byte, string, int, int, error) {
	if len(data) == 0 {
		return nil, "", 0, 0, fmt.Errorf("imagem vazia")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", 0, 0, fmt.Errorf("formato de imagem nao suportado: %w", err)
	}
	frame := imageToBGRAFrame(downscaleImage(img, maxDim))
	if frame == nil {
		return nil, "", 0, 0, fmt.Errorf("imagem vazia")
	}
	data, mime, err := EncodeFrameFormat(frame, quality, -1, FormatAuto)
	if err != nil {
		return nil, "", 0, 0, err
	}
	return data, mime, frame.Width, frame.Height, nil
}

// downscaleImage reduz a imagem se o maior lado exceder maxDim usando média de
// ÁREA (box filter). Antes era nearest-neighbor — que descartava pixels e
// serrilhava texto fino e traços de anotação de 1px ao reduzir a imagem anotada
// do overlay (e o resultado do PrintWindow normalizado por NormalizeEncodedImage).
//
// A média é feita em RGBA 16-bit pré-multiplicado (o que image.Image.RGBA
// devolve) e o destino é image.RGBA, que também é pré-multiplicado — assim a
// conta fica correta mesmo com transparência.
func downscaleImage(src image.Image, maxDim int) image.Image {
	if src == nil || maxDim <= 0 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	longest := w
	if h > longest {
		longest = h
	}
	if longest <= maxDim {
		return src
	}
	scale := float64(maxDim) / float64(longest)
	nw := int(float64(w) * scale)
	nh := int(float64(h) * scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		y0 := b.Min.Y + y*h/nh
		y1 := b.Min.Y + (y+1)*h/nh
		if y1 <= y0 {
			y1 = y0 + 1
		}
		if y1 > b.Max.Y {
			y1 = b.Max.Y
		}
		for x := 0; x < nw; x++ {
			x0 := b.Min.X + x*w/nw
			x1 := b.Min.X + (x+1)*w/nw
			if x1 <= x0 {
				x1 = x0 + 1
			}
			if x1 > b.Max.X {
				x1 = b.Max.X
			}
			var sumR, sumG, sumB, sumA uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					r, g, bl, a := src.At(sx, sy).RGBA()
					sumR += uint64(r)
					sumG += uint64(g)
					sumB += uint64(bl)
					sumA += uint64(a)
				}
			}
			count := uint64((y1 - y0) * (x1 - x0))
			if count == 0 {
				continue
			}
			half := count / 2
			dst.Set(x, y, color.RGBA64{
				R: uint16((sumR + half) / count),
				G: uint16((sumG + half) / count),
				B: uint16((sumB + half) / count),
				A: uint16((sumA + half) / count),
			})
		}
	}
	return dst
}
