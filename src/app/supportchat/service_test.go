package supportchat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// (regressões adicionadas na revisão: ver final do arquivo)

const (
	testAgentID   = "0d1b6a5f-9d3a-4a2e-9d55-1c2f9a2f7b41"
	testSessionID = "6a7f0f9c-2b7c-4d1a-9f0e-3a6a1b2c3d4e"
	testTicketID  = "b1c2d3e4-f5a6-4b7c-8d9e-0f1a2b3c4d5e"
)

func newTestService() *Service {
	return New(Deps{
		Logf: func(string) {},
		GetDebugConfig: func() DebugConfig {
			return DebugConfig{AgentID: testAgentID, ApiScheme: "https", ApiServer: "example.invalid:5288", AuthToken: "mdz_test"}
		},
		Emit: func(string, ...any) {},
	})
}

func messageEnvelope(clientID, body string, sequence int64) []byte {
	payload, _ := json.Marshal(Envelope{
		EventType:       EventMessage,
		SessionID:       testSessionID,
		TicketID:        testTicketID,
		AuthorKind:      "support",
		Body:            body,
		Sequence:        sequence,
		ClientMessageID: clientID,
	})
	return payload
}

func TestHandleEnvelope_MessageAppends(t *testing.T) {
	svc := newTestService()

	if err := svc.HandleEnvelope(messageEnvelope("m1", "ola", 1)); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}

	state := svc.State()
	if len(state.Messages) != 1 {
		t.Fatalf("esperava 1 mensagem, obteve %d", len(state.Messages))
	}
	if state.Messages[0].Body != "ola" {
		t.Fatalf("corpo inesperado: %q", state.Messages[0].Body)
	}
	if state.SessionID != testSessionID {
		t.Fatalf("sessionId não propagado: %q", state.SessionID)
	}
}

func TestHandleEnvelope_DeduplicatesByClientMessageID(t *testing.T) {
	svc := newTestService()

	// at-least-once: o mesmo envelope chega duas vezes.
	_ = svc.HandleEnvelope(messageEnvelope("dup-1", "texto", 1))
	_ = svc.HandleEnvelope(messageEnvelope("dup-1", "texto", 1))

	if got := len(svc.State().Messages); got != 1 {
		t.Fatalf("dedupe falhou: %d mensagens", got)
	}
}

func TestHandleEnvelope_IgnoresOlderSequences(t *testing.T) {
	svc := newTestService()

	_ = svc.HandleEnvelope(messageEnvelope("m1", "primeira", 5))
	_ = svc.HandleEnvelope(messageEnvelope("m2", "antiga", 2))

	state := svc.State()
	if len(state.Messages) != 1 || state.Messages[0].Body != "primeira" {
		t.Fatalf("sequência fora de ordem aceita indevidamente: %+v", state.Messages)
	}
}

func TestHandleEnvelope_SessionOpenedAndEnded(t *testing.T) {
	svc := newTestService()

	opened, _ := json.Marshal(Envelope{EventType: EventSessionOpened, SessionID: testSessionID, TicketID: testTicketID})
	if err := svc.HandleEnvelope(opened); err != nil {
		t.Fatalf("erro ao abrir sessão: %v", err)
	}
	if state := svc.State(); state.Status != StatusActive || !state.HasSession {
		t.Fatalf("estado esperado active/hasSession, obteve %+v", state)
	}

	ended, _ := json.Marshal(Envelope{EventType: EventSessionEnded, SessionID: testSessionID, EndReason: "support_closed"})
	if err := svc.HandleEnvelope(ended); err != nil {
		t.Fatalf("erro ao encerrar sessão: %v", err)
	}
	if state := svc.State(); state.Status != StatusEnded || state.HasSession {
		t.Fatalf("estado esperado ended/sem sessão, obteve %+v", state)
	}
}

func TestHandleEnvelope_InvalidPayloadReturnsError(t *testing.T) {
	svc := newTestService()

	if err := svc.HandleEnvelope([]byte("{")); err == nil {
		t.Fatal("esperava erro para envelope inválido")
	}
}

