package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"discovery/app/core/screenshot"
)

func testGalleryPNG(t *testing.T, width, height int) (string, []byte) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 0x7F, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png: %v", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), buf.Bytes()
}

func withGalleryDir(t *testing.T, dir string) {
	t.Helper()
	previous := screenshotGalleryDirOverride
	screenshotGalleryDirOverride = dir
	t.Cleanup(func() { screenshotGalleryDirOverride = previous })
}

func TestScreenshotGalleryBindingsRoundTrip(t *testing.T) {
	app := newScreenshotTestApp(t)
	gallery := t.TempDir()
	withGalleryDir(t, gallery)

	dataURL, original := testGalleryPNG(t, 20, 12)
	body, err := app.SaveScreenshotImage(dataURL, `{"format":"png","prefix":"teste"}`)
	if err != nil {
		t.Fatalf("SaveScreenshotImage: %v", err)
	}
	var saved screenshot.SaveImageResult
	if err := json.Unmarshal([]byte(body), &saved); err != nil {
		t.Fatalf("json de retorno: %v", err)
	}
	if filepath.Clean(saved.Directory) != filepath.Clean(gallery) {
		t.Fatalf("diretorio = %q, want %q", saved.Directory, gallery)
	}
	if !strings.HasPrefix(saved.FileName, "teste-") || filepath.Ext(saved.FileName) != ".png" {
		t.Fatalf("nome salvo = %q", saved.FileName)
	}
	if saved.Width != 20 || saved.Height != 12 || saved.Format != "png" {
		t.Fatalf("metadados = %+v", saved)
	}
	written, err := os.ReadFile(saved.Path)
	if err != nil {
		t.Fatalf("arquivo salvo nao existe: %v", err)
	}
	if saved.Bytes != len(written) || saved.SHA256 != screenshot.SHA256Hex(written) {
		t.Fatalf("bytes/hash nao correspondem ao arquivo")
	}

	readURL, err := app.ReadScreenshotImageFile(saved.Path, 0)
	if err != nil {
		t.Fatalf("ReadScreenshotImageFile: %v", err)
	}
	if !strings.HasPrefix(readURL, "data:image/png;base64,") {
		t.Fatalf("data URL inesperada: %.40s", readURL)
	}
	readBytes, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(readURL, "data:image/png;base64,"))
	if err != nil || !bytes.Equal(readBytes, written) {
		t.Fatalf("conteudo lido difere do arquivo: err=%v", err)
	}

	if _, err := app.ReadScreenshotImageFile(saved.Path, 10); err == nil {
		t.Fatal("leitura acima de maxBytes deveria falhar")
	}
	if _, err := app.ReadScreenshotImageFile(filepath.Join(filepath.Dir(gallery), "fora.png"), 0); err == nil {
		t.Fatal("leitura fora da galeria deveria falhar")
	}

	evidenceJSON, err := app.GetScreenshotEvidence(dataURL)
	if err != nil {
		t.Fatalf("GetScreenshotEvidence: %v", err)
	}
	var evidence screenshot.Evidence
	if err := json.Unmarshal([]byte(evidenceJSON), &evidence); err != nil {
		t.Fatalf("json da evidencia: %v", err)
	}
	sum := sha256.Sum256(original)
	if evidence.SHA256 != hex.EncodeToString(sum[:]) || evidence.Bytes != len(original) {
		t.Fatalf("evidencia nao corresponde ao binario original: %+v", evidence)
	}
	if evidence.Width != 20 || evidence.Height != 12 || evidence.CapturedAtUTC == "" {
		t.Fatalf("evidencia = %+v", evidence)
	}

	folder, err := app.GetScreenshotGalleryFolder()
	if err != nil || filepath.Clean(folder) != filepath.Clean(gallery) {
		t.Fatalf("pasta da galeria = %q err=%v", folder, err)
	}

	if err := app.DeleteScreenshotImageFile(filepath.Join(filepath.Dir(gallery), "fora.png")); err == nil {
		t.Fatal("exclusao fora da galeria deveria falhar")
	}
	if err := app.DeleteScreenshotImageFile(saved.Path); err != nil {
		t.Fatalf("DeleteScreenshotImageFile: %v", err)
	}
	if _, err := os.Stat(saved.Path); !os.IsNotExist(err) {
		t.Fatalf("arquivo deveria ter sido removido: %v", err)
	}
	if err := app.DeleteScreenshotImageFile(saved.Path); err != nil {
		t.Fatalf("exclusao deveria ser idempotente: %v", err)
	}
}

