package screenshot

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// Galeria local de capturas: arquivos de imagem que o usuário salva em disco
// para reutilizar no chat/chamado. O pacote concentra aqui as regras PURAS
// (nome, formato, hash, guard de caminho) para que os bindings do app cuidem só
// do I/O e possam ser testados sem tocar no perfil real do usuário.

const (
	// GalleryFolderName é a pasta criada dentro de Pictures.
	GalleryFolderName = "Discovery Capturas"
	// DefaultScreenshotPrefix é o prefixo usado quando a UI não informa um.
	DefaultScreenshotPrefix = "captura"
	// MaxScreenshotFileBytes limita uma imagem da galeria. O mesmo teto vale na
	// escrita e na leitura quando o chamador não pede outro.
	MaxScreenshotFileBytes = 32 << 20
	// MaxScreenshotPrefixRunes evita nomes de arquivo absurdos no disco.
	MaxScreenshotPrefixRunes = 40
	// jpegDefaultQuality é usada quando o formato pedido é JPG.
	jpegDefaultQuality = 92
)

// SaveImageOptions são as opções aceitas por SaveScreenshotImage.
type SaveImageOptions struct {
	Format    string `json:"format"`    // "png" ou "jpg"; vazio = infere da data URL
	Directory string `json:"directory"` // vazio = pasta padrão da galeria
	Prefix    string `json:"prefix"`    // vazio = "captura"
}

// SaveImageResult é o contrato JSON devolvido ao frontend.
type SaveImageResult struct {
	Path      string `json:"path"`
	FileName  string `json:"fileName"`
	Directory string `json:"directory"`
	Bytes     int    `json:"bytes"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	SHA256    string `json:"sha256"`
	Format    string `json:"format"`
}

// Evidence é o metadado do "modo evidência": identifica o binário original sem
// reencodar a imagem (o hash precisa ser do que o usuário viu/anexou).
type Evidence struct {
	SHA256        string `json:"sha256"`
	Bytes         int    `json:"bytes"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	CapturedAtUTC string `json:"capturedAtUtc"`
}

// ParseSaveImageOptions interpreta e normaliza o JSON de opções. String vazia é
// válida (tudo padrão); JSON inválido, formato desconhecido ou diretório
// relativo/perigoso são recusados com erro claro.
func ParseSaveImageOptions(raw string) (SaveImageOptions, error) {
	var opts SaveImageOptions
	if trimmed := strings.TrimSpace(raw); trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &opts); err != nil {
			return SaveImageOptions{}, fmt.Errorf("opcoes de salvamento invalidas: %w", err)
		}
	}

	format, ok := NormalizeImageFormat(opts.Format)
	if !ok {
		return SaveImageOptions{}, fmt.Errorf("formato de imagem invalido: %q (use png ou jpg)", opts.Format)
	}
	opts.Format = format

	dir, err := NormalizeSaveDirectory(opts.Directory)
	if err != nil {
		return SaveImageOptions{}, err
	}
	opts.Directory = dir
	opts.Prefix = SanitizeFilePrefix(opts.Prefix)
	return opts, nil
}

// NormalizeImageFormat devolve "png"/"jpg" (ou "" para entrada vazia). Aceita
// também os MIMEs que a UI costuma repassar sem conversão.
func NormalizeImageFormat(format string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "":
		return "", true
	case "png", "image/png":
		return "png", true
	case "jpg", "jpeg", "image/jpeg", "image/jpg":
		return "jpg", true
	default:
		return "", false
	}
}

// ImageFormatFromDataURL infere o formato pelo MIME da data URL.
func ImageFormatFromDataURL(dataURL string) string {
	value := strings.ToLower(strings.TrimSpace(dataURL))
	if !strings.HasPrefix(value, "data:") {
		return ""
	}
	header := value[len("data:"):]
	if idx := strings.IndexAny(header, ";,"); idx >= 0 {
		header = header[:idx]
	}
	switch header {
	case "image/png":
		return "png"
	case "image/jpeg", "image/jpg":
		return "jpg"
	default:
		return ""
	}
}

