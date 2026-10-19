package supportchat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ActiveSessionID é a checagem usada em BACKGROUND ao abrir a janela do
// convite. Ela não pode usar Refresh: Refresh zera o estado "waiting" quando o
// servidor responde sem sessão, apagando o pedido de aceite recém-aberto.
func TestActiveSessionIDNaoAlteraEstadoWaiting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/active") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"hasSession": false})
	}))
	defer server.Close()

	svc := serviceWithContextPointingAt(server.URL)
	svc.PrepareInvite(Invite{SessionID: testSessionID, TicketID: testTicketID})

	sessionID, hasSession, err := svc.ActiveSessionID(context.Background())
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if hasSession || sessionID != "" {
		t.Fatalf("servidor sem sessão deveria devolver hasSession=false; obtido %q %v", sessionID, hasSession)
	}
	state := svc.State()
	if state.Status != StatusWaiting || state.SessionID != testSessionID {
		t.Fatalf("ActiveSessionID não pode alterar o estado local; obtido %s/%s", state.Status, state.SessionID)
	}
}

func TestActiveSessionIDRetornaSessaoDoServidor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/active") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"hasSession": true,
			"session":    map[string]any{"id": testSessionID, "status": "Pending"},
		})
	}))
	defer server.Close()

	svc := serviceWithContextPointingAt(server.URL)

	sessionID, hasSession, err := svc.ActiveSessionID(context.Background())
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if !hasSession || sessionID != testSessionID {
		t.Fatalf("sessão esperada %q; obtido %q (hasSession=%v)", testSessionID, sessionID, hasSession)
	}
}

// O boot da janela usa RefreshKeepingInvite: uma resposta transitória "sem
// sessão" NÃO pode apagar o convite waiting recém-recebido.
func TestRefreshKeepingInvitePreservaWaitingSemSessao(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/active") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"hasSession": false})
	}))
	defer server.Close()

	svc := serviceWithContextPointingAt(server.URL)
	svc.PrepareInvite(Invite{SessionID: testSessionID, TicketID: testTicketID})

	state, err := svc.RefreshKeepingInvite(context.Background())
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if state.Status != StatusWaiting || state.SessionID != testSessionID {
		t.Fatalf("RefreshKeepingInvite não pode apagar o convite waiting; obtido %s/%s", state.Status, state.SessionID)
	}
}

// ClearStateIfWaiting é a operação atômica usada pelo verify. Nunca pode
// limpar um estado que já deixou de ser o convite aguardando.
func TestClearStateIfWaitingSoLimpaWaitingDaSessao(t *testing.T) {
	svc := newTestService()

	if svc.ClearStateIfWaiting(testSessionID) {
		t.Fatal("não pode limpar quando não existe convite")
	}

	svc.PrepareInvite(Invite{SessionID: testSessionID, TicketID: testTicketID})
	if svc.ClearStateIfWaiting("outra-sessao") {
		t.Fatal("não pode limpar o convite de outra sessão")
	}
	if state := svc.State(); state.Status != StatusWaiting {
		t.Fatalf("convite deveria continuar waiting; obtido %s", state.Status)
	}

	if !svc.ClearStateIfWaiting(testSessionID) {
		t.Fatal("deveria limpar o waiting da sessão informada")
	}
	if state := svc.State(); state.Status != StatusNone {
		t.Fatalf("estado deveria voltar a none; obtido %s", state.Status)
	}
}
