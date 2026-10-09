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
	// defaultMaxDimension = 0 significa NÃO reduzir: as capturas são enviadas na
	// resolução ORIGINAL (nativa) do desktop. A redução deixou de ser padrão —
	// era ela que borrava texto em telas 1440p/4K/ultrawide. A IA ainda pode
	// pedir redução explícita via o parâmetro maxDimension da tool.
	defaultMaxDimension = 0
	// defaultJPEGQuality é usado quando o lossless passa de maxPNGBytes
	// (print fotográfico/4K ou desktop muito ruidoso). 95 mantém o texto
	// legível no fallback lossy (o 80 original borrava; 90 ainda deixava
	// ringing visível em fonte pequena).
	defaultJPEGQuality = 95
	// maxPNGBytes é o teto do binário final. Existe apenas para não estourar o
	// limite de payload do servidor (AiChatHelpers.MaxImageBase64Chars = 6 MiB
	// de base64, ou ~4,2 MB de binário; acima disso o servidor descarta a
	// imagem). PNG/WebP lossless é o caminho preferido; quando não cabe, a
	// resolução é PRESERVADA e só a qualidade do lossy cai (ver ladder abaixo).
	maxPNGBytes = 4_200_000
	// Miniatura exibida no chat (o que a IA viu) e no painel de auditoria.
	// 800px @ 72 dá prévia legível de texto em janela/diálogo sem pesar no
	// payload do evento (a miniatura NÃO vai ao LLM).
	thumbnailMaxDimension = 800
	thumbnailQuality      = 72
	// overlayWebPQuality é a qualidade lossy da tela congelada do overlay
	// (imagem intermediária de exibição).
	overlayWebPQuality = 95
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
	// ColorSpace PRECISA acompanhar o recorte: um frame HDR (scRGB float) que
	// perde a marca seria tratado como BGRA 8-bit e o tone mapping deixaria de
	// rodar — o print sairia corrompido (bytes float interpretados como cor).
	dst.ColorSpace = src.ColorSpace
	return dst, nil
}

// Downscale reduz o frame quando o maior lado excede maxDim com filtro de ÁREA
// (média de todos os pixels cobertos): sem ele, texto/linhas de 1px trepidavam
// nas capturas reduzidas (bilinear amostra 4 pixels e descarta o resto).
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
	return screen.ResizeBGRABox(f, scale)
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
//     (melhor relação qualidade/tamanho que JPEG; padrão 95) ou, por fim, JPEG.
//
// maxDim == 0 (padrão) preserva a resolução ORIGINAL do frame; maxDim < 0
// também não redimensiona (usado quando o chamador já reduziu o frame).
// Formatos suportados para a imagem final. "auto" prefere o lossless mais
// econômico (WebP quando menor que o PNG); "png" força PNG (portabilidade
// máxima — útil quando algum provedor de visão recusa WebP).
const (
	FormatAuto = "auto"
	FormatPNG  = "png"
	// FormatWebP força WebP lossless sempre que disponível (qualidade idêntica ao
	// PNG, ~33% menor — porém o encoder lossless do libwebp é lento em imagens
	// grandes: ~2 s em 3440×1440).
	FormatWebP = "webp"
)

// NOTA: o antigo orçamento de pixels (webpLosslessPixelBudget) foi removido.
// O automático usava WebP lossless em janelas/diálogos para economizar ~30%
// de bytes; como a aceitação de WebP pelo provedor de visão não é garantida,
// "auto" agora é PNG lossless e "webp" virou opt-in explícito.

// NormalizeFormat reduz o valor recebido a um formato conhecido.
func NormalizeFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case FormatPNG:
		return FormatPNG
	case FormatWebP:
		return FormatWebP
	default:
		return FormatAuto
	}
}

// EncodeFrame é o atalho para o formato automático (WebP quando compensa).
func EncodeFrame(f *screen.Frame, quality, maxDim int) ([]byte, string, error) {
	return EncodeFrameFormat(f, quality, maxDim, FormatAuto)
}

