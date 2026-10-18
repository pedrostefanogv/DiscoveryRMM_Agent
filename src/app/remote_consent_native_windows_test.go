//go:build windows

package app

import (
	"context"
	"testing"
	"time"

	"discovery/app/core/remotesession"
	"discovery/app/core/winui"
)

// O layout é fixo (coordenadas do desenho): garante que os blocos fiquem dentro
// do cartão, sem sobreposição entre contagem, barra, dica e botões.
func TestConsentLayoutFitsWindow(t *testing.T) {
	logo, title, subtitle, divider, messageBox, messageText, countLabel, countValue,
		defaultAction, progressTrack, allow, deny, hint := consentLayout()

	rects := map[string]winui.Rect{
		"logo": logo, "title": title, "subtitle": subtitle, "divider": divider,
		"messageBox": messageBox, "messageText": messageText, "countLabel": countLabel,
		"countValue": countValue, "defaultAction": defaultAction,
		"progressTrack": progressTrack, "allow": allow, "deny": deny, "hint": hint,
	}
	for name, rect := range rects {
		if rect.Empty() {
			t.Fatalf("%s vazio", name)
		}
		if rect.X < 0 || rect.Y < 0 || rect.Right() > consentWidth || rect.Bottom() > consentHeight {
			t.Fatalf("%s fora do cartão: %+v", name, rect)
		}
	}
	if allow.Right() >= deny.X {
		t.Fatal("botões Permitir/Negar sobrepostos")
	}
	if allow.Y <= progressTrack.Bottom() {
		t.Fatal("botões em cima da barra de progresso")
	}
	if hint.Right() >= allow.X {
		t.Fatal("dica de teclado invade os botões")
	}
	if messageText.X <= messageBox.X || messageText.Y <= messageBox.Y ||
		messageText.Right() >= messageBox.Right() || messageText.Bottom() >= messageBox.Bottom() {
		t.Fatal("texto da mensagem sem padding dentro da caixa")
	}
}

func TestConsentLabelDefaults(t *testing.T) {
	state := &consentNativeState{}
	if got := state.titleText(); got != "Autorização de acesso remoto" {
		t.Fatalf("título default = %q", got)
	}
	if got := state.messageText(); got == "" {
		t.Fatal("mensagem default vazia")
	}
	if state.timeoutApproves() {
		t.Fatal("timeoutAction vazio deveria ser fail closed (negar)")
	}
}

func TestConsentTimeoutDecisionFollowsPolicy(t *testing.T) {
	deniedByTimeout := consentTimeoutDecision(remotesession.ConsentRequest{TimeoutAction: "deny"})
	if deniedByTimeout.Accepted || !deniedByTimeout.Auto || deniedByTimeout.Outcome != "timeout-denied" || deniedByTimeout.Reason != "timeout" {
		t.Fatalf("timeout deny inesperado: %+v", deniedByTimeout)
	}
	if deniedByTimeout.PromptBackend != "native" {
		t.Fatalf("backend = %q", deniedByTimeout.PromptBackend)
	}
	approved := consentTimeoutDecision(remotesession.ConsentRequest{TimeoutAction: "APPROVE"})
	if !approved.Accepted || approved.Outcome != "timeout-granted" {
		t.Fatalf("timeout approve inesperado: %+v", approved)
	}
	// Política desconhecida/vazia é fail closed.
	if consentTimeoutDecision(remotesession.ConsentRequest{}).Accepted {
		t.Fatal("timeoutAction vazio deveria negar")
	}
}

func TestConsentDeniedAndStoppedDecisions(t *testing.T) {
	denied := consentDeniedDecision("user_denied")
	if denied.Accepted || denied.Auto || denied.Outcome != "denied" || denied.Reason != "user_denied" {
		t.Fatalf("denied inesperado: %+v", denied)
	}
	stopped := consentStoppedDecision()
	if stopped.Accepted || !stopped.Auto || stopped.Outcome != "stopped-by-server" {
		t.Fatalf("stopped inesperado: %+v", stopped)
	}
}

// cliqueNoBotao posta um clique nas coordenadas do centro do retângulo.
func cliqueNoBotao(hwnd uintptr, rect winui.Rect) {
	x := uintptr(uint16(rect.X + rect.W/2))
	y := uintptr(uint16(rect.Y + rect.H/2))
	winui.PostMessage(hwnd, winui.MsgLButtonDown, 0, y<<16|x)
	winui.PostMessage(hwnd, winui.MsgLButtonUp, 0, y<<16|x)
}

// esperaJanelaDoPrompt espera o HWND do prompt aparecer (skip sem desktop).
func esperaJanelaDoPrompt(t *testing.T) uintptr {
	t.Helper()
	var hwnd uintptr
	for i := 0; i < 300 && hwnd == 0; i++ {
		time.Sleep(10 * time.Millisecond)
		consentStates.Range(func(key, _ any) bool {
			hwnd = key.(uintptr)
			return false
		})
	}
	if hwnd == 0 {
		t.Skip("sem desktop interativo para exibir o prompt")
	}
	return hwnd
}

func TestNativeConsentPromptAllowClick(t *testing.T) {
	consentThemeLoader = winui.LightTheme
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type result struct {
		decision remotesession.ConsentDecision
		ok       bool
	}
	resultCh := make(chan result, 1)
	go func() {
		decision, ok := nativeConsentPrompt(ctx, remotesession.ConsentRequest{
			Title: "Autorização de acesso remoto", Message: "Teste", Timeout: 10 * time.Second, TimeoutAction: "deny",
		})
		resultCh <- result{decision: decision, ok: ok}
	}()
	hwnd := esperaJanelaDoPrompt(t)
	_, _, _, _, _, _, _, _, _, _, allow, _, _ := consentLayout()
	cliqueNoBotao(hwnd, allow)
	select {
	case got := <-resultCh:
		if !got.ok || !got.decision.Accepted || got.decision.Outcome != "granted" || got.decision.Reason != "user_allowed" {
			t.Fatalf("clique em Permitir = %+v", got.decision)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("prompt não respondeu ao clique em Permitir")
	}
}

func TestNativeConsentPromptDenyClick(t *testing.T) {
	consentThemeLoader = winui.DarkTheme
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type result struct {
		decision remotesession.ConsentDecision
		ok       bool
	}
	resultCh := make(chan result, 1)
	go func() {
		decision, ok := nativeConsentPrompt(ctx, remotesession.ConsentRequest{
			Title: "Autorização de acesso remoto", Message: "Teste", Timeout: 10 * time.Second, TimeoutAction: "approve",
		})
		resultCh <- result{decision: decision, ok: ok}
	}()
	hwnd := esperaJanelaDoPrompt(t)
	_, _, _, _, _, _, _, _, _, _, _, deny, _ := consentLayout()
	cliqueNoBotao(hwnd, deny)
	select {
	case got := <-resultCh:
		if !got.ok || got.decision.Accepted || got.decision.Outcome != "denied" || got.decision.Reason != "user_denied" {
			t.Fatalf("clique em Negar = %+v", got.decision)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("prompt não respondeu ao clique em Negar")
	}
}
