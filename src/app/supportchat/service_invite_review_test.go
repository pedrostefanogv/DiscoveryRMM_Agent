package supportchat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// ── Convite: decisão atômica (revisão 2026-10-18) ──────────────────────────
//
// O handler do agent fazia "checa Estado() e depois PrepareInvite". Dois
// convites chegando quase juntos passavam os dois pela checagem e o segundo
// sobrescrevia o estado do primeiro: o servidor ficava esperando resposta de um
// convite que o usuário nunca viu. PrepareInviteIfIdle devolve a decisão já com
// a corrida resolvida dentro do lock.

func openActiveSession(t *testing.T, svc *Service, sessionID string) {
	t.Helper()

	opened, err := json.Marshal(Envelope{EventType: EventSessionOpened, SessionID: sessionID, TicketID: testTicketID})
	if err != nil {
		t.Fatalf("marshal do SessionOpened falhou: %v", err)
	}
	if err := svc.HandleEnvelope(opened); err != nil {
		t.Fatalf("SessionOpened falhou: %v", err)
	}
}

func TestPrepareInviteIfIdle_ReentregaDeConversaAceitaNaoReabreConvite(t *testing.T) {
	svc := newTestService()

	if decision := svc.PrepareInviteIfIdle(Invite{SessionID: testSessionID, TicketID: testTicketID}); !decision.Accepted {
		t.Fatalf("primeiro convite deveria ser aceito: %+v", decision)
	}
	openActiveSession(t, svc, testSessionID)

	// O servidor reentrega o mesmo comando (Nats:CommandRedelivery) depois do
	// aceite: não pode voltar para "waiting" (o modal de confirmação reaparecia
	// em cima da conversa em curso).
	decision := svc.PrepareInviteIfIdle(Invite{SessionID: testSessionID, TicketID: testTicketID})
	if decision.Accepted {
		t.Fatal("reentrega de convite já aceito não pode ser aceita")
	}
	if decision.Reason != InviteReasonAlreadyActive {
		t.Fatalf("motivo esperado %q, obtido %q", InviteReasonAlreadyActive, decision.Reason)
	}

	state := svc.State()
	if state.Status != StatusActive {
		t.Fatalf("o convite reentregue rebaixou a conversa para %q", state.Status)
	}
	if state.SessionID != testSessionID {
		t.Fatalf("sessão alterada: %q", state.SessionID)
	}
}

func TestPrepareInviteIfIdle_RecusaQuandoOutraConversaEstaAtiva(t *testing.T) {
	svc := newTestService()

	svc.PrepareInviteIfIdle(Invite{SessionID: testSessionID, TicketID: testTicketID})
	openActiveSession(t, svc, testSessionID)

	decision := svc.PrepareInviteIfIdle(Invite{SessionID: "outra-sessao", TicketID: testTicketID})
	if decision.Accepted {
		t.Fatal("convite de outra sessão não pode ser aceito com conversa ativa")
	}
	if decision.Reason != InviteReasonBusy {
		t.Fatalf("motivo esperado %q, obtido %q", InviteReasonBusy, decision.Reason)
	}

	state := svc.State()
	if state.Status != StatusActive || state.SessionID != testSessionID {
		t.Fatalf("o convite recusado mexeu na conversa ativa: %+v", state)
	}
}

