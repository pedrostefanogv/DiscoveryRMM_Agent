package screenshot

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"strings"
	"unsafe"

	"discovery/app/core/screen"
)

const (
	// defaultMaxDimension é o teto do lado maior da imagem enviada ao LLM.
	// 1600 px deixava textos/legendas ilegíveis; 2560 preserva leitura de UI e
	// ainda fica dentro do orçamento de visão da maioria dos modelos.
	defaultMaxDimension = 2560
	// defaultJPEGQuality só é usado quando o PNG passa de maxPNGBytes (print
	// fotográfico/4K). 90 evita o ringing que borrava texto no JPEG 80.
	defaultJPEGQuality = 90
	// maxPNGBytes acima do qual a imagem é reencodada em JPEG. PNG é lossless —
	// é o formato preferido para capturas de UI/texto; o teto existe apenas para
	// não estourar o limite de payload do servidor (~6 MiB de base64).
	maxPNGBytes = 2_900_000
	// Miniatura exibida no chat (o que a IA viu).
	thumbnailMaxDimension = 480
	thumbnailQuality      = 60
)

// CropFrame recorta uma sub-região de um frame BGRA. Retorna um frame novo
// (o original não é alterado). Coordenadas fora dos limites são recortadas;
// uma região vazia retorna erro.
func CropFrame(src *screen.Frame, x, y, w, h int) (*screen.Frame, error) {
	if src == nil || src.Width <= 0 || src.Height <= 0 {
		return nil, fmt.Errorf("frame de origem invalido")
	}
	if x < 0 {
		w += x
		x = 0
	}
	if y < 0 {
		h += y
		y = 0
	}
	if x >= src.Width || y >= src.Height {
		return nil, fmt.Errorf("regiao fora do frame (%d,%d)", x, y)
	}
	if x+w > src.Width {
		w = src.Width - x
	}
	if y+h > src.Height {
		h = src.Height - y
	}
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("regiao de recorte vazia")
	}
	stride := w * 4
	dst := &screen.Frame{Data: make([]byte, stride*h), Width: w, Height: h, Stride: stride}
	for row := 0; row < h; row++ {
		srcStart := (y+row)*src.Stride + x*4
		copy(dst.Data[row*stride:(row+1)*stride], src.Data[srcStart:srcStart+stride])
	}
	dst.OriginX = src.OriginX + x
	dst.OriginY = src.OriginY + y
	return dst, nil
}

// Downscale reduz o frame quando o maior lado excede maxDim (bilinear).
func Downscale(f *screen.Frame, maxDim int) *screen.Frame {
	if f == nil || maxDim <= 0 {
		return f
	}
	longest := f.Width
	if f.Height > longest {
		longest = f.Height
	}
	if longest <= maxDim {
		return f
	}
	scale := float64(maxDim) / float64(longest)
	return screen.ResizeBGRA(f, scale)
}

// bgraToRGBA converte um frame BGRA para image.RGBA (cópia independente).
func bgraToRGBA(f *screen.Frame) *image.RGBA {
	stride := f.Stride
	if stride <= 0 {
		stride = f.Width * 4
	}
	rgba := make([]byte, f.Height*stride)
	copy(rgba, f.Data)
	if stride%4 == 0 && len(rgba) >= 4 {
		pixels := unsafe.Slice((*uint32)(unsafe.Pointer(&rgba[0])), len(rgba)/4)
		for i := range pixels {
			c := pixels[i]
			pixels[i] = (c & 0xFF00FF00) | ((c & 0x000000FF) << 16) | ((c & 0x00FF0000) >> 16)
		}
	} else {
		for y := 0; y < f.Height; y++ {
			rowStart := y * stride
			for x := 0; x < f.Width; x++ {
				off := rowStart + x*4
				rgba[off], rgba[off+2] = rgba[off+2], rgba[off]
			}
		}
	}
	return &image.RGBA{Pix: rgba, Stride: stride, Rect: image.Rect(0, 0, f.Width, f.Height)}
}

