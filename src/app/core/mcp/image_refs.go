package mcp

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
)

// ── Anexos de imagem para chamados/comentários ────────────────────────────
//
// A IA pode anexar ao chamado as imagens que o USUÁRIO enviou no último turno
// do chat (turn:N) e as capturas de tela feitas pela própria IA (shot:last /
// shot:<sha256>). O cache de screenshots vive só em memória: é um atalho para
// reutilizar uma captura já consentida, não um repositório de imagens.

const (
	// maxImageDataURLBytes limita cada imagem pelo tamanho da data URL
	// (base64), alinhado ao padrão de anexos da API (10 MB binários ~ 14 MB
	// base64; AiChatHelpers.MaxImageBase64Chars).
	maxImageDataURLBytes = 14 << 20
	// maxTotalImageBytes limita a soma dos bytes decodificados de um mesmo
	// pedido de anexo.
	maxTotalImageBytes = 32 << 20
)

// TicketImageUpload é uma imagem pronta para envio ao servidor, no formato que
// o bridge do App consome (DataBase64 sem o prefixo data:).
type TicketImageUpload struct {
	FileName    string
	ContentType string
	DataBase64  string
	// SizeBytes é o tamanho DECODIFICADO da imagem. Guardado no resolve para
	// os metadados não precisarem decodificar base64 de novo (e porque
	// base64.StdEncoding.DecodedLen erra para comprimentos sem padding).
	SizeBytes int64
}

var (
	screenshotMu    sync.RWMutex
	screenshotCache = map[string]string{}
	// screenshotOrder mantém a ordem de inserção para limitar o cache: cada
	// captura é um data URL de vários MB; sem teto o processo acumularia
	// memória indefinidamente. Guardamos só as N capturas mais recentes.
	screenshotOrder []string
	screenshotLast  string
)

// maxCachedScreenshots limita o cache de capturas em memória. 8 cobre com
// folga o fluxo "capturar -> anexar" de uma conversa, sem crescer sem limite.
const maxCachedScreenshots = 8

// cacheScreenshotImage guarda a data URL da captura e devolve a ref
// "shot:<sha256>" (string vazia para entrada vazia). O cache é indexado pelo
// sha256 da própria data URL.
func cacheScreenshotImage(dataURL string) string {
	dataURL = strings.TrimSpace(dataURL)
	if dataURL == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(dataURL))
	ref := "shot:" + hex.EncodeToString(sum[:])
	screenshotMu.Lock()
	if _, exists := screenshotCache[ref]; !exists {
		screenshotOrder = append(screenshotOrder, ref)
	}
	screenshotCache[ref] = dataURL
	screenshotLast = ref
	for len(screenshotOrder) > maxCachedScreenshots {
		oldest := screenshotOrder[0]
		screenshotOrder = screenshotOrder[1:]
		if oldest != screenshotLast {
			delete(screenshotCache, oldest)
		}
	}
	screenshotMu.Unlock()
	return ref
}

// screenshotByRef devolve a data URL cacheada para "shot:<hash>".
func screenshotByRef(ref string) (string, bool) {
	ref = normalizeShotRef(ref)
	if ref == "" {
		return "", false
	}
	screenshotMu.RLock()
	dataURL, ok := screenshotCache[ref]
	screenshotMu.RUnlock()
	return dataURL, ok
}

// lastScreenshotDataURL devolve a captura mais recente cacheada.
func lastScreenshotDataURL() (string, bool) {
	screenshotMu.RLock()
	ref := screenshotLast
	screenshotMu.RUnlock()
	if ref == "" {
		return "", false
	}
	return screenshotByRef(ref)
}

// normalizeShotRef normaliza "shot:<hash>" para minúsculas.
func normalizeShotRef(ref string) string {
	ref = strings.ToLower(strings.TrimSpace(ref))
	if !strings.HasPrefix(ref, "shot:") {
		return ""
	}
	return ref
}

// resetScreenshotCacheForTests limpa o cache global (apenas testes).
func resetScreenshotCacheForTests() {
	screenshotMu.Lock()
	screenshotCache = map[string]string{}
	screenshotOrder = nil
	screenshotLast = ""
	screenshotMu.Unlock()
}

