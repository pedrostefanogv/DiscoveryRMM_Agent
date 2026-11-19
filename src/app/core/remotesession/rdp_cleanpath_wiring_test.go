package remotesession

import (
	"os"
	"strings"
	"testing"
)

// TestRdpSessionWiresRdcCleanPath garante que a sessao kind=rdp realmente sobe o
// caminho RDCleanPath (WebRDP no navegador) alem do tunel legado.
//
// Sem isso, o backend RDCleanPath existia mas NUNCA era executado: a aba RDP do
// site nao recebia response e o canvas ficava vazio — o defeito mais grave
// encontrado na revisao.
func TestRdpSessionWiresRdcCleanPath(t *testing.T) {
	src, err := os.ReadFile("manager.go")
	if err != nil {
		t.Fatalf("ler manager.go: %v", err)
	}
	code := string(src)

	required := []string{
		"runRdpCleanPath",              // funcao de integracao
		"rdcleanpath.NewSession",       // sessao RDCleanPath criada
		"rdcleanpath.NewNatsTransport", // transporte nos subjects dedicados
		"SubscribeToRdpCleanpathIn",    // assinatura do canal de entrada
		"go m.runRdpCleanPath",         // chamada efetiva no runRdpSession
	}
	for _, needle := range required {
		if !strings.Contains(code, needle) {
			t.Errorf("manager.go nao contem %q: o caminho RDCleanPath nao esta ligado", needle)
		}
	}

	// A integracao deve estar DENTRO de runRdpSession.
	idx := strings.Index(code, "func (m *Manager) runRdpSession")
	if idx < 0 {
		t.Fatal("runRdpSession nao encontrada")
	}
	body := code[idx:]
	// Limita ao corpo da funcao (ate a proxima func de mesmo nivel).
	if end := strings.Index(body[1:], "\nfunc (m *Manager) runProcessesSession"); end > 0 {
		body = body[:end+1]
	}
	if !strings.Contains(body, "runRdpCleanPath") {
		t.Error("runRdpCleanPath nao e chamado dentro de runRdpSession")
	}
}

// TestRdpSessionKeepsLegacyTunnel garante que o tunel legado (mstsc via
// --rdp-tunnel) NAO foi removido pela integracao — os dois coexistem.
func TestRdpSessionKeepsLegacyTunnel(t *testing.T) {
	src, err := os.ReadFile("manager.go")
	if err != nil {
		t.Fatalf("ler manager.go: %v", err)
	}
	if !strings.Contains(string(src), "NewSessionTunnel") {
		t.Error("o tunel legado foi removido; o mstsc via --rdp-tunnel quebraria")
	}
}
