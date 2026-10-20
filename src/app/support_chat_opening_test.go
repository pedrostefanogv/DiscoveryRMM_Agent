package app

import (
	"strings"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"discovery/app/supportchat"
)

// ── Abertura da janela do chat: reuso, posição e link real do canal ────────
//
// RELATO: "às vezes, quando mando abrir o chat, demora para abrir e aparece a
// mensagem de sem conexão". As guardas abaixo cobrem a causa de cada sintoma:
//
//   - a janela do convite (já pintada) é REUSADA em vez de destruída e recriada
//     (recriar o WebView2 custa o carregamento inteiro);
//   - a janela nova nasce no MESMO ponto da tela da anterior;
//   - o aviso "Sem conexão em tempo real" só aparece com o BACKEND fora, e o
//     processo de UI não pode inventá-lo a partir do próprio estado.

func TestSupportChatWindowReusable_SoComJanelaPintada(t *testing.T) {
	app := &App{}
	if app.supportChatWindowReusableForChat() {
		t.Fatal("sem janela, não há o que reusar")
	}

	// Janela criada mas AINDA sem pintura: reusar deixaria o chat numa janela
	// branca — precisa recriar.
	app.supportChatWin = &fakeChatWindow{}
	app.supportChatWinPaints.Store(false)
	if app.supportChatWindowReusableForChat() {
		t.Fatal("janela que ainda não pintou não pode ser reusada para a conversa")
	}

	app.supportChatWinPaints.Store(true)
	if !app.supportChatWindowReusableForChat() {
		t.Fatal("janela já pintada deve ser reusada no aceite (evita recriar o webview)")
	}
}

func TestSupportChatWindowOptionsWithContext_LevaCabecalhoParaAURL(t *testing.T) {
	opts := supportChatWindowOptions(520, 680)
	opts = supportChatWindowOptionsWithContext(opts, supportchat.State{
		SessionID:   "sess-1",
		TicketID:    "t-9",
		SupportName: "pedrostefanogv",
		TicketTitle: "Impressora & rede",
	})

	if !strings.HasPrefix(opts.URL, "/chat-window.html?") {
		t.Fatalf("a URL da janela deve manter a página e ganhar a query; obtido %q", opts.URL)
	}
	for _, needle := range []string{"sessionId=sess-1", "ticketId=t-9", "supportName=pedrostefanogv"} {
		if !strings.Contains(opts.URL, needle) {
			t.Fatalf("URL sem %q: %q", needle, opts.URL)
		}
	}
	// Espaços e '&' precisam sair escapados para não quebrar a query.
	if !strings.Contains(opts.URL, "ticketTitle=Impressora+%26+rede") {
		t.Fatalf("ticketTitle deve ser escapado; obtido %q", opts.URL)
	}

	// Sem sessão (nenhum dado): URL intacta.
	bare := supportChatWindowOptionsWithContext(supportChatWindowOptions(520, 680), supportchat.State{})
	if bare.URL != "/chat-window.html" {
		t.Fatalf("sem dados a URL não deve mudar; obtido %q", bare.URL)
	}
}

func TestSupportChatPlacementForNewWindow_PriorizaPosicaoAnterior(t *testing.T) {
	screenA := &application.Screen{Bounds: application.Rect{X: 0, Y: 0, Width: 1920, Height: 1080}, WorkArea: application.Rect{X: 0, Y: 0, Width: 1920, Height: 1040}, IsPrimary: true}
	screenB := &application.Screen{Bounds: application.Rect{X: 1920, Y: 0, Width: 1920, Height: 1080}, WorkArea: application.Rect{X: 1920, Y: 0, Width: 1920, Height: 1040}}

	app := &App{}
	app.supportChatLastPlacement = &supportChatPlacementRecord{screen: screenB, x: 200, y: 120}

	if !sameSupportChatScreen(screenB, &application.Screen{Bounds: screenB.Bounds}) {
		t.Fatal("a mesma tela deve ser reconhecida pelos limites, mesmo em outra instância")
	}
	if sameSupportChatScreen(screenA, screenB) {
		t.Fatal("telas diferentes não podem ser confundidas")
	}

	// Posição fora da área de trabalho (janela minimizada/escondida) não vale.
	app.supportChatLastPlacement = &supportChatPlacementRecord{screen: screenA, x: 5000, y: 10}
	if _, point := app.supportChatPlacementForNewWindow(nil); point != nil {
		t.Fatal("posição fora da tela não pode ser reaproveitada")
	}
}

