package app

import (
	"testing"
	"time"
)

// supportChatInviteAnswerWindow é a fronteira entre "resposta recente do
// usuário" (não reabre o cartão) e "resposta antiga que não chegou ao
// servidor" (a pergunta precisa aparecer de novo). Ela tem de ser menor que o
// TTL da marca — senão nunca existiria resposta "antiga" a reapresentar.
func TestSupportChatInviteAnswerWindow_MenorQueOTTl(t *testing.T) {
	if supportChatInviteAnswerWindow <= 0 {
		t.Fatal("a janela de resposta recente precisa ser positiva")
	}
	if supportChatInviteAnswerWindow >= supportChatInviteNotificationAnsweredTTL {
		t.Fatalf("janela (%s) precisa ser menor que o TTL da marca (%s)",
			supportChatInviteAnswerWindow, supportChatInviteNotificationAnsweredTTL)
	}
	if supportChatInviteAnswerWindow > 2*time.Minute {
		t.Fatalf("janela longa demais (%s): um convite pendente ficaria invisível", supportChatInviteAnswerWindow)
	}
}
