package supportchat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
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

// BUGFIX 2 (revisado): a bolha otimista só é substituída quando a mensagem
// canônica CORRESPONDENTE chega (casada por clientMessageId).
func TestMergeMessages_ReplacesOptimisticOnlyWhenConfirmed(t *testing.T) {
	svc := newTestService()

	svc.mu.Lock()
	svc.state.SessionID = testSessionID
	svc.state.HasSession = true
	svc.state.Messages = []Message{
		{ID: "local-1", ClientMessageID: "local-1", SequenceNumber: 0, AuthorKind: "enduser", Body: "enviada agora"},
	}
	svc.mu.Unlock()

	// Snapshot que ainda NÃO contém a mensagem (publish assíncrono por NATS):
	// descartar a otimista aqui faria a mensagem recém-enviada desaparecer da
	// janela para sempre — o agent não recebe de volta as próprias mensagens.
	svc.mergeMessages([]Message{
		{ID: "outra", ClientMessageID: "outra", SequenceNumber: 1, AuthorKind: "Support", Body: "anterior", CreatedAt: "2026-01-01T10:00:00Z"},
	})

	state := svc.State()
	if len(state.Messages) != 2 {
		t.Fatalf("a bolha otimista deveria sobreviver ao snapshot parcial, obteve %+v", state.Messages)
	}

	// Chega a versão persistida da MESMA mensagem → a otimista sai (sem duplicar).
	svc.mergeMessages([]Message{
		{ID: "canonica-2", ClientMessageID: "local-1", SequenceNumber: 2, AuthorKind: "EndUser", Body: "enviada agora", CreatedAt: "2026-01-01T10:00:05Z"},
	})

	state = svc.State()
	if len(state.Messages) != 2 {
		t.Fatalf("esperava 2 mensagens (1 support + 1 canônica), obteve %d: %+v", len(state.Messages), state.Messages)
	}
	for _, message := range state.Messages {
		if message.SequenceNumber == 0 {
			t.Fatalf("a bolha otimista deveria ter sido substituída: %+v", state.Messages)
		}
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

// A transcrição em memória não pode crescer sem limite (o histórico canônico é
// do servidor; o agent não deve acumular conversa).
func TestMergeMessages_CapsInMemoryTranscript(t *testing.T) {
	svc := newTestService()

	batch := make([]Message, 0, maxInMemoryMessages+120)
	for i := 1; i <= maxInMemoryMessages+120; i++ {
		batch = append(batch, Message{
			ID:              "m" + strconv.Itoa(i),
			ClientMessageID: "m" + strconv.Itoa(i),
			SequenceNumber:  int64(i),
			AuthorKind:      "Support",
			Body:            "mensagem",
			CreatedAt:       "2026-01-01T10:00:00Z",
		})
	}

	svc.mergeMessages(batch)

	messages := svc.State().Messages
	if len(messages) != maxInMemoryMessages {
		t.Fatalf("esperava o teto de %d mensagens, obteve %d", maxInMemoryMessages, len(messages))
	}
	if messages[len(messages)-1].SequenceNumber != int64(maxInMemoryMessages+120) {
		t.Fatalf("a poda deve manter as mensagens MAIS RECENTES: última=%d", messages[len(messages)-1].SequenceNumber)
	}
	if messages[0].SequenceNumber != 121 {
		t.Fatalf("a poda deve descartar as mais antigas: primeira=%d", messages[0].SequenceNumber)
	}
}

// BUG CORRIGIDO: ao iniciar uma NOVA conversa as mensagens da anterior ficavam
// no estado e se misturavam com as novas (com sequence_number colidindo entre
// sessões, a ordem saía embaralhada).
func TestPrepareInvite_NewSessionDropsPreviousTranscript(t *testing.T) {
	svc := newTestService()

	// A sessão "antiga" é a mesma dos envelopes do helper.
	svc.PrepareInvite(Invite{SessionID: testSessionID, TicketID: testTicketID, SupportName: "Ana"})
	_ = svc.HandleEnvelope(messageEnvelope("a1", "mensagem da conversa antiga", 1))

	if got := len(svc.State().Messages); got != 1 {
		t.Fatalf("preparo do teste falhou: %d mensagens", got)
	}

	// Nova conversa (sessão diferente): a transcrição anterior precisa sair.
	svc.PrepareInvite(Invite{SessionID: "sessao-nova", TicketID: testTicketID, SupportName: "Ana"})

	state := svc.State()
	if state.SessionID != "sessao-nova" {
		t.Fatalf("sessão não trocou: %q", state.SessionID)
	}
	if len(state.Messages) != 0 {
		t.Fatalf("a conversa anterior deveria ter sido descartada, obteve %+v", state.Messages)
	}
}

// O convite REENVIADO da MESMA sessão (retry) não pode apagar a conversa.
func TestPrepareInvite_SameSessionKeepsTranscript(t *testing.T) {
	svc := newTestService()

	svc.PrepareInvite(Invite{SessionID: testSessionID, TicketID: testTicketID})
	_ = svc.HandleEnvelope(messageEnvelope("m1", "mensagem atual", 1))

	svc.PrepareInvite(Invite{SessionID: testSessionID, TicketID: testTicketID})

	if got := len(svc.State().Messages); got != 1 {
		t.Fatalf("retry da mesma sessão não pode limpar a conversa: %d mensagens", got)
	}
}

// O snapshot REST que devolve OUTRA sessão também precisa descartar a anterior.
func TestRefresh_NewSessionDropsPreviousTranscript(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/active"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"hasSession": true,
				"session":    map[string]any{"id": "sessao-nova", "ticketId": testTicketID, "status": "Active"},
			})
		case strings.HasSuffix(r.URL.Path, "/messages"):
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	svc := servicePointingAt(server.URL)

	svc.PrepareInvite(Invite{SessionID: "sessao-antiga", TicketID: testTicketID})
	_ = svc.HandleEnvelope(messageEnvelope("a1", "conversa antiga", 1))

	if _, err := svc.Refresh(t.Context()); err != nil {
		t.Fatalf("refresh falhou: %v", err)
	}

	state := svc.State()
	if state.SessionID != "sessao-nova" {
		t.Fatalf("sessão não trocou: %q", state.SessionID)
	}
	if len(state.Messages) != 0 {
		t.Fatalf("a conversa da sessão antiga deveria ter sido descartada: %+v", state.Messages)
	}
}

