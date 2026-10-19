//go:build windows

package app

import (
	"sync/atomic"
	"testing"
	"time"
)

// RELATO: "quando clico em fechar [Agora não] ele fica avisando o suporte… e não
// fecha". A recusa é ação de SAÍDA: o cartão precisa sumir NA HORA, sem estado
// de espera, e o envio ao servidor acontece em paralelo (best-effort).
func TestSupportChatNotifyState_RecusaFechaNaHora(t *testing.T) {
	resultCh := make(chan supportChatInviteNotificationResult, 1)
	state := &supportChatNotifyState{
		invite:   inviteForStateTest(),
		resultCh: resultCh,
		focus:    supportChatNotifyNone,
	}

	var sent atomic.Bool
	release := make(chan struct{})
	state.onDecided = func(supportChatInviteNotificationResult) error {
		sent.Store(true)
		<-release // POST lento: não pode segurar o cartão
		return nil
	}

	start := time.Now()
	state.resolve(0, false)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("resolve() de recusa não pode bloquear (demorou %s)", elapsed)
	}

	// O cartão deixa de aceitar input imediatamente e não mostra "Avisando…".
	deadline := time.Now().Add(time.Second)
	for !sent.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !sent.Load() {
		t.Fatal("o envio da recusa deveria começar em paralelo")
	}
	close(release)

	select {
	case result := <-resultCh:
		if result.Accepted {
			t.Fatal("resultado deveria ser recusa")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a recusa deveria chegar ao processo de UI")
	}
}

// O cartão de recusa NÃO deve desenhar o texto de espera (só o aceite espera).
func TestSupportChatNotifyState_TextoDeEsperaSoNoAceite(t *testing.T) {
	// Guarda de contrato dos textos usados pelo cartão.
	if got := supportChatInviteNotificationRespondingMessage(true); got != "Abrindo o chat de suporte…" {
		t.Fatalf("texto de espera do aceite inesperado: %q", got)
	}
}
