package app

import (
	"testing"
	"time"

	"discovery/app/supportchat"
)

// ── Rede de segurança do cartão: NÃO pode recusar sem clique ────────────────
//
// BUG ENCONTRADO NA REVISÃO: a rede de segurança era um timer de 4s armado na
// criação do cartão. Passados 4 segundos com o cartão na tela e SEM clique, ele
// era fechado e uma recusa automática era enviada ao servidor. No log real isso
// apareceu como 4 ciclos "fechando pela rede de seguranca" + "reapresentando",
// e era o sintoma "aparece a notificação mas a conversa não abre": o usuário
// perdia a pergunta antes de conseguir decidir.

// Um cartão SEM decisão nunca pode ser considerado "pronto para fechar".
func TestSupportChatNotifyCardDecided_SemClique(t *testing.T) {
	a := &App{}
	if a.supportChatNotifyCardDecided("sessao-sem-clique") {
		t.Fatal("cartão sem clique NÃO pode ser dado como decidido: a rede de segurança o fecharia e recusaria o convite sozinho")
	}

	// Mesmo com o cartão na tela e o tempo passando, continua sem decisão.
	a.supportChatNotifyMu.Lock()
	a.supportChatNotify = map[string]func(){"sessao-sem-clique": func() {}}
	a.supportChatNotifyMu.Unlock()
	time.Sleep(10 * time.Millisecond)
	if a.supportChatNotifyCardDecided("sessao-sem-clique") {
		t.Fatal("o tempo passando não transforma um cartão na tela em decisão")
	}
}

// Depois do clique a marca de "respondido" existe e a rede de segurança pode
// fechar o cartão que ficou preso na tela.
func TestSupportChatNotifyCardDecided_AposClique(t *testing.T) {
	a := &App{}
	a.markSupportChatInviteNotificationAnswered("sessao-respondida")
	if !a.supportChatNotifyCardDecided("sessao-respondida") {
		t.Fatal("após o clique a rede de segurança precisa poder fechar o cartão")
	}
}

// O prazo da rede de segurança só começa a contar a partir do CLIQUE: um cartão
// recém-criado não tem decisão e por isso não é fechado nem recusado.
func TestSupportChatInviteGuard_NaoRecusaSemInteracao(t *testing.T) {
	a := &App{}
	a.supportChatNotifyMu.Lock()
	a.supportChatNotify = make(map[string]func())
	a.supportChatNotifyMu.Unlock()

	// 1) cartão apresentado, sem clique
	a.markSupportChatInvitePresentation("sessao-aguardando")
	if a.supportChatNotifyCardDecided("sessao-aguardando") {
		t.Fatal("apresentar não é decidir")
	}

	// 2) o "waiting" continua válido e a superfície não marca recusa
	a.ensureSupportChatInviteSurface(supportchat.State{
		Status:    supportchat.StatusWaiting,
		SessionID: "sessao-aguardando",
	})
	if a.supportChatInviteGuardClosed("sessao-aguardando") {
		t.Fatal("sem interação não pode existir marca de cartão fechado pela rede de segurança")
	}
}

// O motivo "user_dismissed" continua reconhecido (compatibilidade com builds
// anteriores), mas NÃO é mais produzido pelo agent.
func TestSupportChatInviteGuardDeniedReason_Compatibilidade(t *testing.T) {
	if supportChatInviteGuardDeniedReason != "user_dismissed" {
		t.Fatalf("motivo inesperado: %q", supportChatInviteGuardDeniedReason)
	}
}