func TestSupportChatLink_NuncaInformadoAssumeConectado(t *testing.T) {
	state := &supportChatLinkState{}
	link, settled := state.snapshot(time.Now())
	if !link.active || !settled {
		t.Fatal("sem informação do serviço o link não pode aparecer como 'sem conexão'")
	}
}

func TestSupportChatLink_MudancaRespeitaCarencia(t *testing.T) {
	state := &supportChatLinkState{}
	if !state.set(true) {
		t.Fatal("o primeiro valor sempre conta como mudança")
	}
	if state.set(true) {
		t.Fatal("repetir o mesmo valor não pode reiniciar a transição")
	}

	// Queda recente: ainda não settle → a janela continua sem mostrar o aviso.
	state.set(false)
	link, settled := state.snapshot(time.Now())
	if link.active || settled {
		t.Fatal("queda recente não deve estar settled (evita piscar o aviso num flap de NATS)")
	}

	// Passada a carência: queda confirmada.
	link, settled = state.snapshot(time.Now().Add(supportChatLinkGrace + time.Second))
	if link.active || !settled {
		t.Fatal("depois da carência a queda deve ser reportada como settled")
	}

	// Volta do canal AINDA dentro da carência: o aviso não deve ser pintado.
	state.set(true)
	app := &App{}
	app.supportChatLink.set(true)
	payload := app.supportChatLinkPayload()
	if payload["active"] != true {
		t.Fatalf("link recém-restabelecido deve sair como ativo; obtido %v", payload["active"])
	}
}

func TestSupportChatLinkFromIPC_LePayloadDoServico(t *testing.T) {
	if active, known := supportChatLinkFromIPC(map[string]any{"supportChatLinkActive": true}); !known || !active {
		t.Fatal("campo supportChatLinkActive deve ser lido")
	}
	if active, known := supportChatLinkFromIPC(map[string]any{"realtime": map[string]any{"active": false}}); !known || active {
		t.Fatal("realtime.active deve ser lido")
	}
	if _, known := supportChatLinkFromIPC(map[string]any{"status": "active"}); known {
		t.Fatal("payload sem link não pode ser interpretado como conhecido")
	}
}

func TestSupportChatOpenTrace_RegistraEtapas(t *testing.T) {
	trace := newSupportChatOpenTrace("sess-1", time.Now())
	trace.mark("post")
	trace.setReuse(true)
	trace.setWebview("reaproveitado")

	line := trace.finish()
	for _, needle := range []string{"sessao=sess-1", "post=", "reuso=sim", "webview=reaproveitado"} {
		if !strings.Contains(line, needle) {
			t.Fatalf("linha de abertura sem %q: %s", needle, line)
		}
	}
	// Etapas não cumpridas saem como "-" (abertura incompleta).
	if !strings.Contains(line, "pintura=-") {
		t.Fatalf("etapa ausente deve aparecer como '-': %s", line)
	}
}

func TestSupportChatUISpawnInProgress_UsaProgressoRecente(t *testing.T) {
	app := &App{}
	supportChatUIProgress.mu.Lock()
	supportChatUIProgress.lastAt = time.Time{}
	supportChatUIProgress.mu.Unlock()

	if app.supportChatUISpawnInProgress() {
		t.Fatal("sem qualquer sinal de UI não há progresso (recusa no_ui deve valer)")
	}

	app.markSupportChatUISpawn("chat_invite")
	if !app.supportChatUISpawnInProgress() {
		t.Fatal("relançamento recente é progresso: o prazo do convite deve ser prorrogado")
	}

	supportChatUIProgress.mu.Lock()
	supportChatUIProgress.lastAt = time.Now().Add(-supportChatUIProgressTTL - time.Minute)
	supportChatUIProgress.mu.Unlock()
	if app.supportChatUISpawnInProgress() {
		t.Fatal("progresso antigo não vale mais")
	}
}

// fakeChatWindow é uma janela mínima para os testes de reuso (não abre nada).
type fakeChatWindow struct {
	application.Window
}
