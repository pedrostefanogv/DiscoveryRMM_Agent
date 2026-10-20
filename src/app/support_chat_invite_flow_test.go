package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"

	"discovery/app/supportchat"
)

// ── Regressões do convite → aceite → abertura da janela ─────────────────────
//
// BUG RELATADO: "a notificação para aprovar o chat aparece corretamente, mas
// quando mando abrir o agent a interface/janela do chat não abre" e "cliquei em
// Agora não e não aparece para o suporte no chat pelo navegador".
//
// Causa raiz (corrigida): caminhos PARALELOS criavam a janela do chat ainda com
// o convite PENDENTE (reentrega do evento IPC, handler de JS da janela
// principal, reconciliação de startup). Essa janela nascia vazia, ficava
// topmost por cima do cartão de aceite e, como openSupportChatWindow reusa a
// janela existente, o aceite seguinte apenas reexibia o painel vazio. A recusa,
// por sua vez, era reportada DEPOIS de destruir o cartão e o estado local seguia
// "waiting" quando o POST falhava — o console ficava "Aguardando confirmação".

// A janela do chat NÃO pode ser criada antes do aceite: em "waiting" a única
// superfície é o cartão do convite (ou a janela fallback).
func TestSupportChatWindowMayOpen_SomenteAposAceite(t *testing.T) {
	cases := []struct {
		status string
		want   bool
	}{
		{"active", true},
		{" active ", true},
		{"ended", true},
		{"waiting", false},
		{"none", false},
		{"", false},
		{"desconhecido", false},
	}

	for _, tc := range cases {
		if got := supportChatWindowMayOpen(tc.status); got != tc.want {
			t.Fatalf("supportChatWindowMayOpen(%q) = %v; esperado %v", tc.status, got, tc.want)
		}
	}
}

// O painel criado no aceite precisa nascer no topo e centralizado (o aceite é
// uma ação do usuário; a janela não pode nascer atrás do cartão/app em uso).
func TestSupportChatWindowOptionsOn_ConviteEmAndamento(t *testing.T) {
	screen := &application.Screen{IsPrimary: true, WorkArea: application.Rect{Width: 1920, Height: 1040}}
	opts := supportChatWindowOptionsOn(supportChatWindowWidth, supportChatWindowHeight, screen, true)
	if !opts.AlwaysOnTop {
		t.Fatal("a janela do convite precisa nascer sempre no topo")
	}
	if opts.Screen != screen {
		t.Fatal("a janela precisa nascer na tela escolhida")
	}
}

// O modo LEGADO (sem cartão nativo, ex.: Linux/macOS) é a única exceção: ali a
// janela do chat É a superfície do convite, então ela pode existir em "waiting".
func TestSupportChatLegacyInviteWindowMode_Excecao(t *testing.T) {
	original := supportChatNativeInviteCard
	defer func() { supportChatNativeInviteCard = original }()

	supportChatNativeInviteCard = true
	a := &App{}
	if a.supportChatLegacyInviteWindowMode() {
		t.Fatal("com cartão nativo o fluxo fallback não pode estar ativo")
	}
	if !supportChatWindowMayOpen(supportchat.StatusActive) {
		t.Fatal("conversa ativa sempre pode abrir a janela")
	}

	supportChatNativeInviteCard = false
	if !a.supportChatLegacyInviteWindowMode() {
		t.Fatal("sem cartão nativo o fluxo fallback precisa estar ativo")
	}
}

// Sem serviço de chat configurado não existe convite pendente.
func TestSupportChatInviteIsCurrentPending_SemSessao(t *testing.T) {
	a := &App{}
	if a.supportChatInviteIsCurrentPending("sessao-1") {
		t.Fatal("sem convite registrado não pode haver convite pendente")
	}
	if a.supportChatInviteIsCurrentPending("   ") {
		t.Fatal("sessionId vazio não pode ser convite pendente")
	}
}

// O cartão não tem mais estado de espera: responder (aceitar OU recusar) tira o
// cartão da tela na hora (ver resolve em _windows.go). Este teste garante que
// nenhuma mensagem de espera voltou para a UI do cartão.
func TestSupportChatInviteNotification_SemMensagemDeEspera(t *testing.T) {
	for _, text := range []string{
		supportChatInviteNotificationMessage(supportchat.Invite{SessionID: "s", SupportName: "Ana"}),
		supportChatInviteNotificationHint(),
	} {
		lower := strings.ToLower(text)
		if strings.Contains(lower, "abrindo o chat") || strings.Contains(lower, "avisando o suporte") {
			t.Fatalf("texto de espera não deve mais existir no cartão: %q", text)
		}
	}
}

// REGRESSÃO PRINCIPAL do "recusei e o console continua aguardando": a decisão
// do cartão é reportada ao SERVIDOR pelo próprio cartão, ANTES de ele sair da
// tela. Este teste exercita o caminho completo com um servidor de mentira e
// confirma que o motivo reportado é o do USUÁRIO (user_denied).
func TestReportSupportChatInviteDecision_RecusaChegaAoServidor(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	a := newSupportChatTestApp(t, srv.URL)

	err := a.reportSupportChatInviteDecision(supportChatInviteNotificationResult{
		SessionID: "sessao-1",
		Accepted:  false,
	})
	if err != nil {
		t.Fatalf("recusa deveria ser reportada sem erro: %v", err)
	}
	if !strings.HasSuffix(gotPath, "/sessions/sessao-1/invite-response") {
		t.Fatalf("rota inesperada: %q", gotPath)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("payload invalido (%q): %v", gotBody, err)
	}
	if accepted, _ := payload["accepted"].(bool); accepted {
		t.Fatalf("payload deveria ser de recusa: %s", gotBody)
	}
	if reason, _ := payload["reason"].(string); reason != "user_denied" {
		t.Fatalf("motivo reportado = %q; esperado user_denied", reason)
	}

	// Durante o POST o convite não pode ser reaberto (reentrega/JS/IPC).
	if !a.supportChatInviteNotificationWasAnswered("sessao-1") {
		t.Fatal("a resposta precisa marcar o convite como respondido antes do POST")
	}
}

// Sem sessionId (cartão órfão/reentrega corrompida) não há o que reportar — e a
// chamada não pode entrar em pânico nem fazer rede.
func TestReportSupportChatInviteDecision_SemSessao(t *testing.T) {
	a := &App{}
	if err := a.reportSupportChatInviteDecision(supportChatInviteNotificationResult{Accepted: true}); err != nil {
		t.Fatalf("sem sessão deveria ser no-op: %v", err)
	}
}

// newSupportChatTestApp monta um App mínimo com o serviço do chat apontando
// para o servidor de teste (sem Wails).
func newSupportChatTestApp(t *testing.T, apiServer string) *App {
	t.Helper()

	a := &App{}
	a.ctx = context.Background()
	a.supportChatSvc = supportchat.New(supportchat.Deps{
		Ctx: func() context.Context { return a.ctx },
		Logf: func(string) {},
		GetDebugConfig: func() supportchat.DebugConfig {
			return supportchat.DebugConfig{
				AgentID:   "8f14e45f-ceea-4b7e-9b0d-0c1a2b3c4d5e",
				ApiScheme: "http",
				ApiServer: apiServer,
				AuthToken: "mdz_test",
			}
		},
		Emit: func(string, ...any) {},
	})
	return a
}