// ScaledDimensions devolve as dimensões que Downscale produz para maxDim.
// É usado quando o chamador precisa dos tamanhos do frame JÁ reduzido — o
// overlay de captura, por exemplo, entrega ao frontend a imagem reduzida e as
// dimensões PRECISAM casar com ela (senão o recorte/anotação sai deslocado).
func ScaledDimensions(w, h, maxDim int) (int, int) {
	if w <= 0 || h <= 0 || maxDim <= 0 {
		return w, h
	}
	longest := w
	if h > longest {
		longest = h
	}
	if longest <= maxDim {
		return w, h
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
	return nw, nh
}

// imageToBGRAFrame converte uma imagem decodificada (ex.: PNG do processo
// auxiliar de PrintWindow) para o Frame BGRA usado pelo pipeline de captura.
func imageToBGRAFrame(img image.Image) *screen.Frame {
	if img == nil {
		return nil
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 {
		return nil
	}
	stride := w * 4
	data := make([]byte, stride*h)
	if rgba, ok := img.(*image.RGBA); ok {
		for y := 0; y < h; y++ {
			srcRow := rgba.PixOffset(bounds.Min.X, bounds.Min.Y+y)
			dstRow := y * stride
			for x := 0; x < w; x++ {
				s := rgba.Pix[srcRow+x*4:]
				d := data[dstRow+x*4:]
				d[0], d[1], d[2], d[3] = s[2], s[1], s[0], s[3]
			}
		}
		return &screen.Frame{Data: data, Width: w, Height: h, Stride: stride}
	}
	for y := 0; y < h; y++ {
		dstRow := y * stride
		for x := 0; x < w; x++ {
			r, g, b, a := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			d := data[dstRow+x*4:]
			d[0] = byte(b >> 8)
			d[1] = byte(g >> 8)
			d[2] = byte(r >> 8)
			d[3] = byte(a >> 8)
		}
	}
	return &screen.Frame{Data: data, Width: w, Height: h, Stride: stride}
}

// EncodePNG codifica o frame como PNG.
func EncodePNG(f *screen.Frame) ([]byte, error) {
	if f == nil || len(f.Data) == 0 {
		return nil, fmt.Errorf("frame vazio")
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, bgraToRGBA(f)); err != nil {
		return nil, fmt.Errorf("png encode: %w", err)
	}
	return buf.Bytes(), nil
}

// EncodeFrame é o atalho para o formato automático (WebP por padrão):
//
//  1. **WebP lossless** (padrão quando cgo/libwebp disponível): mesmo conteúdo do
//     PNG com 20-35% menos bytes em UI/texto — um único encode;
//  2. **PNG** lossless no formato explícito "png" (ou sem WebP);
//  3. se o lossless passar do teto: **WebP lossy** na qualidade pedida
//     (melhor relação qualidade/tamanho que JPEG) ou, por fim, JPEG.
//
// maxDim == 0 aplica o padrão (defaultMaxDimension); maxDim < 0 desativa o
// redimensionamento (usado quando o chamador já reduziu o frame).
// Formatos suportados para a imagem final. "auto" prefere o lossless mais
// econômico (WebP quando menor que o PNG); "png" força PNG (portabilidade
// máxima — útil quando algum provedor de visão recusa WebP).
const (
	FormatAuto = "auto"
	FormatPNG  = "png"
)

// NormalizeFormat reduz o valor recebido a um formato conhecido.
func NormalizeFormat(format string) string {
	if strings.EqualFold(strings.TrimSpace(format), FormatPNG) {
		return FormatPNG
	}
	return FormatAuto
}

// EncodeFrame é o atalho para o formato automático (WebP quando compensa).
func EncodeFrame(f *screen.Frame, quality, maxDim int) ([]byte, string, error) {
	return EncodeFrameFormat(f, quality, maxDim, FormatAuto)
}

// EncodeFrameFormat codifica conforme a preferência ("auto" ou "png").
//
// O modo "auto" é **WebP-first**: com WebP disponível, o lossless é codificado
// UMA vez e usado direto quando cabe no teto (sem calcular PNG — evita o custo
// dobrado de encode). O PNG só entra na conta no caso raro do lossless WebP
// passar do teto, onde ele ainda pode caber; a preferência explícita "png"
// nunca gera WebP (nem no fallback, que vai para JPEG).
func EncodeFrameFormat(f *screen.Frame, quality, maxDim int, format string) ([]byte, string, error) {
	useWebP := NormalizeFormat(format) == FormatAuto
	if maxDim == 0 {
		maxDim = defaultMaxDimension
	}
	if quality <= 0 {
		quality = defaultJPEGQuality
	}
	if maxDim > 0 {
		f = Downscale(f, maxDim)
	}

	// Caminho padrão: WebP lossless (mesmo conteúdo do PNG, ~1/3 menor).
	if useWebP && webpAvailable() {
		if webpData, webpErr := encodeWebPLossless(f); webpErr == nil && len(webpData) > 0 {
			if len(webpData) <= maxPNGBytes {
				return webpData, "image/webp", nil
			}
			// Raro: lossless passou do teto. PNG pode caber (menor em conteúdo
			// ruidoso) — compara; senão, WebP lossy.
			if pngData, pngErr := EncodePNG(f); pngErr == nil && len(pngData) <= maxPNGBytes && len(pngData) < len(webpData) {
				return pngData, "image/png", nil
			}
			if lossyData, lossyErr := encodeWebPLossy(f, quality); lossyErr == nil && len(lossyData) <= maxPNGBytes {
				return lossyData, "image/webp", nil
			}
		}
	}

	// PNG explícito (ou WebP indisponível/falhou): PNG; acima do teto, JPEG.
	pngData, pngErr := EncodePNG(f)
	if pngErr == nil && len(pngData) <= maxPNGBytes {
		return pngData, "image/png", nil
	}
	if useWebP && webpAvailable() {
		if lossyData, lossyErr := encodeWebPLossy(f, quality); lossyErr == nil && len(lossyData) <= maxPNGBytes {
			return lossyData, "image/webp", nil
		}
	}
	jpegData, jpegErr := screen.NewJPEGEncoder().Encode(f, quality)
	if jpegErr != nil {
		if pngErr != nil {
			return nil, "", fmt.Errorf("png: %v; jpeg: %w", pngErr, jpegErr)
		}
		return nil, "", jpegErr
	}
	return jpegData, "image/jpeg", nil
}

// encodeThumbnail gera a miniatura do chat no formato mais econômico
// (WebP lossy quando disponível; JPEG como fallback).
func encodeThumbnail(f *screen.Frame) ([]byte, string) {
	if webpAvailable() {
		if data, err := encodeWebPLossy(f, thumbnailQuality+10); err == nil && len(data) > 0 {
			return data, "image/webp"
		}
	}
	if data, err := screen.NewJPEGEncoder().Encode(f, thumbnailQuality); err == nil {
		return data, "image/jpeg"
	}
	return nil, ""
}
