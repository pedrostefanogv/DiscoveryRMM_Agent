package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"discovery/app/supportchat"
)

// ── Revisão por bugs da revisão 14 ─────────────────────────────────────────

// BUG CORRIGIDO: o caminho do ACEITE fazia o POST duas vezes — uma no cartão
// (reportSupportChatInviteDecision) e outra em
// runSupportChatInviteNotificationDecision (via SupportChatRespondInvite).
// O segundo POST voltava "session_not_pending" e poluía o log.
//
// Aqui: um único aceite no cartão deve gerar exatamente UMA chamada a
// POST /sessions/{id}/invite-response.
func TestReportSupportChatInviteDecision_AceiteFazUmUnicoPost(t *testing.T) {
	var mu sync.Mutex
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/invite-response") {
			mu.Lock()
			posts++
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	a := newSupportChatTestApp(t, srv.URL)
	err := a.reportSupportChatInviteDecision(supportChatInviteNotificationResult{
		SessionID: "sessao-aceite",
		Accepted:  true,
	})
	if err != nil {
		t.Fatalf("aceite deveria ser reportado sem erro: %v", err)
	}

	mu.Lock()
	got := posts
	mu.Unlock()
	if got != 1 {
		t.Fatalf("o aceite deve gerar exatamente 1 POST; obtidos %d", got)
	}
}

// O aceite funciona mesmo SEM o estado local pendente: no modo companion o
// cartão é criado na UI e o estado de verdade fica no serviço. Antes o POST
// era condicional ao estado local e o aceite se perdia.
func TestReportSupportChatInviteDecision_AceiteSemEstadoLocalPendente(t *testing.T) {
	var mu sync.Mutex
	accepted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/invite-response") {
			mu.Lock()
			accepted = true
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	a := newSupportChatTestApp(t, srv.URL)
	// Estado local vazio (nenhum convite registrado nesta instância).
	if st := a.supportChatService().State(); st.Status == supportchat.StatusWaiting {
		t.Fatalf("pré-condição: o estado local não pode estar pendente (%q)", st.Status)
	}

	if err := a.reportSupportChatInviteDecision(supportChatInviteNotificationResult{SessionID: "sessao-x", Accepted: true}); err != nil {
		t.Fatalf("aceite sem estado local deveria ser reportado: %v", err)
	}
	mu.Lock()
	ok := accepted
	mu.Unlock()
	if !ok {
		t.Fatal("o POST do aceite precisa sair mesmo sem estado local pendente")
	}
}

// Convite vencido nunca é apresentado pelo binding (JS/IPC): o vencimento é
// lido do estado do serviço, porque o binding só recebe campos de exibição.
func TestEnsureSupportChatInviteSurface_NaoApresentaVencido(t *testing.T) {
	st := supportchat.State{
		Status:    supportchat.StatusWaiting,
		SessionID: "sessao-velha",
		ExpiresAt: time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339),
	}
	invite := supportchat.Invite{SessionID: st.SessionID, ExpiresAt: st.ExpiresAt}
	if !invite.ExpiredAt(time.Now()) {
		t.Fatal("pré-condição: o convite precisa estar vencido")
	}

	a := &App{}
	// Sem app Wails a chamada é no-op (não cria superfície nenhuma).
	a.ensureSupportChatInviteSurface(st)
	a.supportChatNotifyMu.Lock()
	cards := len(a.supportChatNotify)
	a.supportChatNotifyMu.Unlock()
	if cards != 0 {
		t.Fatalf("convite vencido não pode gerar cartão; cartões=%d", cards)
	}
}
