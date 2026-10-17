package screenshot

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testImage(width, height int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: uint8(x + y), A: 255})
		}
	}
	return img
}

func TestParseSaveImageOptionsDefaults(t *testing.T) {
	opts, err := ParseSaveImageOptions("")
	if err != nil {
		t.Fatalf("string vazia deveria ser valida: %v", err)
	}
	if opts.Format != "" || opts.Directory != "" {
		t.Fatalf("defaults inesperados: %+v", opts)
	}
	if opts.Prefix != DefaultScreenshotPrefix {
		t.Fatalf("prefixo default = %q", opts.Prefix)
	}
}

func TestParseSaveImageOptionsNormalizes(t *testing.T) {
	dir := t.TempDir()
	raw := `{"format":"JPEG","directory":"` + strings.ReplaceAll(dir, "\\", "\\\\") + `","prefix":"../Relatório final"}`
	opts, err := ParseSaveImageOptions(raw)
	if err != nil {
		t.Fatalf("opcoes deveriam ser aceitas: %v", err)
	}
	if opts.Format != "jpg" {
		t.Fatalf("format = %q, want jpg", opts.Format)
	}
	if opts.Directory != filepath.Clean(dir) {
		t.Fatalf("directory = %q, want %q", opts.Directory, dir)
	}
	if strings.ContainsAny(opts.Prefix, "/\\.") || opts.Prefix == "" {
		t.Fatalf("prefixo nao foi sanitizado: %q", opts.Prefix)
	}
}

func TestParseSaveImageOptionsRejectsDangerousValues(t *testing.T) {
	cases := []string{
		`{invalid`,
		`{"format":"bmp"}`,
		`{"directory":"relativo\\capturas"}`,
		`{"directory":"..\\fora"}`,
	}
	for _, raw := range cases {
		if _, err := ParseSaveImageOptions(raw); err == nil {
			t.Errorf("opcoes deveriam ser recusadas: %s", raw)
		}
	}
}

