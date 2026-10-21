//go:build windows

package app

import (
	"testing"
	"time"
)

// ── "Cartão agarrado" (teste de 20:14 no agent com companion) ───────────────
//
// O log mostrou "convite aceito ... e reportado ao servidor" e, no processo de
// UI, "decisao do cartao descartada: convite nao esta mais pendente" — o cartão
// já tinha fechado (o usuário viu "Abrindo o chat..."), mas a abertura da janela
// dependia de uma rechecagem que descartou a decisão. Estes testes fixam as
// garantias novas do cartão.

// Sem hwnd (harness) o envio ao servidor acontece e a decisão é publicada no
// canal exatamente uma vez.
func TestSupportChatNotifyState_PublicaDecisaoUmaVez(t *testing.T) {
	resultCh := make(chan supportChatInviteNotificationResult, 1)
	state := &supportChatNotifyState{
		invite:   inviteForStateTest(),
		resultCh: resultCh,
		focus:    supportChatNotifyNone,
	}

	sent := make(chan struct{})
	state.onDecided = func(supportChatInviteNotificationResult) error {
		close(sent)
		return nil
	}

	state.resolve(0, true)
	<-sent

	// Contrato NOVO (correção da corrida): o publicador EXCLUSIVO é o goroutine
	// do resolve — com o erro real do POST. O WM_DESTROY NÃO publica mais
	// (publishDecided foi removido: publicar ali mascarava falha transitória de
	// rede e pulava o retry da recusa). Segunda publicação é no-op.
	state.publishDecision(nil)

	var results []supportChatInviteNotificationResult
	timeout := time.After(2 * time.Second)
	for {
		select {
		case r, ok := <-resultCh:
			if !ok {
				if len(results) != 1 {
					t.Fatalf("decisão publicada %d vez(es); esperado 1", len(results))
				}
				if !results[0].Accepted {
					t.Fatal("a decisão publicada deveria ser de aceite")
				}
				return
			}
			results = append(results, r)
		case <-timeout:
			t.Fatalf("a decisão deveria ter sido publicada; obtidas %d", len(results))
		}
	}
}

// O fechamento do cartão NUNCA espera o callback/UI: mesmo com um callback
// travado, resolve() retorna na hora (o cartão não fica "agarrado").
func TestSupportChatNotifyState_NaoFicaPresoComCallbackTravado(t *testing.T) {
	resultCh := make(chan supportChatInviteNotificationResult, 1)
	state := &supportChatNotifyState{
		invite:   inviteForStateTest(),
		resultCh: resultCh,
		focus:    supportChatNotifyNone,
	}

	block := make(chan struct{})
	state.onDecided = func(supportChatInviteNotificationResult) error {
		<-block // simula POST travado/UI sem resposta
		return nil
	}

	start := time.Now()
	state.resolve(0, false)

	// O callback continua pendente, mas o fechamento não depende dele em nada
	// (ver resolve: fecha primeiro, reporta em goroutine).
	if supportChatInviteReportWait <= 0 {
		t.Fatal("o timeout do envio precisa ser positivo")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("resolve() não pode bloquear a thread da janela (demorou %s)", elapsed)
	}
	close(block)
}
