package screenshot

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"unsafe"

	"discovery/app/core/screen"
)

const (
	defaultMaxDimension = 1600
	defaultJPEGQuality  = 80
	// maxPNGBytes acima do qual a imagem é reencodada em JPEG. PNG é melhor
	// para UI/texto, mas um print 4K pode passar de 10 MB — inviável para o
	// payload multimodal.
	maxPNGBytes = 1_200_000
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

// EncodeFrame codifica o frame para visão do LLM: tenta PNG (melhor para
// texto/UI) e cai para JPEG quando o PNG fica grande demais.
//
// maxDim == 0 aplica o padrão (defaultMaxDimension); maxDim < 0 desativa o
// redimensionamento (usado quando o chamador já reduziu o frame).
func EncodeFrame(f *screen.Frame, quality, maxDim int) ([]byte, string, error) {
	if maxDim == 0 {
		maxDim = defaultMaxDimension
	}
	if quality <= 0 {
		quality = defaultJPEGQuality
	}
	if maxDim > 0 {
		f = Downscale(f, maxDim)
	}
	pngData, err := EncodePNG(f)
	if err == nil && len(pngData) <= maxPNGBytes {
		return pngData, "image/png", nil
	}
	jpegData, jpegErr := screen.NewJPEGEncoder().Encode(f, quality)
	if jpegErr != nil {
		if err != nil {
			return nil, "", fmt.Errorf("png: %v; jpeg: %w", err, jpegErr)
		}
		return nil, "", jpegErr
	}
	return jpegData, "image/jpeg", nil
}