func TestPrepareInviteIfIdle_SubstituiConviteAnteriorPendente(t *testing.T) {
	svc := newTestService()

	first := svc.PrepareInviteIfIdle(Invite{SessionID: "sessao-antiga", TicketID: testTicketID, SupportName: "Ana"})
	if !first.Accepted || first.SupersededSessionID != "" {
		t.Fatalf("primeiro convite inesperado: %+v", first)
	}

	second := svc.PrepareInviteIfIdle(Invite{SessionID: "sessao-nova", TicketID: testTicketID, SupportName: "Bruno"})
	if !second.Accepted {
		t.Fatalf("segundo convite deveria ser aceito (o anterior estava apenas aguardando): %+v", second)
	}
	// O convite substituído precisa ser RECUSADO no servidor pelo chamador — sem
	// isso ele fica "aguardando" até expirar e o atendente acha que o usuário
	// ignorou o pedido.
	if second.SupersededSessionID != "sessao-antiga" {
		t.Fatalf("sessão substituída deveria ser 'sessao-antiga', obtido %q", second.SupersededSessionID)
	}

	state := svc.State()
	if state.SessionID != "sessao-nova" || state.Status != StatusWaiting {
		t.Fatalf("estado esperado waiting/sessao-nova, obtido %+v", state)
	}
	if state.SupportName != "Bruno" {
		t.Fatalf("atendente do novo convite não aplicado: %q", state.SupportName)
	}
}

func TestPrepareInviteIfIdle_RecusaSessaoVazia(t *testing.T) {
	svc := newTestService()

	for _, invite := range []Invite{{}, {SessionID: "   "}} {
		decision := svc.PrepareInviteIfIdle(invite)
		if decision.Accepted || decision.Reason != InviteReasonInvalid {
			t.Fatalf("convite sem sessão deveria ser inválido: %+v", decision)
		}
	}
}

// ── Ordenação: otimistas sempre no fim ─────────────────────────────────────

// O comparador antigo devolvia "false" quando um dos lados não tinha sequência,
// o que não é uma ordenação válida (viola transitividade): numa mistura de
// bolhas otimistas e canônicas a mensagem recém-enviada podia ficar no meio.
func TestSortMessages_OtimistasVaoParaOFim(t *testing.T) {
	messages := []Message{
		{ID: "otimista-1"},
		{ID: "canonica-3", SequenceNumber: 3},
		{ID: "otimista-2"},
		{ID: "canonica-1", SequenceNumber: 1},
		{ID: "canonica-2", SequenceNumber: 2},
	}

	sortMessages(messages)

	want := []string{"canonica-1", "canonica-2", "canonica-3", "otimista-1", "otimista-2"}
	for i, id := range want {
		if messages[i].ID != id {
			t.Fatalf("posição %d deveria ser %q, obtido %q (lista: %+v)", i, id, messages[i].ID, messages)
		}
	}

	// Casos triviais não podem entrar em pânico.
	sortMessages(nil)
	sortMessages([]Message{{ID: "unica"}})
}

// ── Enriquecimento de anexos: só a janela recente ──────────────────────────

func serviceWithContextPointingAt(rawURL string) *Service {
	base := strings.TrimPrefix(strings.TrimPrefix(rawURL, "http://"), "https://")
	return New(Deps{
		Ctx:  func() context.Context { return context.Background() },
		Logf: func(string) {},
		GetDebugConfig: func() DebugConfig {
			return DebugConfig{AgentID: testAgentID, ApiScheme: "http", ApiServer: base, AuthToken: "mdz_test"}
		},
		Emit: func(string, ...any) {},
	})
}