// parseImageRefsArg aceita uma string separada por vírgulas ("turn:1,shot:last")
// ou um array JSON (["turn:1","shot:last"]). Remove vazios e duplicatas,
// preservando a ordem pedida pelo usuário.
func parseImageRefsArg(refsArg string) ([]string, error) {
	raw := strings.TrimSpace(refsArg)
	if raw == "" {
		return nil, fmt.Errorf("imageRefs nao pode ser vazio")
	}

	var refs []string
	if strings.HasPrefix(raw, "[") {
		if err := json.Unmarshal([]byte(raw), &refs); err != nil {
			return nil, fmt.Errorf("imageRefs invalido: envie uma lista de refs separadas por virgula ou um array JSON de strings: %w", err)
		}
	} else {
		refs = strings.Split(raw, ",")
	}

	out := make([]string, 0, len(refs))
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, ref)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("imageRefs nao pode ser vazio")
	}
	return out, nil
}

// countImageRefs devolve quantas refs distintas o argumento pede (0 se inválido).
// Usado apenas para montar o texto do pedido de consentimento.
func countImageRefs(refsArg string) int {
	refs, err := parseImageRefsArg(refsArg)
	if err != nil {
		return 0
	}
	return len(refs)
}

// validateImageDataURL valida o prefixo, o tipo e o tamanho da data URL e
// devolve o content-type e os bytes decodificados. Função pura (testável).
func validateImageDataURL(dataURL string) (contentType string, data []byte, err error) {
	dataURL = strings.TrimSpace(dataURL)
	if dataURL == "" {
		return "", nil, fmt.Errorf("imagem vazia")
	}
	if len(dataURL) > maxImageDataURLBytes {
		return "", nil, fmt.Errorf("imagem excede o teto de 10 MB (data URL com %d bytes)", len(dataURL))
	}
	if !strings.HasPrefix(dataURL, "data:image/") {
		return "", nil, fmt.Errorf("data URL invalida: esperado prefixo data:image/")
	}
	comma := strings.IndexByte(dataURL, ',')
	if comma < 0 {
		return "", nil, fmt.Errorf("data URL invalida: separador ',' ausente")
	}
	header := strings.TrimPrefix(dataURL[:comma], "data:")
	parts := strings.Split(header, ";")
	mediaType := strings.ToLower(strings.TrimSpace(parts[0]))
	isBase64 := false
	for _, part := range parts[1:] {
		if strings.EqualFold(strings.TrimSpace(part), "base64") {
			isBase64 = true
		}
	}
	if !isBase64 {
		return "", nil, fmt.Errorf("data URL invalida: codificacao base64 ausente")
	}
	switch mediaType {
	case "image/png", "image/jpeg", "image/jpg", "image/webp", "image/gif":
		// image/jpg não é MIME padrão e a policy do servidor aceita image/jpeg:
		// normaliza para o upload não ser recusado por content-type.
		if mediaType == "image/jpg" {
			mediaType = "image/jpeg"
		}
	default:
		return "", nil, fmt.Errorf("tipo de imagem nao suportado: %q (use png, jpeg, webp ou gif)", mediaType)
	}
	payload := dataURL[comma+1:]
	data, err = base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", nil, fmt.Errorf("base64 invalido na imagem: %w", err)
	}
	if len(data) == 0 {
		return "", nil, fmt.Errorf("imagem vazia")
	}
	// O MIME declarado no data URL é controlado por quem envia: a assinatura
	// real dos bytes é a fonte de verdade (um data:image/png com payload HTML/SVG
	// não pode chegar ao servidor como se fosse PNG).
	detected := detectImageMagic(data)
	if detected == "" {
		return "", nil, fmt.Errorf("conteudo da imagem nao reconhecido como png, jpeg, webp ou gif")
	}
	if detected != mediaType {
		return "", nil, fmt.Errorf("MIME declarado (%s) nao corresponde ao conteudo real (%s)", mediaType, detected)
	}
	return mediaType, data, nil
}

// detectImageMagic identifica o tipo real da imagem pela assinatura dos bytes.
// Função pura, usada também pela tool view_ticket_attachment para não confiar
// no content-type declarado pelo servidor.
func detectImageMagic(data []byte) string {
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return "image/png"
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp"
	case len(data) >= 4 && bytes.Equal(data[:4], []byte("GIF8")):
		return "image/gif"
	default:
		return ""
	}
}

// ImageMagicContentType expõe a detecção por magic bytes para o AppBridge
// (download/visualização de anexos).
func ImageMagicContentType(data []byte) string {
	return detectImageMagic(data)
}

// imageFileExtension mapeia o content-type para a extensão do arquivo enviado.
func imageFileExtension(contentType string) string {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".png"
	}
}

