package mcp

import (
	"context"
	"encoding/json"
	"testing"
)

// stubConsentBridge embute AppBridge (nil) e implementa só os métodos tocados
// pelos testes de exportação.
type stubConsentBridge struct {
	AppBridge
	approved bool
	asked    int
	exported int
}

func (s *stubConsentBridge) RequestFileWriteConsent(_ context.Context, _, _ string) (bool, error) {
	s.asked++
	return s.approved, nil
}

func (s *stubConsentBridge) ExportMarkdown() (string, error) {
	s.exported++
	return "C:/tmp/inventory.md", nil
}

func (s *stubConsentBridge) ExportPDF() (string, error) {
	s.exported++
	return "C:/tmp/inventory.pdf", nil
}

// Bug relatado: a IA gravava o relatório no disco sem pedir autorização. As
// tools de exportação DEVEM consultar o usuário antes de escrever qualquer
// arquivo; negado = nada é gravado e o LLM recebe approved=false (sem erro,
// para não repetir a chamada).
func TestExportToolsRequireUserConsent(t *testing.T) {
	reg := NewRegistry()
	stub := &stubConsentBridge{}
	RegisterDiscoveryTools(reg, stub)

	for _, tool := range []string{"export_inventory_markdown", "export_inventory_pdf"} {
		raw, _ := json.Marshal(map[string]any{})

		// 1) Usuário NEGA: nada é gravado, resultado estruturado.
		stub.approved = false
		stub.asked, stub.exported = 0, 0
		res, err := reg.Call(context.Background(), tool, raw)
		if err != nil {
			t.Fatalf("%s: negativa não deveria virar erro: %v", tool, err)
		}
		if stub.asked != 1 {
			t.Fatalf("%s: deveria pedir autorização (asked=%d)", tool, stub.asked)
		}
		if stub.exported != 0 {
			t.Fatalf("%s: gravou o arquivo SEM autorização", tool)
		}
		m, ok := res.(map[string]any)
		if !ok {
			t.Fatalf("%s: resultado inesperado: %#v", tool, res)
		}
		if approved, _ := m["approved"].(bool); approved {
			t.Fatalf("%s: approved deveria ser false na negativa", tool)
		}

		// 2) Usuário AUTORIZA: grava e devolve o caminho.
		stub.approved = true
		stub.asked, stub.exported = 0, 0
		res, err = reg.Call(context.Background(), tool, raw)
		if err != nil {
			t.Fatalf("%s: exportação autorizada falhou: %v", tool, err)
		}
		if stub.exported != 1 {
			t.Fatalf("%s: deveria gravar depois da autorização (exported=%d)", tool, stub.exported)
		}
		pathMap, ok := res.(map[string]string)
		if !ok || pathMap["path"] == "" {
			t.Fatalf("%s: caminho do arquivo não retornado: %#v", tool, res)
		}
	}
}
