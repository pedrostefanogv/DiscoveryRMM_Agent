package app

import (
	"os"
	"strings"
	"testing"
)

// ── Guardas do incidente 21/10 (convite no tray congelando a interface) ──────
//
// Cadeia do incidente: convite chega com o agente no tray (sem janela principal,
// WebView2 frio) → o prewarm criava a janela do chat OCULTA → ela virava o
// PRIMEIRO ambiente WebView2 do processo, nascendo na main thread no meio do
// convite → a UI parava de responder ao IPC por ~96 s (nascimento do chat +
// nascimento da janela principal enfileirada pelo clique no tray) e o
// ui-watchdog do serviço matava a UI; o convite expirava no servidor e o clique
// em "Abrir chat" virava no-op.
//
// Regras agora codificadas em contrato:
//  1. o prewarm da janela do chat só roda com a janela principal VIVA;
//  2. a reapresentação do convite aquece o ambiente sem nascer a janela do chat
//     direto (no tray, quem nasce oculta é a JANELA PRINCIPAL);
//  3. openSupportChatWindow não cria a janela com o ambiente frio: aquece e
//     agenda;
//  4. a criação da janela principal usa single-flight próprio (windowCreating)
//     sem segurar windowMu durante o nascimento do WebView2.

func readFileString(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("não foi possível ler %s: %v", name, err)
	}
	return string(raw)
}

// functionBody extrai o corpo de UMA função (da declaração até a próxima).
func functionBody(content, funcDecl string) string {
	start := strings.Index(content, funcDecl)
	if start < 0 {
		return ""
	}
	next := strings.Index(content[start+1:], "\nfunc ")
	if next < 0 {
		return content[start:]
	}
	return content[start : start+1+next]
}

// 1) O prewarm da janela do chat nunca pode ser o primeiro WebView2 do processo.
func TestPrewarmChatWindow_ExigeJanelaPrincipalViva(t *testing.T) {
	body := functionBody(readFileString(t, "support_chat_window.go"), "func (a *App) prewarmSupportChatWindow()")
	if body == "" {
		t.Fatal("prewarmSupportChatWindow não encontrada")
	}
	if !strings.Contains(body, "!a.mainWindowReady()") {
		t.Fatal("o prewarm da janela do chat precisa exigir a janela principal VIVA (ambiente quente); o gate antigo (mainWindowInitPending) tratava 'nunca tentou criar' como livre e a janela do chat virava o primeiro WebView2 do processo no meio do convite")
	}
	if strings.Contains(body, "a.mainWindowInitPending()") {
		t.Fatal("o prewarm não pode voltar ao gate fraco mainWindowInitPending")
	}
}

// 2) A reapresentação do convite aquece o ambiente sem nascer a janela do chat.
func TestInviteSurface_AqueceAmbienteSemNascerChatOculto(t *testing.T) {
	content := readFileString(t, "support_chat_window.go")
	if !strings.Contains(content, "go a.prewarmSupportChatEnvironment()") {
		t.Fatal("a reapresentação do convite deve aquecer o ambiente via prewarmSupportChatEnvironment (nunca criar a janela do chat com WebView2 frio)")
	}
	if strings.Contains(content, "go a.prewarmSupportChatWindow()") {
		t.Fatal("o convite não pode pré-criar a janela do chat direto: no tray isso nascia o primeiro WebView2 do processo na main thread durante o convite")
	}
}

// 3) Ambiente frio: aquecer pela janela principal e agendar, nunca criar direto.
func TestOpenSupportChatWindow_AmbienteFrioAqueceEAgenda(t *testing.T) {
	body := functionBody(readFileString(t, "support_chat_window.go"), "func (a *App) openSupportChatWindow()")
	if body == "" {
		t.Fatal("openSupportChatWindow não encontrada")
	}
	if !strings.Contains(body, "a.startMainWindowWarm()") || !strings.Contains(body, "a.scheduleSupportChatOpenAfterLazyLoad()") {
		t.Fatal("com a janela principal ausente (WebView2 frio), openSupportChatWindow precisa aquecer pela janela principal oculta e agendar a abertura")
	}
}

// 4) A criação da janela principal não prende windowMu durante o WebView2.
func TestEnsureMainWindow_SingleFlightSemMutexNaCriacao(t *testing.T) {
	body := functionBody(readFileString(t, "window_lazy.go"), "func (a *App) ensureMainWindow()")
	if body == "" {
		t.Fatal("ensureMainWindow não encontrada")
	}
	if !strings.Contains(body, "a.windowCreating.CompareAndSwap(false, true)") {
		t.Fatal("ensureMainWindow precisa do single-flight windowCreating: a factory bloqueia até o WebView2 nascer na main thread e prender windowMu nesse intervalo trava chamadores que rodam na própria main thread")
	}
	unlock := strings.Index(body, "a.windowMu.Unlock()")
	factory := strings.Index(body, "factory()")
	if unlock < 0 || factory < 0 || unlock > factory {
		t.Fatal("a factory precisa rodar com windowMu LIBERADO (a criação bloqueia na main thread; prender o mutex causa deadlock com o aquecimento em segundo plano)")
	}
}