func TestSaveScreenshotImageHonorsDirectoryAndJPG(t *testing.T) {
	app := newScreenshotTestApp(t)
	withGalleryDir(t, t.TempDir())
	customDir := t.TempDir()
	opts, _ := json.Marshal(map[string]string{
		"format":    "jpg",
		"directory": customDir,
		"prefix":    "export",
	})
	dataURL, _ := testGalleryPNG(t, 8, 8)
	body, err := app.SaveScreenshotImage(dataURL, string(opts))
	if err != nil {
		t.Fatalf("SaveScreenshotImage: %v", err)
	}
	var saved screenshot.SaveImageResult
	if err := json.Unmarshal([]byte(body), &saved); err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(saved.Directory) != filepath.Clean(customDir) {
		t.Fatalf("diretorio custom = %q", saved.Directory)
	}
	if saved.Format != "jpg" || filepath.Ext(saved.FileName) != ".jpg" {
		t.Fatalf("formato jpg = %+v", saved)
	}
	data, err := os.ReadFile(saved.Path)
	if err != nil || !bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}) {
		t.Fatalf("conteudo nao e JPEG: %v", err)
	}
}

func TestSaveScreenshotImageRejectsRelativeDirectory(t *testing.T) {
	app := newScreenshotTestApp(t)
	withGalleryDir(t, t.TempDir())
	dataURL, _ := testGalleryPNG(t, 4, 4)
	if _, err := app.SaveScreenshotImage(dataURL, `{"directory":"relativo\\x"}`); err == nil {
		t.Fatal("diretorio relativo deveria ser recusado")
	}
}

func TestSaveScreenshotPolicyPreservesGalleryFields(t *testing.T) {
	app := newScreenshotTestApp(t)
	if err := app.SaveScreenshotPolicy(`{"copyOnConfirm":true,"galleryEnabled":false,"galleryMaxItems":10}`); err != nil {
		t.Fatalf("salvar politica inicial: %v", err)
	}
	// Payload típico do formulário de privacidade, sem os campos da galeria.
	if err := app.SaveScreenshotPolicy(`{"blockedProcesses":["keepass"]}`); err != nil {
		t.Fatalf("salvar politica do formulario: %v", err)
	}
	policy := app.currentScreenshotPolicy()
	if !policy.CopyOnConfirmEnabled() {
		t.Fatal("copyOnConfirm deveria ser preservado")
	}
	if policy.GalleryAllowed() {
		t.Fatal("galleryEnabled=false deveria ser preservado")
	}
	if policy.GalleryItemLimit() != 10 {
		t.Fatalf("galleryMaxItems = %d, want 10", policy.GalleryItemLimit())
	}
}

