package app

import (
	"testing"
	"time"
)

// ── Revisão por bugs (2026-10-19) ────────────────────────────────────────────
//
// Os ajustes do fluxo do convite/cartão escondiam alguns casos de borda; estes
// testes os fixam.

// supportChatInviteRecentlyAnswered separa "resposta recente do usuário, ainda
// indo ao servidor" de "resposta antiga que não chegou" — a segunda precisa
// permitir que o convite pendente seja reapresentado.
func TestSupportChatInviteRecentlyAnswered(t *testing.T) {
	a := &App{}

	if a.supportChatInviteRecentlyAnswered("sessao-1") {
		t.Fatal("sem marca de resposta não pode haver resposta recente")
	}

	a.markSupportChatInviteNotificationAnswered("sessao-1")
	if !a.supportChatInviteRecentlyAnswered("sessao-1") {
		t.Fatal("marca recém-criada é resposta recente")
	}

	// Resposta antiga (dentro do TTL, mas fora da janela de redelivery).
	a.supportChatNotifyMu.Lock()
	a.supportChatNotifyAnswered["sessao-1"] = time.Now().Add(supportChatInviteNotificationAnsweredTTL - supportChatInviteAnswerWindow - time.Second)
	a.supportChatNotifyMu.Unlock()

	if a.supportChatInviteRecentlyAnswered("sessao-1") {
		t.Fatal("resposta fora da janela não é recente")
	}

	// clearStaleSupportChatInviteAnswer libera a marca para a nova pergunta.
	if !a.clearStaleSupportChatInviteAnswer("sessao-1") {
		t.Fatal("a marca antiga deveria ser limpa")
	}
	if a.supportChatInviteNotificationWasAnswered("sessao-1") {
		t.Fatal("a marca antiga não pode mais suprimir o convite")
	}
}

// Uma resposta RECENTE continua protegida: limpar a marca no meio do POST
// reabriria o cartão de um convite que o usuário acabou de responder.
func TestClearStaleSupportChatInviteAnswer_PreservaRespostaRecente(t *testing.T) {
	a := &App{}
	a.markSupportChatInviteNotificationAnswered("sessao-2")

	if a.clearStaleSupportChatInviteAnswer("sessao-2") {
		t.Fatal("resposta recente não pode ser limpa")
	}
	if !a.supportChatInviteNotificationWasAnswered("sessao-2") {
		t.Fatal("a marca recente precisa continuar valendo")
	}
}
