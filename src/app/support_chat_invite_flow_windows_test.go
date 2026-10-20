//go:build windows

package app

import (
	"testing"
	"time"

	"discovery/app/supportchat"
)

// inviteForStateTest monta um convite mínimo para os testes do cartão.
func inviteForStateTest() supportchat.Invite {
	return supportchat.Invite{SessionID: "sessao-windows-1", SupportName: "Ana"}
}

// O cartão do convite só pode sair da tela DEPOIS de a decisão chegar ao
// servidor.
//
// BUG RELATADO: "cliquei em Agora não e não aparece para o suporte no chat pelo
// navegador". Antes, resolve() destruía a janela e publicava a escolha num
// canal lido por uma goroutine que só então fazia o POST — entre o clique e o
// envio restava uma janela em que o cartão já não existia e o convite continuava
// "Aguardando confirmação" no console.
func TestSupportChatNotifyState_ReportaAntesDeFechar(t *testing.T) {
	resultCh := make(chan supportChatInviteNotificationResult, 1)
	state := &supportChatNotifyState{
		invite:   inviteForStateTest(),
		resultCh: resultCh,
		focus:    supportChatNotifyNone,
	}

	called := make(chan struct{})
	state.onDecided = func(result supportChatInviteNotificationResult) error {
		// Neste ponto o cartão já registrou a decisão (done) e está saindo da
		// tela: o envio roda em paralelo ao fechamento.
		state.mu.Lock()
		decided := state.done
		state.mu.Unlock()
		if !decided {
			t.Error("a decisão deveria estar registrada antes do envio")
		}
		if result.Accepted {
			t.Error("o cartão deveria ter recusado")
		}
		close(called)
		return nil
	}

	// hwnd=0: sem janela nativa neste teste (estamos no processo de teste).
	state.resolve(0, false)

	<-called

	// O canal é publicado em goroutine (o envio acontece em paralelo ao
	// fechamento): aguarda sem assumir que é síncrono.
	select {
	case result := <-resultCh:
		if result.Accepted {
			t.Fatal("resultado deveria ser recusa")
		}
		if result.SessionID != "sessao-windows-1" {
			t.Fatalf("sessão inesperada: %q", result.SessionID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a decisão deveria ter sido publicada no canal após o envio")
	}
}

// Decide uma vez só: um segundo clique (ou Esc depois do clique) não pode
// gerar um segundo envio ao servidor.
func TestSupportChatNotifyState_DecisaoUnica(t *testing.T) {
	resultCh := make(chan supportChatInviteNotificationResult, 1)
	state := &supportChatNotifyState{
		invite:   inviteForStateTest(),
		resultCh: resultCh,
		focus:    supportChatNotifyNone,
	}

	calls := make(chan struct{}, 4)
	state.onDecided = func(supportChatInviteNotificationResult) error {
		calls <- struct{}{}
		return nil
	}

	state.resolve(0, false)
	state.resolve(0, true)
	state.resolve(0, false)

	<-calls
	select {
	case <-calls:
		t.Fatal("a decisão foi reportada mais de uma vez")
	default:
	}
}