// Regressão: o segundo save no MESMO segundo e com o MESMO sufixo aleatório
// gerava o mesmo nome e os.WriteFile truncava a captura anterior.
func TestSaveScreenshotImageNaoSobrescreveArquivoEmColisaoDeNome(t *testing.T) {
	app := newScreenshotTestApp(t)
	gallery := t.TempDir()
	withGalleryDir(t, gallery)
	prevSuffix := screenshotRandomSuffix
	screenshotRandomSuffix = func(int) string { return "aaaa" }
	t.Cleanup(func() { screenshotRandomSuffix = prevSuffix })

	firstURL, _ := testGalleryPNG(t, 6, 4)
	firstBody, err := app.SaveScreenshotImage(firstURL, `{"format":"png","prefix":"colisao"}`)
	if err != nil {
		t.Fatalf("primeiro save: %v", err)
	}
	var first screenshot.SaveImageResult
	if err := json.Unmarshal([]byte(firstBody), &first); err != nil {
		t.Fatal(err)
	}
	firstData, err := os.ReadFile(first.Path)
	if err != nil {
		t.Fatalf("primeiro arquivo nao existe: %v", err)
	}
	if screenshot.SHA256Hex(firstData) != first.SHA256 {
		t.Fatal("hash do primeiro arquivo nao corresponde")
	}

	secondURL, _ := testGalleryPNG(t, 7, 3)
	secondBody, err := app.SaveScreenshotImage(secondURL, `{"format":"png","prefix":"colisao"}`)
	if err != nil {
		t.Fatalf("colisao de nome deveria ser resolvida com outro nome: %v", err)
	}
	var second screenshot.SaveImageResult
	if err := json.Unmarshal([]byte(secondBody), &second); err != nil {
		t.Fatal(err)
	}
	if second.Path == first.Path {
		t.Fatalf("o segundo save reutilizou o mesmo caminho %q", second.Path)
	}
	if second.Width != 7 || second.Height != 3 {
		t.Fatalf("metadados do segundo save = %+v", second)
	}
	kept, err := os.ReadFile(first.Path)
	if err != nil {
		t.Fatalf("o primeiro arquivo desapareceu: %v", err)
	}
	if screenshot.SHA256Hex(kept) != first.SHA256 {
		t.Fatal("o segundo save sobrescreveu o primeiro arquivo")
	}
	secondData, err := os.ReadFile(second.Path)
	if err != nil || screenshot.SHA256Hex(secondData) != second.SHA256 {
		t.Fatalf("segundo arquivo nao corresponde ao metadado: %v", err)
	}
}

// makeDirLink cria um link de diretório para simular perfil/galeria redirecionada.
// os.Symlink exige privilégio no Windows; a junção (mklink /J) não exige.
func makeDirLink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err == nil {
		return
	}
	if runtime.GOOS != "windows" {
		t.Skip("sem permissao para criar link simbolico")
	}
	out, cmdErr := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if cmdErr != nil {
		t.Skipf("nao foi possivel criar juncao (%v): %s", cmdErr, strings.TrimSpace(string(out)))
	}
	t.Cleanup(func() { _ = os.Remove(link) })
}

// Regressão: com a galeria (ou um diretório acima) atrás de junção/link, o alvo
// resolvido nunca ficava "dentro" da galeria NÃO resolvida e toda leitura caía
// em "acesso negado" — o arquivo salvo existia, mas a miniatura não carregava.
func TestScreenshotGalleryLeituraPorPastaRedirecionada(t *testing.T) {
	app := newScreenshotTestApp(t)
	realDir := t.TempDir()
	link := filepath.Join(t.TempDir(), "Discovery Capturas")
	makeDirLink(t, realDir, link)
	withGalleryDir(t, link)

	dataURL, _ := testGalleryPNG(t, 5, 5)
	body, err := app.SaveScreenshotImage(dataURL, `{"format":"png","prefix":"link"}`)
	if err != nil {
		t.Fatalf("SaveScreenshotImage: %v", err)
	}
	var saved screenshot.SaveImageResult
	if err := json.Unmarshal([]byte(body), &saved); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ReadScreenshotImageFile(saved.Path, 0); err != nil {
		t.Fatalf("leitura pela pasta redirecionada falhou: %v", err)
	}
	if err := app.DeleteScreenshotImageFile(saved.Path); err != nil {
		t.Fatalf("exclusao pela pasta redirecionada falhou: %v", err)
	}
}

