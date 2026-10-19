//go:build windows

package app

import (
	"context"
	"strings"
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

// Com os cantos do DWM a janela continua retangular para o mouse: só a área
// visível do cartão pode capturar cliques (o resto vira HTTRANSPARENT).
func TestConsentInsideRoundedCard(t *testing.T) {
	cases := []struct {
		name string
		x, y int
		want bool
	}{
		{"centro", consentWidth / 2, consentHeight / 2, true},
		{"canto superior esquerdo cortado", 0, 0, false},
		{"quase canto superior esquerdo cortado", 1, 1, false},
		{"interior do circulo do canto", 4, 4, true},
		{"borda esquerda no meio", 0, consentHeight / 2, true},
		{"borda direita no meio", consentWidth - 1, consentHeight / 2, true},
		{"canto inferior direito cortado", consentWidth - 1, consentHeight - 1, false},
		{"centro do circulo inferior direito", consentWidth - consentSystemCornerRadius, consentHeight - consentSystemCornerRadius, true},
		{"fora a esquerda", -1, consentHeight / 2, false},
		{"fora a direita", consentWidth, consentHeight / 2, false},
		{"fora acima", consentWidth / 2, -1, false},
		{"fora abaixo", consentWidth / 2, consentHeight, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := consentInsideRoundedCard(tc.x, tc.y); got != tc.want {
				t.Fatalf("consentInsideRoundedCard(%d,%d) = %v, want %v", tc.x, tc.y, got, tc.want)
			}
		})
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

// layoutDoPrompt devolve o layout efetivo (escalado por DPI e com a altura
// extra da mensagem) do prompt em exibição.
func layoutDoPrompt(t *testing.T, hwnd uintptr) (logo, title, subtitle, divider, messageBox, messageText,
	countLabel, countValue, defaultAction, progressTrack, allow, deny, hint winui.Rect) {
	t.Helper()
	raw, ok := consentStates.Load(hwnd)
	if !ok {
		t.Fatal("estado do prompt não encontrado")
	}
	state, ok := raw.(*consentNativeState)
	if !ok {
		t.Fatal("estado do prompt com tipo inesperado")
	}
	return state.layout()
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

// TestConsentLayoutAtScaleAndExtra cobre a escala por DPI e o crescimento da
// caixa de mensagem para textos longos: tudo precisa continuar dentro do cartão
// e sem sobreposição.
func TestConsentLayoutAtScaleAndExtra(t *testing.T) {
	const scale = 1.5
	const extra = 60
	scaledWidth := winui.ScalePx(consentWidth, scale)
	scaledHeight := winui.ScalePx(consentHeight, scale) + extra

	logo, title, subtitle, divider, messageBox, messageText, countLabel, countValue,
		defaultAction, progressTrack, allow, deny, hint := consentLayoutFor(scale, extra)

	rects := map[string]winui.Rect{
		"logo": logo, "title": title, "subtitle": subtitle, "divider": divider,
		"messageBox": messageBox, "messageText": messageText, "countLabel": countLabel,
		"countValue": countValue, "defaultAction": defaultAction,
		"progressTrack": progressTrack, "allow": allow, "deny": deny, "hint": hint,
	}
	for name, rect := range rects {
		if rect.Empty() {
			t.Fatalf("%s vazio no layout escalado", name)
		}
		if rect.X < 0 || rect.Y < 0 || rect.Right() > scaledWidth || rect.Bottom() > scaledHeight {
			t.Fatalf("%s fora do cartão escalado: %+v (cartão %dx%d)", name, rect, scaledWidth, scaledHeight)
		}
	}
	if messageBox.H != winui.ScalePx(84, scale)+extra {
		t.Fatalf("messageBox.H = %d, esperado %d", messageBox.H, winui.ScalePx(84, scale)+extra)
	}
	if allow.Right() >= deny.X {
		t.Fatal("botões sobrepostos no layout escalado")
	}
	if allow.Y <= progressTrack.Bottom() {
		t.Fatal("botões em cima da barra no layout escalado")
	}
	if hint.Right() >= allow.X {
		t.Fatal("dica invade os botões no layout escalado")
	}
	if messageText.X <= messageBox.X || messageText.Right() >= messageBox.Right() ||
		messageText.Y <= messageBox.Y || messageText.Bottom() >= messageBox.Bottom() {
		t.Fatal("texto da mensagem sem padding no layout escalado")
	}
}

// TestConsentInsideRoundedCardScaled cobre o hit-test dos cantos com o raio do
// DWM escalado (o raio de projeto é 8px; a 150% vira 12px físicos).
func TestConsentInsideRoundedCardScaled(t *testing.T) {
	width := winui.ScalePx(consentWidth, 1.5)
	height := winui.ScalePx(consentHeight, 1.5)
	corner := winui.ScalePx(consentSystemCornerRadius, 1.5)

	if !consentInsideRoundedCardAt(width/2, height/2, width, height, corner) {
		t.Fatal("centro deveria ser clicável")
	}
	if consentInsideRoundedCardAt(width-1, height-1, width, height, corner) {
		t.Fatal("canto inferior direito deveria ser transparente")
	}
	if !consentInsideRoundedCardAt(corner, corner, width, height, corner) {
		t.Fatal("interior do arco do canto deveria ser clicável")
	}
	if !consentInsideRoundedCardAt(0, height/2, width, height, corner) {
		t.Fatal("borda esquerda no meio deveria ser clicável")
	}
	if consentInsideRoundedCardAt(width, height/2, width, height, corner) {
		t.Fatal("ponto fora da direita deveria ser transparente")
	}
}

// TestNativeConsentPromptLongMessageGrows verifica de ponta a ponta que uma
// mensagem longa (perto do limite de 500 caracteres do servidor) faz o prompt
// crescer o suficiente para não cortar o texto.
func TestNativeConsentPromptLongMessageGrows(t *testing.T) {
	consentThemeLoader = winui.LightTheme
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	longMessage := strings.Repeat("O técnico precisa acessar esta máquina para resolver o chamado. ", 7)
	if len(longMessage) > 500 {
		longMessage = longMessage[:500]
	}
	type result struct {
		decision remotesession.ConsentDecision
		ok       bool
	}
	resultCh := make(chan result, 1)
	go func() {
		decision, ok := nativeConsentPrompt(ctx, remotesession.ConsentRequest{
			Title: "Autorização de acesso remoto", Message: longMessage,
			Timeout: 10 * time.Second, TimeoutAction: "deny",
		})
		resultCh <- result{decision: decision, ok: ok}
	}()

	hwnd := esperaJanelaDoPrompt(t)
	raw, _ := consentStates.Load(hwnd)
	state, _ := raw.(*consentNativeState)
	if state == nil {
		t.Fatal("estado do prompt não encontrado")
	}
	width, height, _, _ := state.metrics()
	if width != winui.ScalePx(consentWidth, state.scale) {
		t.Fatalf("largura = %d, esperado %d", width, winui.ScalePx(consentWidth, state.scale))
	}
	if state.extraHeight <= 0 || height <= winui.ScalePx(consentHeight, state.scale) {
		t.Fatalf("mensagem longa não cresceu o prompt (extra=%d height=%d)", state.extraHeight, height)
	}
	_, _, _, _, messageBox, messageText, _, _, _, _, _, deny, _ := state.layout()
	if messageText.Bottom() > messageBox.Bottom() {
		t.Fatalf("texto da mensagem maior que a caixa: texto=%+v caixa=%+v", messageText, messageBox)
	}
	cliqueNoBotao(hwnd, deny)
	select {
	case got := <-resultCh:
		if !got.ok || got.decision.Accepted {
			t.Fatalf("clique em Negar (mensagem longa) = %+v", got.decision)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("prompt (mensagem longa) não respondeu ao clique em Negar")
	}
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
	_, _, _, _, _, _, _, _, _, _, allow, _, _ := layoutDoPrompt(t, hwnd)
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
	_, _, _, _, _, _, _, _, _, _, _, deny, _ := layoutDoPrompt(t, hwnd)
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
