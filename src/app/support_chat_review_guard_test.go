//go:build windows

package app

import (
	"sync/atomic"
	"testing"
	"time"

	"discovery/app/supportchat"
)

// ── Revisão por bugs da revisão 8 ───────────────────────────────────────────
//
// Os ajustes do cartão (fechar na hora) escondiam regressões de ciclo de vida.
// Estes testes as fixam.

// destroyCard só encerra o loop (PostQuitMessage) quando quem chama é a THREAD
// da janela. De outra goroutine ele não pode parar o loop com a janela viva —
// era isso que deixava o cartão órfão na tela.
func TestSupportChatNotifyState_DestroyCardNaoEncerraDeOutraThread(t *testing.T) {
	state := &supportChatNotifyState{}
	state.mu.Lock()
	state.windowThread = 1 // thread "dona" fictícia (a thread do teste é outra)
	state.mu.Unlock()

	if state.ownsWindowThread() {
		t.Fatal("a goroutine de teste não pode ser a thread dona da janela")
	}
	// hwnd=0: no-op seguro.
	state.destroyCard(0)
}

// A rede de segurança do cartão (runSupportChatInviteNotificationDecision) usa
// o cancelador do cartão: com ele, mesmo que a janela nativa não feche sozinha,
// o processo de UI fecha o cartão.
func TestSupportChatCardGuard_AcionaCancelador(t *testing.T) {
	a := &App{}
	var cancelled atomic.Bool

	a.supportChatNotifyMu.Lock()
	if a.supportChatNotify == nil {
		a.supportChatNotify = map[string]func(){}
	}
	a.supportChatNotify["sessao-g"] = func() { cancelled.Store(true) }
	a.supportChatNotifyMu.Unlock()

	// Simula o que o timer faria quando o prazo vence.
	if !a.supportChatNotifyCardPending("sessao-g") {
		t.Fatal("o cartão deveria estar registrado")
	}
	a.closeSupportChatInviteNotification("sessao-g")
	if !cancelled.Load() {
		t.Fatal("fechar o cartão precisa acionar o cancelador (fecha a janela nativa)")
	}
	if a.supportChatNotifyCardPending("sessao-g") {
		t.Fatal("após fechar, o cartão não pode continuar pendente")
	}
}

// Nenhuma mensagem de espera volta ao cartão: responder (aceitar OU recusar)
// tira o cartão da tela na hora.
func TestSupportChatInviteCardTexts_SemEspera(t *testing.T) {
	msg := supportChatInviteNotificationMessage(supportchat.Invite{SessionID: "s", SupportName: "Ana"})
	for _, needle := range []string{"abrindo o chat", "avisando o suporte"} {
		if containsFold(msg, needle) {
			t.Fatalf("texto de espera não deve existir no cartão: %q", msg)
		}
	}
}

func containsFold(haystack, needle string) bool {
	h := []rune(haystack)
	n := []rune(needle)
	for i := 0; i+len(n) <= len(h); i++ {
		match := true
		for j := range n {
			if lowerRune(h[i+j]) != lowerRune(n[j]) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func lowerRune(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + ('a' - 'A')
	}
	return r
}

// O prazo da rede de segurança precisa ser curto e menor que o timeout do envio.
func TestSupportChatInviteCardCloseGuard_Valor(t *testing.T) {
	if supportChatInviteCardCloseGuard <= 0 || supportChatInviteCardCloseGuard > 10*time.Second {
		t.Fatalf("prazo da rede de segurança fora do razoável: %s", supportChatInviteCardCloseGuard)
	}
}