// EncodeOverlayFrame codifica a TELA CONGELADA exibida no overlay. É a base do
// recorte E da imagem anotada, então precisa ser LOSSLESS: usa PNG (rápido,
// ~0,2 s em 1440p e sem perdas). Se o PNG passar do teto de payload (desktops
// muito grandes/ruidosos), cai para WebP lossy de alta qualidade.
func EncodeOverlayFrame(f *screen.Frame, maxDim int) ([]byte, string, error) {
	if maxDim == 0 {
		maxDim = defaultMaxDimension
	}
	if maxDim > 0 {
		f = Downscale(f, maxDim)
	}
	pngData, err := EncodePNG(f)
	if err == nil && len(pngData) <= maxPNGBytes {
		return pngData, "image/png", nil
	}
	// Mantém a resolução da tela congelada e reduz a qualidade até caber no
	// teto do payload do overlay (antes o WebP lossy era devolvido sem checar
	// tamanho, e o PNG estourado voltava como estava).
	if data, mime, ok := encodeLossyWithinBudget(f, overlayWebPQuality, true); ok {
		return data, mime, nil
	}
	if data, mime, ok := encodeShrunkWithinBudget(f, overlayWebPQuality, true); ok {
		return data, mime, nil
	}
	if err != nil {
		return nil, "", err
	}
	// Devolver um PNG acima do teto só adiava o problema: ele vira a base do
	// canvas anotado, que o decoder recusa (>16 MiB de data URL) com erro.
	return nil, "", fmt.Errorf("tela congelada acima do teto de payload (%d bytes)", maxPNGBytes)
}

// EncodeFrameFormat codifica conforme a preferência ("auto", "webp" ou "png").
//
// Qualidade é lossless no caminho normal: o que muda é o CODEC e, em último
// caso (imagem acima do teto de payload), a qualidade do lossy.
//
//   - "auto" (padrão): **PNG lossless**, aceito por qualquer provedor de visão.
//     O automático não usa WebP por precaução (aceitação de WebP não validada
//     no provedor em uso). Fallback lossy: JPEG.
//   - "png": idêntico ao auto (PNG sempre; fallback JPEG);
//   - "webp": WebP lossless quando disponível (arquivo ~30% menor) — opt-in para
//     quem tem certeza de que o provedor aceita; fallback WebP lossy/JPEG.
func EncodeFrameFormat(f *screen.Frame, quality, maxDim int, format string) ([]byte, string, error) {
	normalized := NormalizeFormat(format)
	if maxDim == 0 {
		maxDim = defaultMaxDimension
	}
	if quality <= 0 {
		quality = defaultJPEGQuality
	}
	if maxDim > 0 {
		f = Downscale(f, maxDim)
	}

	// WebP SÓ por escolha explícita. O modo automático passou a usar PNG
	// lossless por PRECAUÇÃO de compatibilidade: a aceitação de WebP pelo
	// provedor de visão não pôde ser comprovada (todas as amostras WebP do
	// incidente estavam corrompidas pelo truncamento do tool result — ver
	// chat_multi_round.go). PNG é aceito universalmente; "webp" fica
	// disponível para quem validou com o próprio provedor (~30% menor).
	useWebP := normalized == FormatWebP
	// O fallback lossy acompanha o formato pedido: WebP lossy só no "webp";
	// "auto" e "png" caem para JPEG, aceito universalmente.
	allowWebP := normalized == FormatWebP

	// Caminho lossless WebP: mesmo conteúdo do PNG, ~1/3 menor.
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

	// PNG explícito (ou WebP indisponível/falhou): PNG; acima do teto, lossy.
	pngData, pngErr := EncodePNG(f)
	if pngErr == nil && len(pngData) <= maxPNGBytes {
		return pngData, "image/png", nil
	}
	// Acima do teto: mantém a RESOLUÇÃO ORIGINAL e desce apenas a qualidade
	// (WebP lossy é melhor em qualidade/tamanho; o formato "png" explícito
	// nunca usa WebP — compatibilidade é o motivo da opção — então vai só JPEG).
	if data, mime, ok := encodeLossyWithinBudget(f, quality, allowWebP); ok {
		return data, mime, nil
	}
	// Último recurso (desktop gigante e ruidoso): reduz em degraus, porque uma
	// imagem acima do teto seria descartada em silêncio pelo servidor.
	if data, mime, ok := encodeShrunkWithinBudget(f, quality, allowWebP); ok {
		return data, mime, nil
	}
	jpegData, jpegErr := screen.NewJPEGEncoder().Encode(f, quality)
	if jpegErr == nil && len(jpegData) <= maxPNGBytes {
		return jpegData, "image/jpeg", nil
	}
	if jpegErr != nil {
		if pngErr != nil {
			return nil, "", fmt.Errorf("png: %v; jpeg: %w", pngErr, jpegErr)
		}
		return nil, "", jpegErr
	}
	// Entrega uma imagem acima do teto = o servidor descarta em silêncio (e o
	// anexo manual rejeita >6 MiB). Erro explícito é melhor que captura muda.
	return nil, "", fmt.Errorf("imagem acima do teto de payload (%d bytes) mesmo apos reduzir", maxPNGBytes)
}

