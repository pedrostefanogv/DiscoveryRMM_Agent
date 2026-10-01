package screenshot

import (
	"bytes"
	"image"
	"testing"

	"discovery/app/core/screen"
)

func solidFrame(w, h int, b, g, r, a byte) *screen.Frame {
	f := &screen.Frame{Data: make([]byte, w*h*4), Width: w, Height: h, Stride: w * 4}
	for i := 0; i < w*h; i++ {
		f.Data[i*4] = b
		f.Data[i*4+1] = g
		f.Data[i*4+2] = r
		f.Data[i*4+3] = a
	}
	return f
}

func TestEncodeFrameAutoDefaultsToWebP(t *testing.T) {
	if !webpAvailable() {
		t.Skip("webp indisponivel (cgo desabilitado)")
	}
	// Conteúdo de UI (áreas chapadas) comprime melhor em WebP lossless que PNG.
	f := solidFrame(640, 480, 28, 30, 36, 255)
	data, mime, err := EncodeFrame(f, 90, 0)
	if err != nil {
		t.Fatalf("EncodeFrame falhou: %v", err)
	}
	if mime != "image/webp" {
		t.Fatalf("modo auto deveria usar webp, got %q", mime)
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		t.Fatalf("webp nao decodifica: %v", err)
	}
}

func TestEncodeFrameFormatForcesPNG(t *testing.T) {
	f := noiseFrame(400, 300)
	data, mime, err := EncodeFrameFormat(f, 90, 0, FormatPNG)
	if err != nil {
		t.Fatalf("EncodeFrameFormat(png) falhou: %v", err)
	}
	if mime != "image/png" {
		t.Fatalf("mime = %q, want image/png", mime)
	}
	if !bytes.HasPrefix(data, []byte{0x89, 0x50, 0x4E, 0x47}) {
		t.Fatal("assinatura PNG ausente")
	}
}

