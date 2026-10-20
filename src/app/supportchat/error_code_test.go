package supportchat

import (
	"strings"
	"testing"
)

// O código estável da API precisa SOBREVIVER à tradução da mensagem.
//
// BUG ENCONTRADO NA REVISÃO: apiErrorMessage devolvia só o texto traduzido, e o
// agent decidia "convite já resolvido" procurando frases em português. Aqui a
// garantia: o code vem prefixado para quem precisa decidir por ele.
func TestApiErrorMessage_PreservaCodigoDeSessao(t *testing.T) {
	body := "{\"code\":\"session_declined\",\"error\":\"Operação inválida para o estado atual da conversa.\"}"
	msg := apiErrorMessage(409, body)
	if !strings.Contains(msg, "[session_declined]") {
		t.Fatalf("código ausente na mensagem: %q", msg)
	}
}

func TestApiErrorMessage_SessionNotActiveMantemTraducaoECodigo(t *testing.T) {
	msg := apiErrorMessage(409, "{\"code\":\"session_not_active\",\"error\":\"x\"}")
	if !strings.Contains(msg, "a conversa não está mais ativa") {
		t.Fatalf("tradução para o usuário final precisa continuar: %q", msg)
	}
	if !strings.Contains(msg, "[session_not_active]") {
		t.Fatalf("código precisa acompanhar a tradução: %q", msg)
	}
}

// Corpo sem code (servidor antigo / proxy) continua legível e sem prefixo.
func TestApiErrorMessage_SemCodigoNaoPrefixa(t *testing.T) {
	msg := apiErrorMessage(400, "{\"error\":\"campo inválido\"}")
	if strings.HasPrefix(msg, "[") {
		t.Fatalf("sem code não pode aparecer prefixo: %q", msg)
	}
}
