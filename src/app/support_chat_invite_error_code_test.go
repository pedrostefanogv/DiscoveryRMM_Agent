package app

import (
	"errors"
	"testing"
)

// ── 409 do servidor: decisão pelo CÓDIGO, não pelo texto ────────────────────
//
// BUG ENCONTRADO NA REVISÃO: apiErrorMessage traduzia a mensagem para o usuário
// final e DESCARTava o `code`. O agent então reconhecia "convite já resolvido"
// procurando frases em português ("a conversa não está mais ativa."). Qualquer
// ajuste de texto no servidor fazia o agent retentar uma recusa já aplicada.

// O caminho por código funciona mesmo com a mensagem em outro idioma.
func TestIsSupportChatInviteAlreadyResolved_PorCodigoComTextoDiferente(t *testing.T) {
	for _, code := range []string{"session_declined", "session_ended", "session_expired"} {
		msg := "[" + code + "] conversation is no longer pending"
		if !isSupportChatInviteAlreadyResolved(errors.New(msg)) {
			t.Fatalf("código %q precisa ser reconhecido como já resolvido; mensagem: %q", code, msg)
		}
	}
}

// Compatibilidade: builds antigas do servidor não prefixavam o código — o
// fallback por texto continua valendo.
func TestIsSupportChatInviteAlreadyResolved_FallbackTexto(t *testing.T) {
	if !isSupportChatInviteAlreadyResolved(errors.New("a conversa não está mais ativa.")) {
		t.Fatal("servidor antigo (sem code) precisa continuar reconhecido pelo texto")
	}
}

// Um erro de rede qualquer NÃO pode ser tratado como resolvido (senão a recusa
// seria descartada em silêncio).
func TestIsSupportChatInviteAlreadyResolved_NaoConfundeErroDeRede(t *testing.T) {
	if isSupportChatInviteAlreadyResolved(errors.New("dial tcp 10.0.0.1:443: connect: connection refused")) {
		t.Fatal("erro de rede não é convite resolvido: precisa retentar")
	}
}