// BUG/OTIMIZAÇÃO: a chegada de uma mensagem com anexo dispara um snapshot REST
// para completar nome/tipo/tamanho. Ele buscava até 300 mensagens (o histórico
// inteiro) a cada anexo recebido. O anexo está SEMPRE na janela recente, então a
// consulta agora é limitada a attachmentEnrichWindow/attachmentEnrichLimit.
func TestEnrichAttachmentsFromREST_ConsultaApenasJanelaRecente(t *testing.T) {
	var (
		mu       sync.Mutex
		gotAfter string
		gotLimit string
		hits     int
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/messages") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		mu.Lock()
		hits++
		gotAfter = r.URL.Query().Get("afterSequence")
		gotLimit = r.URL.Query().Get("limit")
		mu.Unlock()
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer server.Close()

	svc := serviceWithContextPointingAt(server.URL)
	svc.PrepareInvite(Invite{SessionID: testSessionID, TicketID: testTicketID})

	// Conversa com 100 mensagens canônicas: a última é a janela de referência.
	batch := make([]Message, 0, 100)
	for i := 1; i <= 100; i++ {
		batch = append(batch, Message{ID: fmt.Sprintf("m%d", i), SequenceNumber: int64(i)})
	}
	svc.mergeMessages(batch)

	svc.enrichAttachmentsFromREST(testSessionID)

	mu.Lock()
	defer mu.Unlock()

	if hits != 1 {
		t.Fatalf("esperava 1 consulta de enriquecimento, obtido %d", hits)
	}
	wantAfter := strconv.Itoa(100 - attachmentEnrichWindow)
	if gotAfter != wantAfter {
		t.Fatalf("afterSequence esperado %q (janela recente), obtido %q", wantAfter, gotAfter)
	}
	if gotLimit != strconv.Itoa(attachmentEnrichLimit) {
		t.Fatalf("limit esperado %q, obtido %q", strconv.Itoa(attachmentEnrichLimit), gotLimit)
	}
}

// ── Política de anexos vinda do servidor ───────────────────────────────────
//
// A janela montava o seletor de arquivos com um espelho HARDCODED da allowlist:
// o usuário escolhia um arquivo permitido pela configuração do servidor e só
// descobria o descompasso no envio. FetchAttachmentPolicy lê a política real
// (rota nova .../support-chat/attachment-policy) e cai no default quando a rota
// não existe (API mais antiga) — nunca deixa a janela sem política.
func TestFetchAttachmentPolicy_LeAllowlistDoServidor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/support-chat/attachment-policy") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"enabled":             false,
			"allowedContentTypes": []string{"image/png", "application/zip"},
			"maxFileSizeBytes":    2 * 1024 * 1024,
		})
	}))
	defer server.Close()

	svc := servicePointingAt(server.URL)

	policy, err := svc.FetchAttachmentPolicy(t.Context())
	if err != nil {
		t.Fatalf("política deveria ser lida: %v", err)
	}
	if policy.Enabled {
		t.Fatal("enabled=false do servidor foi ignorado")
	}
	if len(policy.AllowedContentTypes) != 2 || policy.AllowedContentTypes[1] != "application/zip" {
		t.Fatalf("allowlist do servidor não aplicada: %+v", policy.AllowedContentTypes)
	}
	if policy.MaxFileSizeBytes != 2*1024*1024 {
		t.Fatalf("tamanho máximo do servidor não aplicado: %d", policy.MaxFileSizeBytes)
	}
}

func TestFetchAttachmentPolicy_FalhaCaiNoPadrao(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	svc := servicePointingAt(server.URL)

	policy, err := svc.FetchAttachmentPolicy(t.Context())
	if err == nil {
		t.Fatal("esperava erro quando a rota não existe (API antiga)")
	}

	defaults := DefaultAttachmentPolicy()
	if policy.Enabled != defaults.Enabled ||
		policy.MaxFileSizeBytes != defaults.MaxFileSizeBytes ||
		len(policy.AllowedContentTypes) != len(defaults.AllowedContentTypes) {
		t.Fatalf("fallback deveria ser o padrão do servidor; obtido %+v", policy)
	}
}

// Resposta parcial/vazia não pode desligar o seletor nem zerar o limite.
func TestFetchAttachmentPolicy_RespostaParcialMantemDefaults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/support-chat/attachment-policy") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"enabled": true})
	}))
	defer server.Close()

	svc := servicePointingAt(server.URL)

	policy, err := svc.FetchAttachmentPolicy(t.Context())
	if err != nil {
		t.Fatalf("política parcial deveria ser aceita: %v", err)
	}
	defaults := DefaultAttachmentPolicy()
	if len(policy.AllowedContentTypes) == 0 {
		t.Fatal("allowlist vazia deixaria o seletor sem filtro")
	}
	if policy.MaxFileSizeBytes != defaults.MaxFileSizeBytes {
		t.Fatalf("limite ausente deveria cair no padrão; obtido %d", policy.MaxFileSizeBytes)
	}
}