// O guard de symlink continua valendo: uma junção DENTRO da galeria apontando
// para fora não pode liberar leitura nem exclusão.
func TestScreenshotGalleryRejectsJuncaoParaFora(t *testing.T) {
	app := newScreenshotTestApp(t)
	gallery := t.TempDir()
	withGalleryDir(t, gallery)
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "segredo.png")
	if err := os.WriteFile(outsideFile, []byte("nao-e-imagem"), 0o644); err != nil {
		t.Fatal(err)
	}
	makeDirLink(t, outside, filepath.Join(gallery, "atalho"))

	if _, err := app.ReadScreenshotImageFile(filepath.Join(gallery, "atalho", "segredo.png"), 0); err == nil {
		t.Fatal("leitura por juncao para fora da galeria deveria falhar")
	}
	if err := app.DeleteScreenshotImageFile(filepath.Join(gallery, "atalho", "segredo.png")); err == nil {
		t.Fatal("exclusao por juncao para fora da galeria deveria falhar")
	}
	if _, err := os.Stat(outsideFile); err != nil {
		t.Fatalf("o arquivo fora da galeria nao pode ser apagado: %v", err)
	}
}

func TestParseOCRScriptOutput(t *testing.T) {
	result, ok := parseOCRScriptOutput("aviso qualquer\n{\"ok\":true,\"engineAvailable\":true,\"text\":\"Ola mundo\",\"error\":\"\"}\n")
	if !ok {
		t.Fatal("JSON do OCR nao foi reconhecido")
	}
	if !result.EngineAvailable || result.Text != "Ola mundo" || result.Error != "" {
		t.Fatalf("resultado = %+v", result)
	}

	result, ok = parseOCRScriptOutput(`{"ok":false,"engineAvailable":false,"text":"","error":"idioma ausente"}`)
	if !ok || result.EngineAvailable || result.Error != "idioma ausente" || result.Text != "" {
		t.Fatalf("degradacao = %+v ok=%v", result, ok)
	}
	if _, ok := parseOCRScriptOutput("sem json"); ok {
		t.Fatal("saida sem JSON nao deveria ser reconhecida")
	}
}

func TestParseOCRScriptOutputIgnoraBOM(t *testing.T) {
	// TrimSpace não remove U+FEFF: sem limpar o BOM o HasPrefix("{") falhava e um
	// OCR bem-sucedido era reportado como "nenhuma resposta do motor".
	raw := "\ufeff{\"ok\":true,\"engineAvailable\":true,\"text\":\"Ola\",\"error\":\"\"}\r\n"
	result, ok := parseOCRScriptOutput(raw)
	if !ok {
		t.Fatal("JSON precedido de BOM deveria ser reconhecido")
	}
	if !result.EngineAvailable || result.Text != "Ola" || result.Error != "" {
		t.Fatalf("resultado = %+v", result)
	}
}

func TestEncodeOCRResultTruncaSemQuebrarUTF8(t *testing.T) {
	// "a" + N "é": o corte em 200000 bytes cai no meio de uma rune (2 bytes cada).
	text := "a" + strings.Repeat("é", screenshot.OCRMaxOutputChars/2)
	if len(text) <= screenshot.OCRMaxOutputChars {
		t.Fatalf("texto de teste (%d bytes) precisa passar do teto", len(text))
	}
	var result ocrBindingResult
	if err := json.Unmarshal([]byte(encodeOCRResult(ocrBindingResult{Text: text, EngineAvailable: true})), &result); err != nil {
		t.Fatalf("json do OCR invalido: %v", err)
	}
	if len(result.Text) > screenshot.OCRMaxOutputChars {
		t.Fatalf("texto truncado ficou acima do teto: %d bytes", len(result.Text))
	}
	if strings.ContainsRune(result.Text, utf8.RuneError) {
		t.Fatal("o corte partiu uma rune e o texto saiu corrompido")
	}
	if !strings.HasPrefix(text, result.Text) {
		t.Fatal("o texto truncado nao e um prefixo valido do original")
	}
}

func TestOcrScreenshotImageDegradesOnInvalidImage(t *testing.T) {
	app := newScreenshotTestApp(t)
	body, err := app.OcrScreenshotImage("data:image/png;base64,AAAA", "pt-BR")
	if err != nil {
		t.Fatalf("OCR nao pode falhar o binding: %v", err)
	}
	var result ocrBindingResult
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatalf("json do OCR: %v", err)
	}
	if result.EngineAvailable || result.Text != "" || result.Error == "" {
		t.Fatalf("degradacao esperada: %+v", result)
	}
}
