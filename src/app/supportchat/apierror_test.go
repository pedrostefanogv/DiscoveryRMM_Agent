package supportchat

import (
	"strings"
	"testing"
)

// Regressão do relato: ao anexar uma imagem a janela mostrava o JSON cru da API
// ("HTTP 400: {\"error\":\"Requisição inválida\",\"message\":\"Object storage não
// está configurado...\",\"traceId\":\"...\"}"). O usuário final precisa de uma
// mensagem legível que diga o que fazer.
func TestAPIErrorMessage_TraduzObjectStorageNaoConfigurado(t *testing.T) {
	raw := "{\"error\":\"Requisição inválida\",\"message\":\"Object storage não está configurado e não pode ser usado. Configure o storage em Configurações > Servidor. Detalhes: BucketName é obrigatório; Endpoint é obrigatório\",\"traceId\":\"0HNP7QS2VQO69:00000014\"}"

	got := apiErrorMessage(400, raw)
	if !strings.Contains(got, "object storage") {
		t.Fatalf("mensagem não explicou o object storage: %q", got)
	}
	for _, unwanted := range []string{"traceId", "HTTP 400", "BucketName"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("mensagem não pode expor detalhe técnico %q: %q", unwanted, got)
		}
	}
}

func TestAPIErrorMessage_UsaMessageDoCorpoJSON(t *testing.T) {
	got := apiErrorMessage(409, "{\"message\":\"session_not_active\"}")
	if got != "a conversa não está mais ativa." {
		t.Fatalf("esperava a tradução do conflito de sessão, obteve %q", got)
	}
}

func TestAPIErrorMessage_CorpoNaoJSONNaoEstoura(t *testing.T) {
	got := apiErrorMessage(502, "<html><body>Bad Gateway</body></html>")
	if !strings.Contains(got, "Bad Gateway") {
		t.Fatalf("corpo não-JSON deveria ser repassado, obteve %q", got)
	}
}

func TestAPIErrorMessage_CorpoVazio(t *testing.T) {
	got := apiErrorMessage(500, "   ")
	if !strings.Contains(got, "500") {
		t.Fatalf("corpo vazio deveria citar o status, obteve %q", got)
	}
}