func TestSanitizeFilePrefixNeverKeepsSeparators(t *testing.T) {
	cases := map[string]string{
		"":                      DefaultScreenshotPrefix,
		"  ":                    DefaultScreenshotPrefix,
		"../etc/passwd":         "etc-passwd",
		"Captura de Tela 01":    "captura-de-tela-01",
		"///":                   DefaultScreenshotPrefix,
		strings.Repeat("a", 80): strings.Repeat("a", MaxScreenshotPrefixRunes),
	}
	for in, want := range cases {
		if got := SanitizeFilePrefix(in); got != want {
			t.Errorf("SanitizeFilePrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildScreenshotFileName(t *testing.T) {
	at := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	if got := BuildScreenshotFileName("captura", at, "a1b2", "png"); got != "captura-20240102-030405-a1b2.png" {
		t.Fatalf("nome png = %q", got)
	}
	if got := BuildScreenshotFileName("Evidência", at, "Z9Y8", "jpg"); got != "evid-ncia-20240102-030405-z9y8.jpg" {
		t.Fatalf("nome jpg = %q", got)
	}
	if got := BuildScreenshotFileName("", at, "", "png"); got != "captura-20240102-030405-0000.png" {
		t.Fatalf("nome default = %q", got)
	}
	if ext := filepath.Ext(BuildScreenshotFileName("x", at, "aaaa", "jpeg")); ext != ".jpg" {
		t.Fatalf("extensao = %q", ext)
	}
}

func TestImageFormatFromDataURL(t *testing.T) {
	cases := map[string]string{
		"data:image/png;base64,AAAA": "png",
		"data:image/jpeg;base64,AA":  "jpg",
		"data:image/jpg;base64,AA":   "jpg",
		"data:image/webp;base64,AA":  "",
		"http://exemplo/x.png":       "",
	}
	for in, want := range cases {
		if got := ImageFormatFromDataURL(in); got != want {
			t.Errorf("ImageFormatFromDataURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSHA256AndEvidence(t *testing.T) {
	if got := SHA256Hex([]byte("abc")); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("sha256 = %q", got)
	}
	raw := []byte("binario-original")
	sum := sha256.Sum256(raw)
	at := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	evidence := BuildEvidence(raw, testImage(12, 7), at)
	if evidence.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("sha da evidencia = %q", evidence.SHA256)
	}
	if evidence.Bytes != len(raw) || evidence.Width != 12 || evidence.Height != 7 {
		t.Fatalf("evidencia = %+v", evidence)
	}
	if evidence.CapturedAtUTC != "2024-05-06T07:08:09Z" {
		t.Fatalf("capturedAtUtc = %q", evidence.CapturedAtUTC)
	}
}

func TestPathWithinDir(t *testing.T) {
	base := t.TempDir()
	inside := filepath.Join(base, "captura.png")
	if !PathWithinDir(base, inside) {
		t.Fatal("arquivo dentro da pasta deveria passar")
	}
	if PathWithinDir(base, base) {
		t.Fatal("a propria pasta nao e um arquivo da galeria")
	}
	if PathWithinDir(base, filepath.Join(base, "..", "fora.png")) {
		t.Fatal("caminho com .. deveria ser recusado")
	}
	if PathWithinDir(base, base+"2") {
		t.Fatal("pasta irma com prefixo parecido deveria ser recusada")
	}
	if PathWithinDir(base, filepath.Dir(base)) {
		t.Fatal("pasta pai deveria ser recusada")
	}
}

func TestResolveGalleryPath(t *testing.T) {
	gallery := t.TempDir()
	inside, err := ResolveGalleryPath(gallery, filepath.Join(gallery, "ok.png"))
	if err != nil || filepath.Base(inside) != "ok.png" {
		t.Fatalf("caminho interno: %q err=%v", inside, err)
	}
	relative, err := ResolveGalleryPath(gallery, "ok.png")
	if err != nil || relative != inside {
		t.Fatalf("basename deveria resolver para a galeria: %q err=%v", relative, err)
	}
	if _, err := ResolveGalleryPath(gallery, filepath.Join(gallery, "..", "fora.png")); err == nil {
		t.Fatal(".. deveria ser recusado")
	}
	if _, err := ResolveGalleryPath(gallery, filepath.Join(filepath.Dir(gallery), "outra", "x.png")); err == nil {
		t.Fatal("caminho absoluto fora da galeria deveria ser recusado")
	}
	if _, err := ResolveGalleryPath(gallery, "sub/arquivo.png"); err == nil {
		t.Fatal("caminho relativo com subpasta deveria ser recusado")
	}
	if _, err := ResolveGalleryPath(gallery, ""); err == nil {
		t.Fatal("caminho vazio deveria ser recusado")
	}
}

func TestEncodeImageRespectsFormat(t *testing.T) {
	img := testImage(16, 9)
	pngData, err := EncodeImage(img, "png", 0)
	if err != nil {
		t.Fatalf("png: %v", err)
	}
	if !bytes.HasPrefix(pngData, []byte{0x89, 0x50, 0x4E, 0x47}) {
		t.Fatal("bytes de PNG com assinatura errada")
	}
	jpgData, err := EncodeImage(img, "jpg", 0)
	if err != nil {
		t.Fatalf("jpg: %v", err)
	}
	if !bytes.HasPrefix(jpgData, []byte{0xFF, 0xD8, 0xFF}) {
		t.Fatal("bytes de JPEG com assinatura errada")
	}
	if _, err := EncodeImage(nil, "png", 0); err == nil {
		t.Fatal("imagem nil deveria falhar")
	}
	if format, ok := NormalizeImageFormat("gif"); ok || format != "" {
		t.Fatal("formato gif deveria ser recusado")
	}
}

func TestDecodeDataURLImageBytesKeepsOriginal(t *testing.T) {
	img := testImage(5, 4)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
	raw, decoded, err := DecodeDataURLImageBytes(dataURL)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(raw, buf.Bytes()) {
		t.Fatal("os bytes originais foram alterados")
	}
	if decoded.Bounds().Dx() != 5 || decoded.Bounds().Dy() != 4 {
		t.Fatalf("dimensoes = %v", decoded.Bounds())
	}
	if _, _, err := DecodeDataURLImageBytes("data:text/plain;base64,AAAA"); err == nil {
		t.Fatal("data URL que nao e imagem deveria falhar")
	}
}

func TestRandomSuffix(t *testing.T) {
	first := RandomSuffix(4)
	if len(first) != 4 {
		t.Fatalf("sufixo = %q", first)
	}
	for _, r := range first {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			t.Fatalf("caractere invalido no sufixo: %q", first)
		}
	}
	if RandomSuffix(0) != "" {
		t.Fatal("tamanho zero deveria devolver string vazia")
	}
}

func TestGalleryFolderNameIsStable(t *testing.T) {
	if GalleryFolderName != "Discovery Capturas" {
		t.Fatalf("nome da pasta = %q", GalleryFolderName)
	}
	if _, err := os.Stat("."); err != nil {
		t.Fatal(err)
	}
}
