package app

import (
	"testing"
	"time"

	"discovery/app/supportchat"
)

// A rede de segurança do cartão só age em cartão REGISTRADO (aberto). Depois de
// fechado/cancelado o mapa não tem a sessão e nada mais é feito.
func TestSupportChatNotifyCardPending(t *testing.T) {
	a := &App{}
	if a.supportChatNotifyCardPending("sessao-x") {
		t.Fatal("sem cartão registrado não pode estar pendente")
	}

	cancelled := false
	a.supportChatNotifyMu.Lock()
	if a.supportChatNotify == nil {
		a.supportChatNotify = map[string]func(){}
	}
	a.supportChatNotify["sessao-x"] = func() { cancelled = true }
	a.supportChatNotifyMu.Unlock()

	if !a.supportChatNotifyCardPending("sessao-x") {
		t.Fatal("cartão registrado deveria estar pendente")
	}

	a.closeSupportChatInviteNotification("sessao-x")
	if !cancelled {
		t.Fatal("fechar o cartão precisa acionar o cancelador (fecha a janela nativa)")
	}
	if a.supportChatNotifyCardPending("sessao-x") {
		t.Fatal("após fechar, o cartão não pode mais estar pendente")
	}
}

// O prazo da rede de segurança precisa ser curto: o usuário respondeu ao
// cartão e não pode esperar mais que poucos segundos pela saída da tela.
func TestSupportChatInviteCardCloseGuard_Curto(t *testing.T) {
	if supportChatInviteCardCloseGuard <= 0 || supportChatInviteCardCloseGuard > 10*time.Second {
		t.Fatalf("prazo da rede de segurança fora do razoável: %s", supportChatInviteCardCloseGuard)
	}
	if supportChatInviteCardCloseGuard > supportChatInviteReportWait {
		t.Fatalf("a rede de segurança (%s) não pode ser maior que o timeout do envio (%s)",
			supportChatInviteCardCloseGuard, supportChatInviteReportWait)
	}
}

// Sanidade do estado de convite usado pela decisão do cartão.
func TestSupportChatInviteDecisionState_RecusaNaoAbreJanela(t *testing.T) {
	if supportChatWindowMayOpen(supportchat.StatusWaiting) {
		t.Fatal("recusa (waiting antes de limpar) não pode abrir a janela")
	}
	if !supportChatWindowMayOpen(supportchat.StatusActive) {
		t.Fatal("aceite (active) pode abrir a janela")
	}
}
