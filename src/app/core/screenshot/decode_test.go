package screenshot

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"strings"
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

// buildPNGHeaderOnly monta o cabeçalho de um PNG (assinatura + IHDR) com as
// dimensões pedidas e sem IDAT: suficiente para image.DecodeConfig ler o
// tamanho sem que a decodificação precise alocar a imagem inteira.
func buildPNGHeaderOnly(width, height uint32) []byte {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], width)
	binary.BigEndian.PutUint32(ihdr[4:8], height)
	ihdr[8] = 8 // bit depth
	ihdr[9] = 6 // RGBA
	// compressão/filtro/interlace = 0

	chunk := func(kind string, data []byte) []byte {
		out := make([]byte, 0, 12+len(data))
		length := make([]byte, 4)
		binary.BigEndian.PutUint32(length, uint32(len(data)))
		out = append(out, length...)
		out = append(out, kind...)
		out = append(out, data...)
		crc := crc32.NewIEEE()
		_, _ = crc.Write([]byte(kind))
		_, _ = crc.Write(data)
		sum := make([]byte, 4)
		binary.BigEndian.PutUint32(sum, crc.Sum32())
		return append(out, sum...)
	}

	var png []byte
	png = append(png, 0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A)
	png = append(png, chunk("IHDR", ihdr)...)
	return append(png, chunk("IEND", nil)...)
}

func TestDecodeDataURLImageBytesRejectsHugeDimensions(t *testing.T) {
	// 100k x 100k pixels = 10 bilhões de pixels: o cabeçalho é barato, a
	// decodificação seria gigabytes (decompression bomb).
	bomb := buildPNGHeaderOnly(100_000, 100_000)
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(bomb)

	_, _, err := DecodeDataURLImageBytes(dataURL)

	if err == nil {
		t.Fatal("imagem com dimensoes absurdas deveria ser recusada antes de decodificar")
	}
	if !strings.Contains(err.Error(), "pixels") {
		t.Fatalf("erro = %q, want mensagem sobre pixels", err.Error())
	}
}

func TestDecodeDataURLImageBytesAcceptsNormalImage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 32, 24))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())

	raw, decoded, err := DecodeDataURLImageBytes(dataURL)
	if err != nil {
		t.Fatalf("imagem normal recusada: %v", err)
	}
	if !bytes.Equal(raw, buf.Bytes()) {
		t.Fatal("o binario original precisa ser devolvido intacto (hash de evidencia)")
	}
	if decoded == nil || decoded.Bounds().Dx() != 32 || decoded.Bounds().Dy() != 24 {
		t.Fatalf("dimensoes decodificadas = %v", decoded.Bounds())
	}
}