// Depois que a conversa termina, a reconciliação REST continua rodando (startup,
// reconexão do NATS e o ticker de 60s). Se ela zerar o estado local, a janela
// perde a conversa que o usuário ainda está lendo — inclusive a nota de sistema
// com quem encerrou. O agent NÃO guarda histórico: preservar isso é só manter em
// memória a sessão encerrada enquanto a janela está aberta.
func TestRefresh_KeepsEndedTranscriptWhenServerHasNoSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/active") {
			_ = json.NewEncoder(w).Encode(map[string]any{"hasSession": false})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	svc := servicePointingAt(server.URL)

	if _, err := svc.Refresh(t.Context()); err != nil {
		t.Fatalf("refresh inicial falhou: %v", err)
	}

	svc.PrepareInvite(Invite{SessionID: testSessionID, TicketID: testTicketID})
	_ = svc.HandleEnvelope(messageEnvelope("m1", "conversa", 1))

	ended, _ := json.Marshal(Envelope{
		EventType: EventSessionEnded,
		SessionID: testSessionID,
		TicketID:  testTicketID,
	})
	if err := svc.HandleEnvelope(ended); err != nil {
		t.Fatalf("session-ended falhou: %v", err)
	}

	if state := svc.State(); state.Status != StatusEnded {
		t.Fatalf("estado deveria ser ended, obteve %q", state.Status)
	}

	state, err := svc.Refresh(t.Context())
	if err != nil {
		t.Fatalf("refresh falhou: %v", err)
	}
	if state.Status != StatusEnded {
		t.Fatalf("a reconciliação zerou uma conversa encerrada: status %q", state.Status)
	}
	if len(state.Messages) != 1 {
		t.Fatalf("a conversa encerrada deveria continuar legível: %+v", state.Messages)
	}

	// Serviço sem conversa encerrada continua sendo zerado (sem histórico local).
	fresh := servicePointingAt(server.URL)
	fresh.PrepareInvite(Invite{SessionID: "outra-sessao", TicketID: testTicketID})
	_ = fresh.HandleEnvelope(messageEnvelope("x1", "rascunho", 1))
	cleared, err := fresh.Refresh(t.Context())
	if err != nil {
		t.Fatalf("refresh do serviço novo falhou: %v", err)
	}
	if cleared.Status != StatusNone || len(cleared.Messages) != 0 {
		t.Fatalf("sem sessão ativa o estado deveria zerar: %+v", cleared)
	}
}

// A mensagem de SISTEMA do encerramento chega pelo canal com authorKind
// "system": o authorKind precisa sobreviver ao ingest para a UI desenhar a nota
// de encerramento em vez de uma bolha de conversa.
func TestHandleEnvelope_KeepsSystemAuthorKind(t *testing.T) {
	svc := newTestService()

	payload, _ := json.Marshal(Envelope{
		EventType:  EventMessage,
		SessionID:  testSessionID,
		TicketID:   testTicketID,
		AuthorKind: "system",
		Body:       "Chat de suporte encerrado pelo usuário",
		Sequence:   7,
	})
	if err := svc.HandleEnvelope(payload); err != nil {
		t.Fatalf("envelope falhou: %v", err)
	}

	messages := svc.State().Messages
	if len(messages) != 1 {
		t.Fatalf("esperava 1 mensagem, obteve %d", len(messages))
	}
	if messages[0].AuthorKind != "system" {
		t.Fatalf("authorKind da mensagem de sistema foi perdido: %q", messages[0].AuthorKind)
	}
}

