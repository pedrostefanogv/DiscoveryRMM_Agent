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

// O ACEITE também fecha o cartão na hora (mesmo comportamento de "Agora não"):
// o usuário não deve ver "Abrindo o chat de suporte…" no cartão — quem abre a
// janela é o processo de UI.
func TestSupportChatNotifyState_AceiteFechaNaHora(t *testing.T) {
	resultCh := make(chan supportChatInviteNotificationResult, 1)
	state := &supportChatNotifyState{
		invite:   inviteForStateTest(),
		resultCh: resultCh,
		focus:    supportChatNotifyNone,
	}

	release := make(chan struct{})
	state.onDecided = func(supportChatInviteNotificationResult) error {
		<-release // POST lento: não pode segurar o cartão
		return nil
	}

	start := time.Now()
	state.resolve(0, true)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("resolve() do aceite não pode bloquear (demorou %s)", elapsed)
	}
	if !state.done {
		t.Fatal("o cartão deveria estar respondido imediatamente")
	}
	close(release)
}