func TestPrepareInviteThenClearState_KeepsNoHistory(t *testing.T) {
	svc := newTestService()

	svc.PrepareInvite(Invite{SessionID: testSessionID, TicketID: testTicketID, TicketTitle: "Notebook lento", SupportName: "Ana"})
	state := svc.State()
	if state.Status != StatusWaiting || state.SessionID != testSessionID || state.TicketTitle != "Notebook lento" {
		t.Fatalf("convite não preparado corretamente: %+v", state)
	}

	_ = svc.HandleEnvelope(messageEnvelope("m1", "conversa", 1))

	// Ao fechar a janela o agent descarta TUDO (requisito: sem histórico).
	svc.ClearState()

	final := svc.State()
	if final.HasSession || final.SessionID != "" || len(final.Messages) != 0 || final.Status != StatusNone {
		t.Fatalf("estado deveria estar limpo, obteve %+v", final)
	}
}

func TestRefresh_ReadsActiveSessionFromApi(t *testing.T) {
	var paths []string
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()

		if got := r.Header.Get("X-Agent-ID"); got != testAgentID {
			t.Errorf("X-Agent-ID inesperado: %q", got)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("Authorization ausente")
		}

		switch {
		case strings.HasSuffix(r.URL.Path, "/active"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"hasSession": true,
				"session": map[string]any{
					"id":            testSessionID,
					"ticketId":      testTicketID,
					"status":        "Active",
					"startedByName": "Ana",
					"messageCount":  2,
				},
			})
		case strings.HasSuffix(r.URL.Path, "/messages"):
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "m1", "sequenceNumber": 1, "authorKind": "Support", "body": "bom dia", "createdAt": "2026-01-01T10:00:00Z", "attachments": []any{}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	svc := New(Deps{
		Logf: func(string) {},
		GetDebugConfig: func() DebugConfig {
			return DebugConfig{
				AgentID:   testAgentID,
				ApiScheme: "http",
				ApiServer: strings.TrimPrefix(server.URL, "http://"),
				AuthToken: "mdz_test",
			}
		},
		Emit: func(string, ...any) {},
	})

	state, err := svc.Refresh(t.Context())
	if err != nil {
		t.Fatalf("refresh falhou: %v", err)
	}
	if state.Status != StatusActive || state.SessionID != testSessionID || state.SupportName != "Ana" {
		t.Fatalf("estado após refresh inesperado: %+v", state)
	}

	messages, err := svc.ListMessages(t.Context(), 0, 10)
	if err != nil {
		t.Fatalf("list messages falhou: %v", err)
	}
	if len(messages) != 1 || messages[0].Body != "bom dia" {
		t.Fatalf("mensagens inesperadas: %+v", messages)
	}
}

// ── Regressões dos bugs encontrados na revisão ──────────────────────────────

// BUGFIX 1: Refresh precisa trazer as mensagens. Sem isso, um agent reiniciado
// no meio da conversa reabriria a janela VAZIA (o consumer durável já tinha
// confirmado os offsets antigos).
func TestRefresh_LoadsMessagesFromSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/active"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"hasSession": true,
				"session":    map[string]any{"id": testSessionID, "ticketId": testTicketID, "status": "Active"},
			})
		case strings.HasSuffix(r.URL.Path, "/messages"):
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "m1", "sequenceNumber": 1, "authorKind": "Support", "body": "antes do restart", "createdAt": "2026-01-01T10:00:00Z", "attachments": []any{}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	svc := servicePointingAt(server.URL)

	state, err := svc.Refresh(t.Context())
	if err != nil {
		t.Fatalf("refresh falhou: %v", err)
	}

	if len(state.Messages) != 1 || state.Messages[0].Body != "antes do restart" {
		t.Fatalf("Refresh deveria carregar o histórico da sessão ativa, obteve %+v", state.Messages)
	}
}