// resolveOneImageRef resolve uma ref individual para a data URL correspondente.
func resolveOneImageRef(ref string, app AppBridge) (string, error) {
	ref = strings.TrimSpace(ref)
	lower := strings.ToLower(ref)

	switch {
	case strings.HasPrefix(lower, "turn:"):
		indexStr := strings.TrimSpace(ref[len("turn:"):])
		index, err := strconv.Atoi(indexStr)
		if err != nil || index <= 0 {
			return "", fmt.Errorf("ref turn:%s invalida: use turn:N com N inteiro >= 1 (imagem N do ultimo turno)", indexStr)
		}
		if app == nil {
			return "", fmt.Errorf("bridge do app indisponivel para resolver turn:%d", index)
		}
		dataURL, err := app.GetLastTurnImage(index)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(dataURL) == "" {
			return "", fmt.Errorf("turn:%d nao possui imagem", index)
		}
		return dataURL, nil

	case lower == "shot:last":
		dataURL, ok := lastScreenshotDataURL()
		if !ok {
			return "", fmt.Errorf("shot:last indisponivel: nenhuma captura de tela foi feita nesta sessao")
		}
		return dataURL, nil

	case strings.HasPrefix(lower, "shot:"):
		hash := strings.TrimSpace(lower[len("shot:"):])
		if len(hash) != sha256.Size*2 {
			return "", fmt.Errorf("ref shot:%s invalida: informe o sha256 completo (64 caracteres hexadecimais)", hash)
		}
		if _, err := hex.DecodeString(hash); err != nil {
			return "", fmt.Errorf("ref shot:%s invalida: hash nao e hexadecimal", hash)
		}
		dataURL, ok := screenshotByRef("shot:" + hash)
		if !ok {
			return "", fmt.Errorf("captura shot:%s nao encontrada no cache (capture de novo ou use shot:last)", hash)
		}
		return dataURL, nil

	default:
		return "", fmt.Errorf("ref %q nao suportada: use turn:N, shot:last ou shot:<sha256>", ref)
	}
}

// resolveImageRefs transforma as refs pedidas pela IA em imagens prontas para
// upload, validando tipo/tamanho e aplicando os tetos por imagem e total.
func resolveImageRefs(refsArg string, app AppBridge) ([]TicketImageUpload, error) {
	refs, err := parseImageRefsArg(refsArg)
	if err != nil {
		return nil, err
	}

	uploads := make([]TicketImageUpload, 0, len(refs))
	// Dedupe pelo sha256 dos BYTES decodificados: a mesma imagem pode chegar
	// por refs diferentes (ex.: turn:1 e shot:last da mesma captura) e não deve
	// ser enviada duas vezes ao servidor. Mantém a ordem da primeira ocorrência.
	seenHashes := make(map[[sha256.Size]byte]string, len(refs))
	totalBytes := 0
	for _, ref := range refs {
		dataURL, err := resolveOneImageRef(ref, app)
		if err != nil {
			return nil, fmt.Errorf("imageRef %q: %w", ref, err)
		}
		contentType, data, err := validateImageDataURL(dataURL)
		if err != nil {
			return nil, fmt.Errorf("imageRef %q: %w", ref, err)
		}
		sum := sha256.Sum256(data)
		if firstRef, repeated := seenHashes[sum]; repeated {
			log.Printf("[mcp] imagem repetida ignorada em imageRefs: %q tem os mesmos bytes de %q", ref, firstRef)
			continue
		}
		seenHashes[sum] = ref
		totalBytes += len(data)
		if totalBytes > maxTotalImageBytes {
			return nil, fmt.Errorf("teto total de anexos excedido: as imagens somam %d bytes (maximo %d bytes = 32 MiB); anexe menos imagens por vez", totalBytes, maxTotalImageBytes)
		}
		uploads = append(uploads, TicketImageUpload{
			FileName:    fmt.Sprintf("chat-image-%d%s", len(uploads)+1, imageFileExtension(contentType)),
			ContentType: contentType,
			DataBase64:  base64.StdEncoding.EncodeToString(data),
			SizeBytes:   int64(len(data)),
		})
	}
	return uploads, nil
}

// imageUploadSizeBytes devolve o tamanho decodificado de uma imagem pronta
// para upload, para os metadados devolvidos à IA (sem bytes).
func imageUploadSizeBytes(img TicketImageUpload) int64 {
	// SizeBytes é preenchido no resolve com o tamanho real decodificado.
	// Não usar base64.DecodedLen(len(trimmed)): para comprimentos sem padding
	// (len%4 == 2 ou 3) o helper retorna 0 e o metadado sairia errado.
	return img.SizeBytes
}