func TestNormalizeFormat(t *testing.T) {
	cases := map[string]string{
		"":      FormatAuto,
		"auto":  FormatAuto,
		"PNG":   FormatPNG,
		" png ": FormatPNG,
		"webp":  FormatAuto,
		"jpg":   FormatAuto,
	}
	for in, want := range cases {
		if got := NormalizeFormat(in); got != want {
			t.Errorf("NormalizeFormat(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCropFrameKeepsPixelsAndOrigin(t *testing.T) {
	src := solidFrame(10, 8, 10, 20, 30, 255)
	src.OriginX = -100
	src.OriginY = 50
	dst, err := CropFrame(src, 2, 3, 4, 2)
	if err != nil {
		t.Fatalf("CropFrame falhou: %v", err)
	}
	if dst.Width != 4 || dst.Height != 2 {
		t.Fatalf("dimensoes = %dx%d, want 4x2", dst.Width, dst.Height)
	}
	if dst.OriginX != -98 || dst.OriginY != 53 {
		t.Fatalf("origem = (%d,%d), want (-98,53)", dst.OriginX, dst.OriginY)
	}
	if len(dst.Data) != 4*2*4 {
		t.Fatalf("len(data) = %d", len(dst.Data))
	}
	if dst.Data[0] != 10 || dst.Data[1] != 20 || dst.Data[2] != 30 {
		t.Fatalf("pixel copiado = %v", dst.Data[:4])
	}
}

func TestCropFrameClampsOutOfBounds(t *testing.T) {
	src := solidFrame(10, 10, 1, 2, 3, 255)
	dst, err := CropFrame(src, -5, -5, 8, 8)
	if err != nil {
		t.Fatalf("CropFrame falhou: %v", err)
	}
	if dst.Width != 3 || dst.Height != 3 {
		t.Fatalf("dimensoes = %dx%d, want 3x3", dst.Width, dst.Height)
	}
	if _, err := CropFrame(src, 20, 20, 5, 5); err == nil {
		t.Fatal("esperava erro para regiao fora do frame")
	}
}

func TestDownscaleLimitsLongestSide(t *testing.T) {
	src := solidFrame(4000, 2000, 0, 0, 0, 255)
	out := Downscale(src, 1600)
	if out.Width != 1600 {
		t.Fatalf("width = %d, want 1600", out.Width)
	}
	if out.Height != 800 {
		t.Fatalf("height = %d, want 800", out.Height)
	}
	// Abaixo do limite não redimensiona.
	if got := Downscale(src, 5000); got != src {
		t.Fatal("nao deveria redimensionar abaixo do limite")
	}
}

func TestEncodeFrameProducesPNG(t *testing.T) {
	src := solidFrame(64, 32, 10, 20, 30, 255)
	// Formato forçado: o modo "auto" pode escolher WebP lossless (menor).
	data, mime, err := EncodeFrameFormat(src, 80, 0, FormatPNG)
	if err != nil {
		t.Fatalf("EncodeFrame falhou: %v", err)
	}
	if mime != "image/png" {
		t.Fatalf("mime = %q, want image/png", mime)
	}
	if !bytes.HasPrefix(data, []byte{0x89, 0x50, 0x4E, 0x47}) {
		t.Fatalf("assinatura PNG ausente: %v", data[:4])
	}
}

func noiseFrame(w, h int) *screen.Frame {
	f := &screen.Frame{Data: make([]byte, w*h*4), Width: w, Height: h, Stride: w * 4}
	seed := uint32(12345)
	for i := 0; i < w*h; i++ {
		seed = seed*1664525 + 1013904223
		f.Data[i*4] = byte(seed >> 24)
		f.Data[i*4+1] = byte(seed>>16) ^ 0x5a
		f.Data[i*4+2] = byte(seed >> 8)
		f.Data[i*4+3] = 255
	}
	return f
}

func TestEncodeFramePrefersEfficientLossless(t *testing.T) {
	f := noiseFrame(1200, 800)
	pngData, pngErr := EncodePNG(f)
	if pngErr != nil {
		t.Fatalf("EncodePNG: %v", pngErr)
	}

	data, mime, err := EncodeFrame(f, 90, 0)
	if err != nil {
		t.Fatalf("EncodeFrame falhou: %v", err)
	}
	if mime != "image/webp" && mime != "image/png" {
		t.Fatalf("mime inesperado: %q", mime)
	}
	if len(data) > len(pngData) {
		t.Fatalf("formato escolhido (%s) ficou MAIOR que o PNG: %d > %d", mime, len(data), len(pngData))
	}
	// O formato escolhido precisa ser decodificável (valida o registro do WebP).
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		t.Fatalf("imagem codificada nao decodifica (%s): %v", mime, err)
	}
}

func TestScaledDimensionsMatchesDownscale(t *testing.T) {
	cases := []struct{ w, h, max int }{
		{4000, 2000, 1600},
		{3440, 1440, 2560},
		{1000, 800, 1600},
		{5000, 900, 1000},
	}
	for _, c := range cases {
		wantW, wantH := ScaledDimensions(c.w, c.h, c.max)
		out := Downscale(solidFrame(c.w, c.h, 0, 0, 0, 255), c.max)
		if out.Width != wantW || out.Height != wantH {
			t.Errorf("ScaledDimensions(%d,%d,%d)=(%d,%d), Downscale=(%d,%d)",
				c.w, c.h, c.max, wantW, wantH, out.Width, out.Height)
		}
	}
}

func TestCaptureResultDataURL(t *testing.T) {
	r := &CaptureResult{MIME: "image/png", Data: []byte{1, 2, 3}}
	if got := r.DataURL(); got != "data:image/png;base64,AQID" {
		t.Fatalf("DataURL = %q", got)
	}
	if got := r.Base64(); got != "AQID" {
		t.Fatalf("Base64 = %q", got)
	}
	payload := r.ToolPayload("resumo", map[string]any{"windowHandle": 42})
	if payload["image_base64"] != "AQID" || payload["mime"] != "image/png" {
		t.Fatalf("payload inesperado: %v", payload)
	}
}
