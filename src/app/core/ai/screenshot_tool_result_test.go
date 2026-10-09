package ai

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// Regressão: o tool result de uma captura de tela precisa chegar INTACTO ao
// servidor. O corte em 16 KB gerava JSON válido com base64 parcial (~94% da
// imagem perdida) e o LLM recebia um PNG truncado — sem erro em lugar nenhum.
func TestTruncateToolResultKeepsScreenshotImageIntact(t *testing.T) {
	raw := make([]byte, 200_000)
	for i := range raw {
		raw[i] = byte(i % 251)
	}
	original := base64.StdEncoding.EncodeToString(raw)
	body, err := json.Marshal(map[string]any{
		"ok":           true,
		"type":         "screenshot",
		"mode":         "full",
		"mime":         "image/png",
		"image_base64": original,
		"width":        3840,
		"height":       2160,
		"bytes":        len(raw),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	out := truncateToolResult("capture_screenshot", string(body))
	if out != string(body) {
		t.Fatalf("tool result de captura foi alterado: %d -> %d bytes", len(body), len(out))
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("payload de captura nao parseia: %v", err)
	}
	got, _ := parsed["image_base64"].(string)
	if got != original {
		t.Fatalf("base64 alterado: %d bytes, want %d", len(got), len(original))
	}
}

// Resultados sem imagem continuam truncados no teto curto (proteção de contexto).
func TestTruncateToolResultStillCapsTextResults(t *testing.T) {
	big := strings.Repeat("x", maxToolResultBytes*2)
	out := truncateToolResult("get_inventory", big)
	if len(out) > maxToolResultBytes+64 {
		t.Fatalf("resultado textual nao foi truncado: %d bytes", len(out))
	}
}

// Um texto que apenas CITA a chave image_base64 (ex.: conteúdo lido de um log)
// não pode ganhar o teto de 8 MiB: o teto curto continua valendo.
func TestTruncateToolResultIgnoresUnrelatedImageMention(t *testing.T) {
	quoted := `{"arquivo":"log.json","conteudo":"... "image_base64": "..." ..."}` + strings.Repeat("x", maxToolResultBytes*2)
	out := truncateToolResult("read_file", quoted)
	if len(out) > maxToolResultBytes+64 {
		t.Fatalf("resultado nao relacionado a captura nao foi truncado: %d bytes", len(out))
	}
}

// Residual do teto: acima de 8 MiB não pode haver corte dentro do base64 —
// o corte produziria JSON válido com imagem parcial (corrupção silenciosa).
func TestTruncateToolResultOversizedScreenshotFailsLoud(t *testing.T) {
	huge := make([]byte, maxScreenshotToolResultBytes)
	for i := range huge {
		huge[i] = 'A'
	}
	body, err := json.Marshal(map[string]any{
		"type":         "screenshot",
		"image_base64": string(huge),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Reforça o nome da tool: o payload real vem de capture_screenshot.
	out := truncateToolResult("capture_screenshot", string(body))
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("erro do teto nao e JSON valido: %v", err)
	}
	if _, hasImage := parsed["image_base64"]; hasImage {
		t.Fatalf("imagem acima do teto deveria ser substituida por erro, nao cortada")
	}
	if ok, _ := parsed["ok"].(bool); ok {
		t.Fatalf("resposta deveria sinalizar falha: %v", parsed)
	}
}
