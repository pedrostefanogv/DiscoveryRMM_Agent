package app

import (
	"testing"
	"time"

	"discovery/app/supportchat"
)

// ── 3.4: fim do loop "fecha pela rede de segurança → reapresenta" ───────────
//
// RELATO (teste real): 4 ciclos seguidos de
//   "cartao do convite ainda na tela apos o prazo; fechando pela rede de seguranca"
//   "estado waiting sem cartao na tela; reapresentando"
// e cada fechamento era uma DECISÃO PERDIDA do usuário.

// O limite de reapresentação é pequeno e positivo.
func TestSupportChatInviteMaxPresentations_Limitado(t *testing.T) {
	if supportChatInviteMaxPresentations < 2 {
		t.Fatalf("o convite precisa poder ser reapresentado ao menos 1 vez; cap=%d", supportChatInviteMaxPresentations)
	}
	if supportChatInviteMaxPresentations > 2 {
		t.Fatalf("cap alto demais (%d): o ciclo do cartão volta a ser possível", supportChatInviteMaxPresentations)
	}
}

// A contagem de apresentações cresce a cada cartão mostrado; depois do cap o
// show() recusa reapresentar o MESMO convite.
func TestSupportChatInvitePresentationCap(t *testing.T) {
	a := &App{}
	for i := 1; i <= supportChatInviteMaxPresentations; i++ {
		a.markSupportChatInvitePresentation("sessao-loopy")
		if got := a.supportChatInvitePresentationCount("sessao-loopy"); got != i {
			t.Fatalf("contagem = %d; esperado %d", got, i)
		}
	}
	if a.supportChatInvitePresentationCount("outra-sessao") != 0 {
		t.Fatal("a contagem é por sessão")
	}
}

// ensureSupportChatInviteSurface não reapresenta um "waiting" cujo cartão já
// foi apresentado o número máximo de vezes (nenhum caminho — evento IPC, JS,
// reconciliação, verify — consegue reentrar no ciclo).
func TestEnsureSupportChatInviteSurface_NaoReapresentaAlemDoCap(t *testing.T) {
	a := &App{}
	for i := 0; i < supportChatInviteMaxPresentations; i++ {
		a.markSupportChatInvitePresentation("sessao-cap")
	}
	a.ensureSupportChatInviteSurface(supportchat.State{
		Status:    supportchat.StatusWaiting,
		SessionID: "sessao-cap",
	})
	a.supportChatNotifyMu.Lock()
	cards := len(a.supportChatNotify)
	a.supportChatNotifyMu.Unlock()
	if cards != 0 {
		t.Fatalf("não pode criar cartão depois do cap; cartões=%d", cards)
	}
}

// Depois de a REDE DE SEGURANÇA fechar o cartão, o "waiting" que sobra não é um
// pedido que o usuário esteja vendo: não pode ser reapresentado (era o que
// fechava/reabria o cartão em sequência).
func TestEnsureSupportChatInviteSurface_NaoReapresentaAposRedeDeSeguranca(t *testing.T) {
	a := &App{}
	a.markSupportChatInviteGuardClosed("sessao-guard")

	if !a.supportChatInviteGuardClosed("sessao-guard") {
		t.Fatal("pré-condição: a marca da rede de segurança precisa existir")
	}

	a.ensureSupportChatInviteSurface(supportchat.State{
		Status:    supportchat.StatusWaiting,
		SessionID: "sessao-guard",
	})
	a.supportChatNotifyMu.Lock()
	cards := len(a.supportChatNotify)
	a.supportChatNotifyMu.Unlock()
	if cards != 0 {
		t.Fatalf("estado waiting após a rede de segurança não pode virar cartão; cartões=%d", cards)
	}
}

// A marca da rede de segurança vence (o convite pode ser reenviado depois).
func TestSupportChatInviteGuardClosed_Expira(t *testing.T) {
	a := &App{}
	a.markSupportChatInviteGuardClosed("sessao-velha")
	a.supportChatNotifyMu.Lock()
	a.supportChatNotifyGuardClosed["sessao-velha"] = time.Now().Add(-supportChatInviteGuardCloseTTL - time.Second)
	a.supportChatNotifyMu.Unlock()

	if a.supportChatInviteGuardClosed("sessao-velha") {
		t.Fatal("marca vencida não pode bloquear a reapresentação para sempre")
	}
	// E a marca vencida é podada no próximo registro.
	a.markSupportChatInviteGuardClosed("sessao-nova")
	a.supportChatNotifyMu.Lock()
	_, stillThere := a.supportChatNotifyGuardClosed["sessao-velha"]
	a.supportChatNotifyMu.Unlock()
	if stillThere {
		t.Fatal("marcas vencidas precisam ser podadas")
	}
}

// A rede de segurança só fecha o cartão AINDA REGISTRADO para a sessão.
func TestSupportChatInviteGuard_SomenteCartaoRegistrado(t *testing.T) {
	a := &App{}
	if a.supportChatNotifyCardPending("sessao-sem-cartao") {
		t.Fatal("sem cartão registrado a rede de segurança não tem o que fechar")
	}
}