// ImageFormatExtension devolve a extensão (sem ponto) do formato.
func ImageFormatExtension(format string) string {
	if normalized, ok := NormalizeImageFormat(format); ok && normalized != "" {
		return normalized
	}
	return "png"
}

// SanitizeFilePrefix limpa o prefixo do nome: apenas ASCII alfanumérico, "_" e
// "-", sem sequências repetidas de separador. Não deixa o prefixo virar caminho
// (a barra nunca sobrevive), o que fecha a porta para "../".
func SanitizeFilePrefix(prefix string) string {
	trimmed := strings.TrimSpace(prefix)
	if trimmed == "" {
		return DefaultScreenshotPrefix
	}
	var b strings.Builder
	lastDash := false
	for _, r := range trimmed {
		var out rune
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			out = unicode.ToLower(r)
		case r == '-' || r == ' ' || r == '.':
			out = '-'
		default:
			// Qualquer outro caractere (barra, dois-pontos, acento) vira
			// separador: preservar letras acentuadas abriria nomes estranhos no
			// disco e remover sem separador colaria palavras ("a/b" -> "ab").
			out = '-'
		}
		if out == '-' {
			if lastDash {
				continue
			}
			lastDash = true
		} else {
			lastDash = false
		}
		b.WriteRune(out)
		if b.Len() >= MaxScreenshotPrefixRunes {
			break
		}
	}
	clean := strings.Trim(b.String(), "-_")
	if clean == "" {
		return DefaultScreenshotPrefix
	}
	return clean
}

// NormalizeSaveDirectory valida o override de diretório. Precisa ser absoluto e
// não pode conter segmento "..": gravar captura em caminho relativo deixaria o
// destino dependente do CWD do processo (inclusive do serviço, em SYSTEM).
func NormalizeSaveDirectory(dir string) (string, error) {
	value := strings.TrimSpace(dir)
	if value == "" {
		return "", nil
	}
	if strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("diretorio de salvamento invalido")
	}
	cleaned := filepath.Clean(value)
	for _, part := range strings.Split(filepath.ToSlash(cleaned), "/") {
		if part == ".." {
			return "", fmt.Errorf("diretorio de salvamento nao pode conter \"..\": %q", dir)
		}
	}
	if !filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("diretorio de salvamento precisa ser absoluto: %q", dir)
	}
	if filepath.VolumeName(cleaned) == "" {
		return "", fmt.Errorf("diretorio de salvamento sem volume: %q", dir)
	}
	return cleaned, nil
}

// BuildScreenshotFileName monta o nome canônico
// <prefixo>-YYYYMMDD-HHmmss-<sufixo>.<ext>. O sufixo é sanitizado; quando fica
// vazio (chamador esqueceu de gerar) usa "0000" para não produzir nome ambíguo.
func BuildScreenshotFileName(prefix string, at time.Time, suffix, format string) string {
	cleanPrefix := SanitizeFilePrefix(prefix)
	cleanSuffix := sanitizeSuffix(suffix)
	if cleanSuffix == "" {
		cleanSuffix = "0000"
	}
	if at.IsZero() {
		at = time.Now()
	}
	return fmt.Sprintf("%s-%s-%s.%s", cleanPrefix, at.Format("20060102-150405"), cleanSuffix, ImageFormatExtension(format))
}

func sanitizeSuffix(suffix string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(suffix)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
		if b.Len() >= 8 {
			break
		}
	}
	return b.String()
}

// RandomSuffix gera n caracteres aleatórios (cripto) para evitar colisão de
// nomes salvos no mesmo segundo. Em falha do gerador devolve zeros — o arquivo
// ainda é gravado, com risco de nome repetido tratado pelo caller.
func RandomSuffix(n int) string {
	if n <= 0 {
		return ""
	}
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return strings.Repeat("0", n)
	}
	for i := range buf {
		buf[i] = charset[int(buf[i])%len(charset)]
	}
	return string(buf)
}