// BUG CORRIGIDO: o envelope do NATS entrega os anexos apenas por ID. Se o ID
// não sobreviver ao ingest, o print do suporte some da conversa (foi o caso
// relatado: "o suporte manda a imagem e ela não aparece para o usuário").
func TestHandleEnvelope_KeepsAttachmentIds(t *testing.T) {
	svc := newTestService()

	attachmentID := "11111111-2222-3333-4444-555555555555"
	payload, _ := json.Marshal(Envelope{
		EventType:       EventMessage,
		SessionID:       testSessionID,
		TicketID:        testTicketID,
		AuthorKind:      "support",
		Body:            "segue o print",
		Sequence:        4,
		ClientMessageID: "m-print",
		AttachmentIDs:   []string{attachmentID},
	})
	if err := svc.HandleEnvelope(payload); err != nil {
		t.Fatalf("envelope falhou: %v", err)
	}

	messages := svc.State().Messages
	if len(messages) != 1 || len(messages[0].Attachments) != 1 {
		t.Fatalf("o anexo do envelope foi perdido: %+v", messages)
	}
	if messages[0].Attachments[0].ID != attachmentID {
		t.Fatalf("id do anexo inesperado: %+v", messages[0].Attachments)
	}
}

// BUG CORRIGIDO: o snapshot REST só completava nome/tipo/tamanho quando a
// mensagem não tinha NENHUM anexo. Com o envelope passando a trazer os IDs, o
// anexo ficaria eternamente exibido como "anexo" na janela.
func TestMergeMessages_EnrichesAttachmentMetadataById(t *testing.T) {
	svc := newTestService()

	svc.mu.Lock()
	svc.state.SessionID = testSessionID
	svc.state.HasSession = true
	svc.state.Messages = []Message{{
		ID:              "m-print",
		ClientMessageID: "m-print",
		SequenceNumber:  4,
		AuthorKind:      "support",
		Body:            "segue o print",
		CreatedAt:       "2026-01-01T10:00:00Z",
		Attachments:     []Attachment{{ID: "att-1"}},
	}}
	svc.mu.Unlock()

	// Snapshot REST: mesma sequência, agora com os metadados resolvidos.
	svc.mergeMessages([]Message{{
		ID:              "m-print",
		ClientMessageID: "m-print",
		SequenceNumber:  4,
		AuthorKind:      "support",
		Body:            "segue o print",
		CreatedAt:       "2026-01-01T10:00:00Z",
		Attachments: []Attachment{{
			ID:          "att-1",
			FileName:    "print.png",
			ContentType: "image/png",
			SizeBytes:   2048,
			UploadedBy:  "suporte",
		}},
	}})

	messages := svc.State().Messages
	if len(messages) != 1 {
		t.Fatalf("esperava 1 mensagem, obteve %d: %+v", len(messages), messages)
	}
	attachment := messages[0].Attachments[0]
	if attachment.FileName != "print.png" || attachment.ContentType != "image/png" || attachment.SizeBytes != 2048 {
		t.Fatalf("metadados do anexo não foram enriquecidos: %+v", attachment)
	}
}

// BUG CORRIGIDO: mensagem SÓ com anexo (print sem legenda) era recusada como
// "mensagem vazia" — as duas UIs permitem esse envio.
func TestSendMessage_AllowsAttachmentOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/messages") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":             "m1",
				"sequenceNumber": 1,
				"authorKind":     "enduser",
				"body":           "",
				"createdAt":      "2026-01-01T10:00:00Z",
				"attachments": []map[string]any{{
					"id":          "att-1",
					"fileName":    "print.png",
					"contentType": "image/png",
				}},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	svc := servicePointingAt(server.URL)
	svc.PrepareInvite(Invite{SessionID: testSessionID, TicketID: testTicketID})

	// Sem canal NATS (nc nil), o envio cai para o REST — é justamente o caminho
	// de fallback. O importante aqui é o guard: corpo vazio + anexo é aceito.
	if err := svc.SendMessage(t.Context(), "", []string{"att-1"}); err != nil {
		t.Fatalf("mensagem só com anexo deveria ser aceita, obteve %v", err)
	}

	messages := svc.State().Messages
	if len(messages) != 1 || len(messages[0].Attachments) != 1 {
		t.Fatalf("anexo perdido no envio só com anexo: %+v", messages)
	}
	if messages[0].Attachments[0].FileName != "print.png" {
		t.Fatalf("metadados do anexo não vieram do snapshot: %+v", messages[0].Attachments)
	}
}

// Corpo vazio SEM anexo continua sendo recusado.
func TestSendMessage_RejectsEmptyBodyWithoutAttachment(t *testing.T) {
	svc := newTestService()
	svc.PrepareInvite(Invite{SessionID: testSessionID, TicketID: testTicketID})

	err := svc.SendMessage(t.Context(), "   ", nil)
	if err == nil || !strings.Contains(err.Error(), "mensagem vazia") {
		t.Fatalf("corpo vazio sem anexo deveria ser recusado, obteve %v", err)
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
