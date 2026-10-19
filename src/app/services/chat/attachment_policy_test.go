package chat

import (
	"encoding/base64"
	"strings"
	"testing"
)

func dataURLFor(contentType string, payload []byte) string {
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(payload)
}

func TestValidateChatImage(t *testing.T) {
	policy := AttachmentPolicy{
		Enabled:             true,
		AllowedContentTypes: []string{"image/png", "image/jpeg"},
		MaxFileSizeBytes:    4,
	}

	tests := []struct {
		name     string
		image    string
		wantPart string
	}{
		{name: "png valido no limite", image: dataURLFor("image/png", []byte("1234")), wantPart: ""},
		{name: "jpeg valido", image: dataURLFor("image/jpeg", []byte("12")), wantPart: ""},
		{name: "tipo fora da allowlist", image: dataURLFor("image/svg+xml", []byte("12")), wantPart: "tipo"},
		{name: "tamanho decodificado acima do limite", image: dataURLFor("image/png", []byte("12345")), wantPart: "tamanho"},
		{name: "base64 invalido", image: "data:image/png;base64,@@@", wantPart: "invalida"},
		{name: "sem base64", image: "data:image/png,AQID", wantPart: "invalida"},
		{name: "nao e imagem", image: "data:application/pdf;base64,AQID", wantPart: "invalida"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason := validateChatImage(tt.image, policy)
			if tt.wantPart == "" {
				if reason != "" {
					t.Fatalf("anexo deveria ser aceito, motivo=%q", reason)
				}
				return
			}
			if !strings.Contains(reason, tt.wantPart) {
				t.Fatalf("motivo=%q, esperado conter %q", reason, tt.wantPart)
			}
		})
	}
}

// enabled=false: nenhuma imagem passa (o servidor desligou os anexos).
func TestValidateChatImage_Disabled(t *testing.T) {
	policy := AttachmentPolicy{
		Enabled:             false,
		AllowedContentTypes: []string{"image/png"},
		MaxFileSizeBytes:    1024,
	}
	if reason := validateChatImage(dataURLFor("image/png", []byte("ok")), policy); !strings.Contains(reason, "desabilitados") {
		t.Fatalf("motivo=%q", reason)
	}
}

// Resposta parcial não pode desligar a validação nem zerar o limite: o
// normalizador mantém o default de 10 MB e a allowlist de imagens.
func TestNormalizeAttachmentPolicy_Partial(t *testing.T) {
	policy := normalizeAttachmentPolicy(AttachmentPolicy{Enabled: true})
	if policy.MaxFileSizeBytes != defaultAIChatMaxImageBytes {
		t.Fatalf("limite=%d", policy.MaxFileSizeBytes)
	}
	if len(policy.AllowedContentTypes) == 0 {
		t.Fatal("allowlist vazia apos normalizar")
	}
	for _, item := range policy.AllowedContentTypes {
		if !strings.HasPrefix(item, "image/") {
			t.Fatalf("default do chat IA nao deve conter executaveis: %q", item)
		}
	}
}

// Default explícito: 10 MB e sem executáveis.
func TestDefaultAttachmentPolicy(t *testing.T) {
	policy := DefaultAttachmentPolicy()
	if !policy.Enabled {
		t.Fatal("default deveria estar habilitado")
	}
	if policy.MaxFileSizeBytes != 10*1024*1024 {
		t.Fatalf("limite default=%d", policy.MaxFileSizeBytes)
	}
	for _, item := range policy.AllowedContentTypes {
		if !strings.HasPrefix(item, "image/") {
			t.Fatalf("tipo inesperado no default: %q", item)
		}
	}
}

// Tamanho é o DECODIFICADO, não o tamanho da string base64.
func TestValidateChatImage_UsesDecodedSize(t *testing.T) {
	payload := make([]byte, 64)
	policy := AttachmentPolicy{
		Enabled:             true,
		AllowedContentTypes: []string{"image/png"},
		MaxFileSizeBytes:    64,
	}
	image := dataURLFor("image/png", payload)
	if len(image) <= int(policy.MaxFileSizeBytes) {
		t.Fatal("premissa do teste: data URL base64 deveria ser maior que o limite decodificado")
	}
	if reason := validateChatImage(image, policy); reason != "" {
		t.Fatalf("64 bytes decodificados deveriam caber no limite de 64: %q", reason)
	}
}