// ── Guarda de replay com bolha otimista ────────────────────────────────────
//
// BUG LATENTE: a guarda de sequência do applyMessage comparava com a ÚLTIMA
// mensagem da lista. Como o envio do usuário insere uma bolha OTIMISTA
// (sequenceNumber = 0) no fim, a comparação nunca rejeitava nada e um envelope
// antigo (replay do consumer durável) era aplicado por cima da conversa.
func TestHandleEnvelope_RejeitaReplayAntigoMesmoComBolhaOtimista(t *testing.T) {
	svc := newTestService()

	if err := svc.HandleEnvelope(messageEnvelope("m5", "mensagem 5", 5)); err != nil {
		t.Fatalf("envelope inicial falhou: %v", err)
	}

	// Bolha otimista no fim, como o SendMessage faz antes da confirmação.
	svc.mu.Lock()
	svc.state.Messages = append(svc.state.Messages, Message{
		ID:              "otimista-1",
		ClientMessageID: "otimista-1",
		AuthorKind:      "enduser",
		Body:            "enviando...",
	})
	svc.mu.Unlock()

	// Replay de um envelope ANTIGO da mesma sessão: precisa ser descartado.
	if err := svc.HandleEnvelope(messageEnvelope("m2", "mensagem 2 (replay)", 2)); err != nil {
		t.Fatalf("replay não deveria falhar (é descartado silenciosamente): %v", err)
	}

	for _, message := range svc.State().Messages {
		if message.Body == "mensagem 2 (replay)" {
			t.Fatalf("replay antigo foi aplicado por cima da conversa: %+v", svc.State().Messages)
		}
	}
	if got := len(svc.State().Messages); got != 2 {
		t.Fatalf("lista deveria ter a canônica 5 + a otimista, obteve %d: %+v", got, svc.State().Messages)
	}
}

// BUG (revisão final): um convite REENTREGUE de uma conversa já encerrada voltava
// o estado para "waiting". A janela do chat, que fica aberta em leitura depois do
// fim, ganhava o modal de convite por cima da conversa encerrada — e voltava a
// flutuar sobre os outros apps.
func TestPrepareInviteIfIdle_NaoRessuscitaConversaEncerrada(t *testing.T) {
	svc := newTestService()

	svc.PrepareInvite(Invite{SessionID: testSessionID, TicketID: testTicketID})
	openActiveSession(t, svc, testSessionID)

	ended, _ := json.Marshal(Envelope{EventType: EventSessionEnded, SessionID: testSessionID, EndReason: "support_closed"})
	if err := svc.HandleEnvelope(ended); err != nil {
		t.Fatalf("SessionEnded falhou: %v", err)
	}
	if state := svc.State(); state.Status != StatusEnded {
		t.Fatalf("preparo do teste falhou: status %q", state.Status)
	}

	decision := svc.PrepareInviteIfIdle(Invite{SessionID: testSessionID, TicketID: testTicketID})
	if decision.Accepted {
		t.Fatalf("convite de conversa encerrada não pode ser aceito: %+v", decision)
	}
	if decision.Reason != InviteReasonAlreadyFinished {
		t.Fatalf("motivo esperado %q, obtido %q", InviteReasonAlreadyFinished, decision.Reason)
	}
	if state := svc.State(); state.Status != StatusEnded {
		t.Fatalf("o estado voltou para %q (modal reapareceria na conversa encerrada)", state.Status)
	}

	// Convite de uma conversa NOVA continua sendo aceito normalmente.
	decision = svc.PrepareInviteIfIdle(Invite{SessionID: "sessao-nova", TicketID: testTicketID})
	if !decision.Accepted {
		t.Fatalf("convite de sessão nova deveria ser aceito: %+v", decision)
	}
}