// lossyQualityLadder devolve a qualidade pedida seguida de degraus menores.
func lossyQualityLadder(quality int) []int {
	if quality <= 0 {
		quality = defaultJPEGQuality
	}
	if quality > 100 {
		quality = 100
	}
	// Degraus CURTOS de propósito: cada nível custa encode full-frame (WebP e
	// JPEG) e capture_screenshot é interativo (sem timeout) — uma cadeia longa
	// deixaria o chat pendurado em telas 5K/6K.
	ladder := []int{quality}
	for _, q := range []int{85, 70, 50} {
		if q < quality {
			ladder = append(ladder, q)
		}
	}
	return ladder
}

// encodeLossyWithinBudget tenta lossy na qualidade pedida e em degraus menores,
// SEM mudar a resolução. allowWebP=false restringe a tentativa a JPEG (formato
// "png" explícito, que existe justamente para máxima compatibilidade). Devolve o
// primeiro resultado que couber em maxPNGBytes.
func encodeLossyWithinBudget(f *screen.Frame, quality int, allowWebP bool) ([]byte, string, bool) {
	for _, q := range lossyQualityLadder(quality) {
		if data, mime, ok := encodeLossySingle(f, q, allowWebP); ok {
			return data, mime, true
		}
	}
	return nil, "", false
}

// encodeLossySingle tenta UM nível de qualidade (WebP e depois JPEG), sem
// redimensionar. É a unidade usada pelo ladder e pelo caminho de redução.
func encodeLossySingle(f *screen.Frame, quality int, allowWebP bool) ([]byte, string, bool) {
	if allowWebP && webpAvailable() {
		if data, err := encodeWebPLossy(f, quality); err == nil && len(data) > 0 && len(data) <= maxPNGBytes {
			return data, "image/webp", true
		}
	}
	if data, err := screen.NewJPEGEncoder().Encode(f, quality); err == nil && len(data) > 0 && len(data) <= maxPNGBytes {
		return data, "image/jpeg", true
	}
	return nil, "", false
}

// encodeShrunkWithinBudget é a rede de segurança: se nem a qualidade mínima
// couber, reduz a resolução em degraus pequenos até o teto ser respeitado.
func encodeShrunkWithinBudget(f *screen.Frame, quality int, allowWebP bool) ([]byte, string, bool) {
	if f == nil || f.Width <= 0 || f.Height <= 0 {
		return nil, "", false
	}
	longest := f.Width
	if f.Height > longest {
		longest = f.Height
	}
	for _, factor := range []float64{0.85, 0.7, 0.5} {
		target := int(float64(longest) * factor)
		if target < 1 {
			break
		}
		shrunk := Downscale(f, target)
		if shrunk == f {
			break
		}
		// Um nível (e no máximo um degrau extra) por escala: aqui o objetivo é
		// caber no teto, não maximizar qualidade — o custo já é alto.
		if data, mime, ok := encodeLossySingle(shrunk, quality, allowWebP); ok {
			return data, mime, true
		}
		if quality > 60 {
			if data, mime, ok := encodeLossySingle(shrunk, 60, allowWebP); ok {
				return data, mime, true
			}
		}
	}
	return nil, "", false
}

// AttachThumbnailFrame gera a miniatura do chat/auditoria a partir de um frame
// capturado e a anexa ao resultado. É usada pelos caminhos que montam o
// CaptureResult à mão (overlay do usuário): sem isso a auditoria de privacidade
// ficava sem a prévia de toda captura manual, e o chat ficava sem a miniatura.
func AttachThumbnailFrame(res *CaptureResult, f *screen.Frame) {
	if res == nil || f == nil || f.Width <= 0 || f.Height <= 0 {
		return
	}
	thumb := Downscale(f, thumbnailMaxDimension)
	if thumb == nil {
		return
	}
	if data, mime := encodeThumbnail(thumb); len(data) > 0 {
		res.Thumbnail = data
		res.ThumbnailMIME = mime
	}
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