// BUGFIX 2: a bolha otimista (sequence 0) não pode duplicar quando o snapshot
// canônico chega.
func TestMergeMessages_DropsOptimisticPlaceholders(t *testing.T) {
	svc := newTestService()

	svc.mu.Lock()
	svc.state.SessionID = testSessionID
	svc.state.HasSession = true
	svc.state.Messages = []Message{
		{ID: "local-1", SequenceNumber: 0, AuthorKind: "enduser", Body: "enviada agora"},
	}
	svc.mu.Unlock()

	svc.mergeMessages([]Message{
		{ID: "canonica-1", SequenceNumber: 1, AuthorKind: "EndUser", Body: "enviada agora", CreatedAt: "2026-01-01T10:00:00Z"},
	})

	messages := svc.State().Messages
	if len(messages) != 1 {
		t.Fatalf("esperava 1 mensagem canônica, obteve %d: %+v", len(messages), messages)
	}
	if messages[0].SequenceNumber != 1 {
		t.Fatalf("a mensagem canônica deveria substituir o placeholder: %+v", messages[0])
	}
}

// BUGFIX 3: recusar um convite NÃO pode reescrever a sessão ativa (recusa por
// "busy" acontece com outro chat em andamento).
func TestRespondInviteForSession_DoesNotTouchCurrentSession(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	svc := servicePointingAt(server.URL)

	// Conversa ativa (mesma sessão dos envelopes).
	svc.PrepareInvite(Invite{SessionID: testSessionID, TicketID: testTicketID, SupportName: "Ana"})
	_ = svc.HandleEnvelope(messageEnvelope("m1", "conversa em andamento", 1))

	// Chega um convite de OUTRO chamado enquanto a conversa está ativa.
	if err := svc.RespondInviteForSession(t.Context(), "sessao-nova", false, "busy"); err != nil {
		t.Fatalf("recusa falhou: %v", err)
	}

	if !strings.Contains(gotPath, "sessao-nova") {
		t.Fatalf("a recusa deveria ir para a sessão do convite, foi para %q", gotPath)
	}

	state := svc.State()
	if state.SessionID != testSessionID {
		t.Fatalf("sessão ativa foi sobrescrita: %q", state.SessionID)
	}
	if len(state.Messages) != 1 {
		t.Fatalf("mensagens da conversa ativa foram perdidas: %+v", state.Messages)
	}
}

// Replay de uma sessão ANTERIOR não pode sequestrar a conversa ativa.
func TestHandleEnvelope_IgnoresEnvelopeFromOtherSession(t *testing.T) {
	svc := newTestService()

	svc.PrepareInvite(Invite{SessionID: testSessionID, TicketID: testTicketID})
	_ = svc.HandleEnvelope(messageEnvelope("m1", "conversa atual", 1))

	// Envelope atrasado de uma sessão antiga (replay do consumer durável).
	stale, _ := json.Marshal(Envelope{
		EventType:       EventMessage,
		SessionID:       "sessao-anterior",
		TicketID:        testTicketID,
		AuthorKind:      "support",
		Body:            "conversa antiga",
		Sequence:        9,
		ClientMessageID: "stale-1",
	})
	if err := svc.HandleEnvelope(stale); err != nil {
		t.Fatalf("envelope antigo não deveria falhar: %v", err)
	}

	state := svc.State()
	if state.SessionID != testSessionID {
		t.Fatalf("sessão ativa foi sequestrada: %q", state.SessionID)
	}
	if len(state.Messages) != 1 || state.Messages[0].Body != "conversa atual" {
		t.Fatalf("conversa misturada com sessão antiga: %+v", state.Messages)
	}
}

func servicePointingAt(rawURL string) *Service {
	base := strings.TrimPrefix(strings.TrimPrefix(rawURL, "http://"), "https://")
	return New(Deps{
		Logf: func(string) {},
		GetDebugConfig: func() DebugConfig {
			return DebugConfig{AgentID: testAgentID, ApiScheme: "http", ApiServer: base, AuthToken: "mdz_test"}
		},
		Emit: func(string, ...any) {},
	})
}

