package app

import (
	"strings"
	"testing"
)

// ── Revisão dos bugs INTRODUZIDOS pelas correções de abertura ──────────────
//
// Estes testes existem para que cada defeito achado na revisão crítica não
// volte silenciosamente.

// BUG 1: a flag de PINTURA é por janela, mas é um atomic global. Sem reset na
// criação, a janela NOVA herdava "pintou" da anterior e o reuso tratava um
// webview ainda vazio como pronto (chat numa janela branca).
func TestSupportChatWindow_PinturaResetaNaCriacao(t *testing.T) {
	a := &App{}
	a.supportChatWinPaints.Store(true)

	a.markSupportChatWindowContentPending()

	if a.supportChatWinPaints.Load() {
		t.Fatal("janela recém-criada não pode herdar 'já pintou' da janela anterior")
	}
	if a.supportChatWindowContentReady.Load() {
		t.Fatal("janela recém-criada não pode herdar 'conteúdo pronto'")
	}
}

// BUG 2: hideSupportChatInviteSurface desregistrava a janela e registrava de
// novo. Entre as duas operações supportChatWindowOpen() respondia false com a
// janela VIVA na tela — a reconciliação de 30 s podia abrir uma segunda janela.
// O contrato agora é: com a janela pintada, ela nunca sai do registro.
func TestSupportChatHideInviteSurface_NaoDesregistraJanelaPintada(t *testing.T) {
	a := newSupportChatTestApp(t, "http://127.0.0.1:1")
	win := &fakeChatWindow{}
	a.supportChatWin = win
	a.supportChatAllowClose = nil
	a.supportChatWinPaints.Store(true)

	// Conversa aceita (não "waiting"): o caminho de fechamento fica habilitado.
	a.hideSupportChatInviteSurface()

	if a.supportChatWin != win {
		t.Fatal("a janela pintada precisa continuar registrada (reuso) — sem janela de tempo com 'janela fechada'")
	}
	if !a.supportChatWindowOpen() {
		t.Fatal("com a janela viva na tela, supportChatWindowOpen() precisa dizer true")
	}
}

// BUG 3: markSupportChatOpenStep criava trace. Depois de uma abertura concluída,
// o próximo sinal de janela ressuscitava um trace que nunca terminava e a
// pintura seguinte logava uma linha "abertura:" falsa.
func TestSupportChatOpenTrace_NaoRessuscitaDepoisDeConcluido(t *testing.T) {
	a := newSupportChatTestApp(t, "http://127.0.0.1:1")
	lines := captureAppLogs(a)

	a.supportChatTraceFor("sess-1").mark("post")
	a.finishSupportChatOpenTrace()

	// Sinal tardio da janela (reload, reemissão do ready): não pode criar trace.
	a.markSupportChatOpenStep("sess-1", "pintura")
	if trace := a.currentSupportChatOpenTrace(); trace != nil {
		t.Fatal("etapa sem abertura em andamento não pode criar um trace novo")
	}

	// E, portanto, a próxima pintura não pode fechar uma "abertura" falsa.
	a.SupportChatWindowPainted()
	for _, line := range *lines {
		if strings.Contains(line, "abertura:") && strings.Contains(line, "post=-") {
			t.Fatalf("linha de abertura sem etapas (trace ressuscitado): %q", line)
		}
	}
}

// BUG 4: markSupportChatOpenStep misturava etapas de OUTRA sessão no trace da
// sessão que iniciou a abertura.
func TestSupportChatOpenTrace_IgnoraOutraSessao(t *testing.T) {
	a := newSupportChatTestApp(t, "http://127.0.0.1:1")

	trace := a.supportChatTraceFor("sess-1")
	trace.mark("post")

	a.markSupportChatOpenStep("sess-2", "pintura")

	if offset := trace.offset("pintura"); offset >= 0 {
		t.Fatal("etapa de outra sessão não pode entrar no trace da abertura corrente")
	}
}

// BUG 5: a renavegação de emergência do webview descartava os parâmetros de
// contexto, e a página recarregada perdia o cabeçalho da conversa.
func TestSupportChatWindowURL_MantemContextoDaSessao(t *testing.T) {
	a := newSupportChatTestApp(t, "http://127.0.0.1:1")

	bare := a.supportChatWindowURL("/chat-window.html")
	if bare != "/chat-window.html" {
		t.Fatalf("sem sessão a URL não deve ganhar query; obtido %q", bare)
	}
}
