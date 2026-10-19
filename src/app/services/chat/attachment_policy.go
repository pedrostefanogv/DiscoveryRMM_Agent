package chat

import (
	"encoding/base64"
	"strings"
)

// AttachmentPolicy é a dica de UI/validação para os anexos de imagem do chat
// IA/LLM. A AUTORIDADE da allowlist e do limite é o servidor
// (GET /agent-auth/me/ai-chat/attachment-settings): aqui só evitamos enviar um
// data URL que o servidor recusaria/descartaria silenciosamente.
type AttachmentPolicy struct {
	Enabled             bool     `json:"enabled"`
	AllowedContentTypes []string `json:"allowedContentTypes"`
	MaxFileSizeBytes    int64    `json:"maxFileSizeBytes"`
}

// defaultAIChatMaxImageBytes é o limite padrão do servidor (10 MB) usado
// quando a política não pôde ser consultada. Não substitui a validação do
// servidor — é apenas o fallback local.
const defaultAIChatMaxImageBytes = 10 * 1024 * 1024

// defaultAIChatImageTypes é a allowlist padrão de imagens (sem executáveis).
var defaultAIChatImageTypes = []string{"image/png", "image/jpeg", "image/webp", "image/gif"}

// DefaultAttachmentPolicy espelha a política padrão do servidor.
func DefaultAttachmentPolicy() AttachmentPolicy {
	return AttachmentPolicy{
		Enabled:             true,
		AllowedContentTypes: append([]string(nil), defaultAIChatImageTypes...),
		MaxFileSizeBytes:    defaultAIChatMaxImageBytes,
	}
}

// normalizeAttachmentPolicy preenche campos ausentes/zerados (resposta parcial
// ou falha de rede) sem forçar enabled=true: um "enabled=false" explícito do
// servidor precisa continuar valendo.
func normalizeAttachmentPolicy(policy AttachmentPolicy) AttachmentPolicy {
	defaults := DefaultAttachmentPolicy()
	if len(policy.AllowedContentTypes) == 0 {
		policy.AllowedContentTypes = defaults.AllowedContentTypes
	}
	if policy.MaxFileSizeBytes <= 0 {
		policy.MaxFileSizeBytes = defaults.MaxFileSizeBytes
	}
	return policy
}

// normalizeContentType devolve o MIME em minúsculas sem parâmetros
// ("image/png;charset=utf-8" -> "image/png").
func normalizeContentType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if idx := strings.IndexByte(value, ';'); idx >= 0 {
		value = strings.TrimSpace(value[:idx])
	}
	return value
}

// contentTypeAllowed informa se o MIME está na allowlist. Lista vazia =
// política indisponível: não bloqueia localmente (o servidor decide).
func contentTypeAllowed(contentType string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	wanted := normalizeContentType(contentType)
	for _, item := range allowed {
		if normalizeContentType(item) == wanted {
			return true
		}
	}
	return false
}

// imageDataURLInfo extrai o MIME e o tamanho DECODIFICADO de um data URL de
// imagem base64. Devolve ok=false para qualquer coisa que não seja
// "data:image/<mime>;base64,<payload>" com base64 válido.
func imageDataURLInfo(dataURL string) (contentType string, sizeBytes int64, ok bool) {
	dataURL = strings.TrimSpace(dataURL)
	if !strings.HasPrefix(dataURL, "data:") {
		return "", 0, false
	}
	comma := strings.IndexByte(dataURL, ',')
	if comma < 0 {
		return "", 0, false
	}
	header := dataURL[len("data:"):comma]
	payload := dataURL[comma+1:]
	if !strings.Contains(strings.ToLower(header), ";base64") {
		return "", 0, false
	}
	contentType = normalizeContentType(strings.SplitN(header, ";", 2)[0])
	if !strings.HasPrefix(contentType, "image/") {
		return "", 0, false
	}
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || len(decoded) == 0 {
		return "", 0, false
	}
	return contentType, int64(len(decoded)), true
}

// validateChatImage valida um data URL contra a política. Devolve o motivo da
// recusa (chave curta, para log) ou "" quando o anexo pode ser enviado.
func validateChatImage(dataURL string, policy AttachmentPolicy) string {
	policy = normalizeAttachmentPolicy(policy)
	if !policy.Enabled {
		return "anexos de imagem desabilitados nas configuracoes do servidor"
	}
	contentType, sizeBytes, ok := imageDataURLInfo(dataURL)
	if !ok {
		return "data URL de imagem invalida"
	}
	if !contentTypeAllowed(contentType, policy.AllowedContentTypes) {
		return "tipo nao permitido: " + contentType
	}
	if sizeBytes > policy.MaxFileSizeBytes {
		return "tamanho excede o limite configurado"
	}
	return ""
}
