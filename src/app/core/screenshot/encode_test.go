package screenshot

import (
	"bytes"
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
	data, mime, err := EncodeFrame(src, 80, 0)
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
