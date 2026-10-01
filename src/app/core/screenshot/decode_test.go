package screenshot

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestNormalizeEncodedImageDownscalesAndKeepsPNG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4000, 2000))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}

	data, mime, w, h, err := NormalizeEncodedImage(buf.Bytes(), 2560, 92, 0)
	if err != nil {
		t.Fatalf("NormalizeEncodedImage falhou: %v", err)
	}
	if w != 2560 || h != 1280 {
		t.Fatalf("dimensoes = %dx%d, want 2560x1280", w, h)
	}
	// O formato é escolhido pelo modo "auto" (WebP lossless quando menor).
	if mime != "image/png" && mime != "image/webp" {
		t.Fatalf("mime = %q, want image/png ou image/webp", mime)
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		t.Fatalf("imagem normalizada nao decodifica (%s): %v", mime, err)
	}

	if _, _, _, _, err := NormalizeEncodedImage(nil, 0, 0, 0); err == nil {
		t.Fatal("bytes vazios deveriam falhar")
	}
}

func TestDecodeDataURLToImageRoundTrip(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.Set(1, 1, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())

	decoded, err := DecodeDataURLToImage(dataURL)
	if err != nil {
		t.Fatalf("DecodeDataURLToImage falhou: %v", err)
	}
	if got := decoded.Bounds().Dx(); got != 3 {
		t.Fatalf("largura = %d, want 3", got)
	}
	if got := decoded.Bounds().Dy(); got != 2 {
		t.Fatalf("altura = %d, want 2", got)
	}

	if _, err := DecodeDataURLToImage("https://example.com/x.png"); err == nil {
		t.Fatal("URL externa deveria ser rejeitada")
	}
	if _, err := DecodeDataURLToImage("data:text/plain;base64,YQ=="); err == nil {
		t.Fatal("data URL nao-imagem deveria ser rejeitada")
	}
	if _, err := DecodeDataURLToImage("data:image/png;base64,@@invalido@@"); err == nil {
		t.Fatal("base64 invalido deveria ser rejeitado")
	}
}
