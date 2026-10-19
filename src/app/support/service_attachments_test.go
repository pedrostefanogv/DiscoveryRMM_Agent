package support

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// O console unifica a configuração de Anexos; o anexo do CHAMADO/repositório
// usa o limite de arquivo grande (maxLargeFileSizeBytes), que antes o agent
// não conhecia.
func TestGetTicketAttachmentSettings_ParsesLargeFileLimit(t *testing.T) {
	var gotPath, gotAgentID string
	svc := newLifecycleService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/api/v1/agent-auth/me/tickets/attachment-settings") {
			http.NotFound(w, r)
			return
		}
		gotPath = r.URL.Path
		gotAgentID = r.Header.Get("X-Agent-ID")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"enabled": true,
			"maxFileSizeBytes": 10485760,
			"maxLargeFileSizeBytes": 2147483648,
			"allowedContentTypes": ["image/png", "application/pdf"],
			"presignedUploadUrlTtlMinutes": 15
		}`))
	})

	settings, err := svc.GetTicketAttachmentSettings()
	if err != nil {
		t.Fatalf("consulta da policy falhou: %v", err)
	}
	if gotPath != "/api/v1/agent-auth/me/tickets/attachment-settings" {
		t.Fatalf("rota inesperada: %q", gotPath)
	}
	if gotAgentID != lifecycleAgentID {
		t.Fatalf("X-Agent-ID nao enviado, got=%q", gotAgentID)
	}
	if settings.MaxFileSizeBytes != 10*1024*1024 {
		t.Fatalf("maxFileSizeBytes=%d", settings.MaxFileSizeBytes)
	}
	if settings.MaxLargeFileSizeBytes != 2*1024*1024*1024 {
		t.Fatalf("maxLargeFileSizeBytes nao decodificado: %d", settings.MaxLargeFileSizeBytes)
	}
	if len(settings.AllowedContentTypes) != 2 || settings.AllowedContentTypes[1] != "application/pdf" {
		t.Fatalf("allowlist inesperada: %+v", settings.AllowedContentTypes)
	}
	if settings.PresignedUploadURLTTL != 15 {
		t.Fatalf("presignedUploadUrlTtlMinutes=%d", settings.PresignedUploadURLTTL)
	}
}

// Endpoint NOVO do chat IA: mesma auth de agent e parse do contrato.
func TestGetAIChatAttachmentSettings_FetchesAndParses(t *testing.T) {
	var gotPath string
	svc := newLifecycleService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/api/v1/agent-auth/me/ai-chat/attachment-settings") {
			http.NotFound(w, r)
			return
		}
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"enabled":true,"maxFileSizeBytes":10485760,"allowedContentTypes":["image/png","image/jpeg"]}`))
	})

	settings, err := svc.GetAIChatAttachmentSettings()
	if err != nil {
		t.Fatalf("consulta da policy do chat IA falhou: %v", err)
	}
	if gotPath != "/api/v1/agent-auth/me/ai-chat/attachment-settings" {
		t.Fatalf("rota inesperada: %q", gotPath)
	}
	if !settings.Enabled || settings.MaxFileSizeBytes != 10*1024*1024 {
		t.Fatalf("policy inesperada: %+v", settings)
	}
	if len(settings.AllowedContentTypes) != 2 {
		t.Fatalf("allowlist inesperada: %+v", settings.AllowedContentTypes)
	}
}

// Resposta parcial (servidor antigo) não pode zerar limite/allowlist nem
// liberar executáveis no fallback.
func TestGetAIChatAttachmentSettings_PartialFallsBack(t *testing.T) {
	svc := newLifecycleService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"enabled":true}`))
	})

	settings, err := svc.GetAIChatAttachmentSettings()
	if err != nil {
		t.Fatalf("consulta falhou: %v", err)
	}
	defaults := DefaultAIAttachmentSettings()
	if settings.MaxFileSizeBytes != defaults.MaxFileSizeBytes {
		t.Fatalf("limite deveria cair no default: %d", settings.MaxFileSizeBytes)
	}
	if len(settings.AllowedContentTypes) != len(defaults.AllowedContentTypes) {
		t.Fatalf("allowlist deveria cair no default: %+v", settings.AllowedContentTypes)
	}
	for _, item := range settings.AllowedContentTypes {
		if !strings.HasPrefix(item, "image/") {
			t.Fatalf("default do chat IA nao deve conter executaveis/outros tipos: %q", item)
		}
	}
}

// enabled=false é decisão do servidor: o fallback NÃO pode religar o seletor.
func TestGetAIChatAttachmentSettings_DisabledPreserved(t *testing.T) {
	svc := newLifecycleService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"enabled":false,"maxFileSizeBytes":2097152,"allowedContentTypes":["image/png"]}`))
	})

	settings, err := svc.GetAIChatAttachmentSettings()
	if err != nil {
		t.Fatalf("consulta falhou: %v", err)
	}
	if settings.Enabled {
		t.Fatal("enabled=false foi ignorado")
	}
	if settings.MaxFileSizeBytes != 2*1024*1024 {
		t.Fatalf("limite do servidor foi sobrescrito: %d", settings.MaxFileSizeBytes)
	}
}

// O parse JSON do contrato precisa aceitar o campo novo isoladamente.
func TestTicketAttachmentSettings_MaxLargeJSONRoundTrip(t *testing.T) {
	var settings TicketAttachmentSettings
	raw := []byte(`{"enabled":true,"maxFileSizeBytes":10485760,"maxLargeFileSizeBytes":2147483648,"allowedContentTypes":["image/png"]}`)
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if settings.MaxLargeFileSizeBytes != 2147483648 {
		t.Fatalf("maxLargeFileSizeBytes=%d", settings.MaxLargeFileSizeBytes)
	}
}
