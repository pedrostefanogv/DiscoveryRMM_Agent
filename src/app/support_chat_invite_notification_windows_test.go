//go:build windows

package app

import (
	"testing"

	"discovery/app/core/winui"
)

// O cartão compacto precisa caber nos retângulos de projeto sem sobreposição.
func TestSupportChatNotifyLayoutFitsWindow(t *testing.T) {
	logo, title, subtitle, divider, messageBox, messageText, open, dismiss, hint := supportChatNotifyLayout()

	rects := map[string]winui.Rect{
		"logo": logo, "title": title, "subtitle": subtitle, "divider": divider,
		"messageBox": messageBox, "messageText": messageText,
		"open": open, "dismiss": dismiss, "hint": hint,
	}
	for name, rect := range rects {
		if rect.Empty() {
			t.Fatalf("%s vazio", name)
		}
		if rect.X < 0 || rect.Y < 0 || rect.Right() > supportChatNotifyWidth || rect.Bottom() > supportChatNotifyHeight {
			t.Fatalf("%s fora do cartão: %+v", name, rect)
		}
	}
	if open.Right() >= dismiss.X {
		t.Fatal("botões Abrir/Agora não sobrepostos")
	}
	if open.Y <= messageBox.Bottom() {
		t.Fatal("botões em cima da caixa de mensagem")
	}
	if hint.Right() >= open.X {
		t.Fatal("dica de teclado invade os botões")
	}
	if messageText.X <= messageBox.X || messageText.Y <= messageBox.Y ||
		messageText.Right() >= messageBox.Right() || messageText.Bottom() >= messageBox.Bottom() {
		t.Fatal("texto da mensagem sem padding dentro da caixa")
	}
}

// Textos longos (atendente + chamado) crescem a caixa e empurram os botões.
func TestSupportChatNotifyLayoutGrowsForLongMessage(t *testing.T) {
	_, _, _, divider, messageBox, _, open, dismiss, hint := supportChatNotifyLayoutFor(1, 120)
	if messageBox.H != 84+120 {
		t.Fatalf("altura da caixa = %d", messageBox.H)
	}
	if open.Y != 196+120 || dismiss.Y != 196+120 || hint.Y != 206+120 {
		t.Fatalf("botões/dica não foram empurrados: open=%+v dismiss=%+v hint=%+v", open, dismiss, hint)
	}
	if open.Y < divider.Y+120 {
		t.Fatal("botões subiram para cima do divisor")
	}
}

func TestSupportChatNotifyButtonAt(t *testing.T) {
	state := &supportChatNotifyState{}
	_, _, _, _, _, _, open, dismiss, _ := state.layout()

	if got := state.buttonAt(open.X+2, open.Y+2); got != supportChatNotifyOpen {
		t.Fatalf("botão Abrir = %v", got)
	}
	if got := state.buttonAt(dismiss.X+2, dismiss.Y+2); got != supportChatNotifyDismiss {
		t.Fatalf("botão Agora não = %v", got)
	}
	if got := state.buttonAt(supportChatNotifyWidth/2, 20); got != supportChatNotifyNone {
		t.Fatalf("cabeçalho deveria ser área sem botão, veio %v", got)
	}
}

// Regra central: SEM ação padrão. Enter/Espaço sem foco não decide nada; Tab
// passa a focar os botões explicitamente e Esc recusa.
func TestSupportChatNotifyNoDefaultAction(t *testing.T) {
	newState := func() *supportChatNotifyState {
		return &supportChatNotifyState{
			resultCh: make(chan supportChatInviteNotificationResult, 1),
			focus:    supportChatNotifyNone,
		}
	}

	state := newState()
	state.handleKey(0, winui.VkReturn)
	state.handleKey(0, winui.VkSpace)
	select {
	case res := <-state.resultCh:
		t.Fatalf("Enter/Espaço sem foco decidiu sozinho: %+v", res)
	default:
	}

	// Tab foca "Abrir chat"; Enter confirma apenas o botão focado.
	state.handleKey(0, winui.VkTab)
	if state.focus != supportChatNotifyOpen {
		t.Fatalf("foco após Tab = %v", state.focus)
	}
	state.handleKey(0, winui.VkReturn)
	res := <-state.resultCh
	if !res.Accepted {
		t.Fatalf("Enter no botão focado deveria aceitar: %+v", res)
	}
}

func TestSupportChatNotifyEscapeDeclines(t *testing.T) {
	state := &supportChatNotifyState{
		resultCh: make(chan supportChatInviteNotificationResult, 1),
		focus:    supportChatNotifyNone,
	}
	state.handleKey(0, winui.VkEscape)
	res := <-state.resultCh
	if res.Accepted {
		t.Fatalf("Esc deveria recusar: %+v", res)
	}
}
