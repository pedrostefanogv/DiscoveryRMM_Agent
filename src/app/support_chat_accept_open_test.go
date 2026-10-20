package app

import (
	"os"
	"strings"
	"testing"

	"discovery/app/supportchat"
)

// ── "Cliquei em Abrir chat e a janela não abriu" ──────────────────────────
//
// LOG REAL (18:20:55):
//
//	convite aceito no cartao nativo
//	janela do chat nao aberta: conversa ainda nao foi aceita (status=waiting)
//	estado -> active (invite-response)          <- 76 ms DEPOIS
//
// O servidor ACEITOU, mas a abertura consultava o status LOCAL, ainda
// "waiting", e desistia — e ninguém tentava de novo. A janela nunca era criada.
// A confirmação do SERVIDOR é a autoridade: com ela a janela abre.

// Prepara um convite pendente de verdade, pelo caminho público do serviço.
func prepareWaitingInvite(t *testing.T, a *App, sessionID string) {
	t.Helper()
	decision := a.supportChatService().PrepareInviteIfIdle(supportchat.Invite{
		SessionID:   sessionID,
		SupportName: "pedrostefanogv",
	})
	if !decision.Accepted {
		t.Fatalf("pré-condição: o convite precisa ser aceito pelo serviço; motivo=%q", decision.Reason)
	}
	if state := a.supportChatService().State(); state.Status != supportchat.StatusWaiting {
		t.Fatalf("pré-condição: estado inicial precisa ser waiting; obtido %q", state.Status)
	}
}

// O alinhamento de estado é o que destrava a abertura: mesma sessão,
// "waiting" -> "active", sem depender de evento nenhum.
func TestOpenForAcceptedSession_AlinhaEstadoAntesDeAbrir(t *testing.T) {
	a := newSupportChatTestApp(t, "http://127.0.0.1:1")
	prepareWaitingInvite(t, a, "sess-aceite")

	svc := a.supportChatService()
	// Pré-condição do bug: com "waiting" a janela é recusada.
	if supportChatWindowMayOpen(svc.State().Status) {
		t.Fatal("pré-condição: 'waiting' não pode permitir a abertura")
	}

	// a.app == nil neste App de teste: a abertura para no guard de Wails (sem
	// pânico), mas o ESTADO já precisa ter sido alinhado antes.
	a.openSupportChatWindowForAcceptedSession("sess-aceite")

	if state := svc.State(); state.Status != supportchat.StatusActive {
		t.Fatalf("depois do aceite confirmado o estado local precisa ser 'active' (era %q) — "+
			"sem isso openSupportChatWindow recusa a abertura", state.Status)
	}
	if !supportChatWindowMayOpen(svc.State().Status) {
		t.Fatal("com o aceite confirmado a janela PRECISA poder abrir")
	}
}

// Sessão diferente não é ressuscitada pelo alinhamento.
func TestOpenForAcceptedSession_NaoRessuscitaOutraSessao(t *testing.T) {
	a := newSupportChatTestApp(t, "http://127.0.0.1:1")
	prepareWaitingInvite(t, a, "sess-corrente")

	a.openSupportChatWindowForAcceptedSession("sess-outra")

	if state := a.supportChatService().State(); state.Status != supportchat.StatusWaiting {
		t.Fatalf("aceite de outra sessão não pode mudar o estado da corrente; obtido %q", state.Status)
	}
}

// MarkActive: idempotente e só para a sessão corrente.
func TestServiceMarkActive_IdempotenteESoDaSessaoCorrente(t *testing.T) {
	a := newSupportChatTestApp(t, "http://127.0.0.1:1")
	prepareWaitingInvite(t, a, "s1")
	svc := a.supportChatService()

	svc.MarkActive("s2")
	if state := svc.State(); state.Status != supportchat.StatusWaiting {
		t.Fatalf("MarkActive de outra sessão não pode agir; obtido %q", state.Status)
	}

	svc.MarkActive("s1")
	if state := svc.State(); state.Status != supportchat.StatusActive {
		t.Fatalf("MarkActive da sessão corrente precisa virar active; obtido %q", state.Status)
	}

	if after := svc.State(); after.Status != supportchat.StatusActive {
		t.Fatal("MarkActive precisa ser idempotente")
	}
}

// Guarda de contrato do código: a abertura de um aceite CONFIRMADO não pode
// voltar a depender do status local (o bug acima era exatamente isso).
func TestAcceptDecision_NaoConsultaStatusLocalParaAbrir(t *testing.T) {
	raw, err := os.ReadFile("support_chat_invite_notification.go")
	if err != nil {
		t.Fatalf("não foi possível ler o arquivo: %v", err)
	}
	content := string(raw)
	if !strings.Contains(content, "openSupportChatWindowForAcceptedSession(result.SessionID)") {
		t.Fatal("o aceite confirmado deve abrir a janela por openSupportChatWindowForAcceptedSession (sem depender do estado local)")
	}
	if strings.Contains(content, "go a.openSupportChatWindow()") {
		t.Fatal("o aceite não pode chamar openSupportChatWindow direto: ele recusa com status=waiting e a janela nunca abre")
	}
}
