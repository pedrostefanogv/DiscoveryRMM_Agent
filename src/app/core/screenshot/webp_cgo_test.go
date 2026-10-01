//go:build cgo

package screenshot

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"testing"

	"github.com/chai2010/webp"
)

func TestWebPDataURLRoundTrip(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 32))
	img.Set(5, 5, color.RGBA{R: 200, G: 100, B: 50, A: 255})
	var buf bytes.Buffer
	if err := webp.Encode(&buf, img, &webp.Options{Lossless: true}); err != nil {
		t.Fatalf("webp.Encode: %v", err)
	}

	dataURL := "data:image/webp;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
	decoded, err := DecodeDataURLToImage(dataURL)
	if err != nil {
		t.Fatalf("DecodeDataURLToImage(webp) falhou: %v", err)
	}
	if decoded.Bounds().Dx() != 64 || decoded.Bounds().Dy() != 32 {
		t.Fatalf("dimensoes = %dx%d", decoded.Bounds().Dx(), decoded.Bounds().Dy())
	}

	// NormalizeEncodedImage também precisa aceitar WebP como entrada.
	data, mime, w, h, err := NormalizeEncodedImage(buf.Bytes(), 0, 90, 0)
	if err != nil {
		t.Fatalf("NormalizeEncodedImage(webp) falhou: %v", err)
	}
	if w != 64 || h != 32 {
		t.Fatalf("normalizado = %dx%d", w, h)
	}
	if mime != "image/webp" && mime != "image/png" {
		t.Fatalf("mime normalizado inesperado: %q", mime)
	}
	if len(data) == 0 {
		t.Fatal("bytes normalizados vazios")
	}
}