// EncodeImage codifica a imagem no formato pedido ("png" ou "jpg"), com teto de
// tamanho. O formato do arquivo SEMPRE acompanha a extensão — não adianta salvar
// um JPEG com nome .png.
func EncodeImage(img image.Image, format string, quality int) ([]byte, error) {
	if img == nil {
		return nil, fmt.Errorf("imagem vazia")
	}
	bounds := img.Bounds()
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		return nil, fmt.Errorf("imagem sem dimensoes")
	}
	normalized, ok := NormalizeImageFormat(format)
	if !ok || normalized == "" {
		normalized = "png"
	}
	var buf bytes.Buffer
	switch normalized {
	case "jpg":
		if quality <= 0 || quality > 100 {
			quality = jpegDefaultQuality
		}
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
			return nil, fmt.Errorf("jpeg encode: %w", err)
		}
	default:
		if err := png.Encode(&buf, img); err != nil {
			return nil, fmt.Errorf("png encode: %w", err)
		}
	}
	if buf.Len() > MaxScreenshotFileBytes {
		return nil, fmt.Errorf("imagem codificada acima do limite (%d bytes)", MaxScreenshotFileBytes)
	}
	return buf.Bytes(), nil
}

// SHA256Hex devolve o SHA-256 em hexadecimal minúsculo.
func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// BuildEvidence monta o metadado de evidência a partir do binário decodificado
// (hash do binário ORIGINAL) e da imagem já decodificada (dimensões).
func BuildEvidence(raw []byte, img image.Image, at time.Time) Evidence {
	width, height := 0, 0
	if img != nil {
		bounds := img.Bounds()
		width, height = bounds.Dx(), bounds.Dy()
	}
	if at.IsZero() {
		at = time.Now()
	}
	return Evidence{
		SHA256:        SHA256Hex(raw),
		Bytes:         len(raw),
		Width:         width,
		Height:        height,
		CapturedAtUTC: at.UTC().Format(time.RFC3339),
	}
}

// PathWithinDir informa se target está DENTRO de dir (não a própria pasta).
// Comparação lexical após Clean; no Windows é case-insensitive. É a base do
// guard da galeria: "C:\Capturas2" não pode passar por prefixo de "C:\Capturas".
func PathWithinDir(dir, target string) bool {
	base := filepath.Clean(strings.TrimSpace(dir))
	candidate := filepath.Clean(strings.TrimSpace(target))
	if base == "" || candidate == "" {
		return false
	}
	rel, err := filepath.Rel(base, candidate)
	if err != nil || rel == "." || filepath.IsAbs(rel) {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// ResolveGalleryPath resolve o caminho pedido pela UI e recusa qualquer coisa
// fora da pasta da galeria. Caminho relativo é aceito só como nome de arquivo
// (a UI às vezes manda apenas o basename).
func ResolveGalleryPath(galleryDir, target string) (string, error) {
	base, err := filepath.Abs(strings.TrimSpace(galleryDir))
	if err != nil {
		return "", fmt.Errorf("pasta da galeria invalida: %w", err)
	}
	value := strings.TrimSpace(target)
	if value == "" {
		return "", fmt.Errorf("caminho da imagem vazio")
	}
	if strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("caminho da imagem invalido")
	}
	if !filepath.IsAbs(value) {
		if strings.ContainsAny(value, "/\\") || value == ".." {
			return "", fmt.Errorf("caminho da imagem precisa ser absoluto")
		}
		value = filepath.Join(base, value)
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("caminho da imagem invalido: %w", err)
	}
	absolute = filepath.Clean(absolute)
	if !PathWithinDir(base, absolute) {
		return "", fmt.Errorf("acesso negado: %q nao esta na pasta da galeria", target)
	}
	return absolute, nil
}
