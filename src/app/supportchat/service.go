package supportchat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"discovery/app/core/tlsutil"
)

const (
	// requestTimeout protege a UI de uma rede degradada; snapshot e sends são curtos.
	requestTimeout = 30 * time.Second
	// attachmentEnrichWindow/attachmentEnrichLimit limitam o refresh disparado
	// quando chega uma mensagem com anexo: o anexo recém-chegado está SEMPRE na
	// janela recente, então reconsultar até 300 mensagens a cada print enviava
	// a conversa inteira sem necessidade (vários prints = várias buscas completas).
	attachmentEnrichWindow = 20
	attachmentEnrichLimit  = 30
	// seenClientIDsLimit limita o mapa de deduplicação em memória.
	seenClientIDsLimit = 256
	// maxInMemoryMessages limita a transcrição mantida em memória.
	//
	// O histórico canônico é do servidor (e só consultável no console); aqui é
	// apenas a janela visível. Sem teto, uma conversa longa dentro de um turno
	// cresceria indefinidamente no processo — e o requisito é justamente NÃO
	// guardar conversa no agent.
	maxInMemoryMessages = 500
)

// Deps são as dependências injetadas pelo App.
type Deps struct {
	Ctx            func() context.Context
	Logf           func(string)
	GetDebugConfig func() DebugConfig
	Emit           func(name string, data ...any)
}

// Service mantém o estado do chat de suporte do agent.
//
// Sem persistência: todo o estado vive em memória e é descartado quando a
// janela do chat fecha (requisito "o agent não mostra histórico antigo").
type Service struct {
	deps Deps

	mu            sync.Mutex
	state         State
	seenClientIDs map[string]time.Time
	seenOrder     []string

	// waitingSince/activeSince alimentam o RESUMO de sessão no log (uma linha
	// por sessão em vez de só as transições). waitingSince = quando o convite
	// chegou; activeSince = quando o usuário aceitou (ou a sessão já veio ativa).
	waitingSince time.Time
	activeSince  time.Time

	natsMu     sync.Mutex
	nc         *nats.Conn
	serverSubj string
	agentSubj  string
	natsReady  bool
}

// New cria o serviço.
func New(deps Deps) *Service {
	logf := deps.Logf
	if logf == nil {
		logf = func(string) {}
	}
	emit := deps.Emit
	if emit == nil {
		emit = func(string, ...any) {}
	}
	return &Service{
		deps:          Deps{Ctx: deps.Ctx, Logf: logf, GetDebugConfig: deps.GetDebugConfig, Emit: emit},
		state:         State{Status: StatusNone, Messages: []Message{}},
		seenClientIDs: make(map[string]time.Time),
	}
}

// ── Estado ──────────────────────────────────────────────────────────────────

// State devolve uma cópia do estado atual (segura para serialização).
func (s *Service) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.copyStateLocked()
}

func (s *Service) copyStateLocked() State {
	state := s.state
	state.Connected = s.natsReady
	messages := make([]Message, len(state.Messages))
	copy(messages, state.Messages)
	state.Messages = messages
	return state
}

func (s *Service) emitState(reason string) {
	s.mu.Lock()
	state := s.copyStateLocked()
	s.mu.Unlock()

	s.deps.Logf(fmt.Sprintf("[supportchat] estado -> %s/%s (%s)", state.Status, state.SessionID, reason))
	s.deps.Emit("supportchat:state", state)
}

func (s *Service) emitError(message string) {
	s.mu.Lock()
	s.state.LastError = message
	s.mu.Unlock()
	s.deps.Emit("supportchat:error", message)
}

// ── Ciclo de vida NATS ──────────────────────────────────────────────────────

// OnNATSConnected registra a conexão e os subjects do chat.
//
// NÃO assina o canal servidor -> agent: a assinatura (consumer JetStream
// durável) é responsabilidade do agentconn, que é o dono das subscriptions.
//
// BUG CORRIGIDO: existiam DUAS assinaturas — esta e a do agentconn — com o
// mesmo durable. O broker responde "consumer is already bound to a
// subscription", o agent caía para core NATS sem replay e cada mensagem era
// processada duas vezes (o serviço chegava a emitir "session-ended" em
// duplicidade).
func (s *Service) OnNATSConnected(nc *nats.Conn, clientID, siteID, agentID string) {
	if nc == nil {
		return
	}

	clientID = strings.TrimSpace(clientID)
	siteID = strings.TrimSpace(siteID)
	agentID = strings.TrimSpace(agentID)
	if clientID == "" || siteID == "" || agentID == "" {
		s.deps.Logf("[supportchat] identidade NATS incompleta; canal do chat não será assinado")
		return
	}

	prefix := fmt.Sprintf("tenant.%s.site.%s.agent.%s", clientID, siteID, agentID)
	serverSubject := prefix + ".supportchat.server"
	agentSubject := prefix + ".supportchat.agent"

	s.natsMu.Lock()
	s.nc = nc
	s.serverSubj = serverSubject
	s.agentSubj = agentSubject
	s.natsReady = true
	s.natsMu.Unlock()

	s.deps.Logf("[supportchat] conexão NATS registrada (assinatura do canal é do agentconn)")
	s.emitState("nats-connected")
}

// OnNATSDisconnected limpa a conexão (a janela mostra o banner de reconexão).
func (s *Service) OnNATSDisconnected() {
	s.natsMu.Lock()
	s.nc = nil
	s.natsReady = false
	s.natsMu.Unlock()

	s.emitState("nats-disconnected")
}

// ── Ingestão dos envelopes do servidor ──────────────────────────────────────

// HandleEnvelope processa um envelope servidor -> agent.
func (s *Service) HandleEnvelope(data []byte) error {
	var envelope Envelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		s.emitError("Envelope inválido recebido do servidor.")
		return fmt.Errorf("supportchat: envelope inválido: %w", err)
	}

	switch strings.TrimSpace(envelope.EventType) {
	case EventSessionOpened:
		s.applySessionOpened(envelope)
		return nil

	case EventMessage:
		return s.applyMessage(envelope)

	case EventSessionEnded:
		s.applySessionEnded(envelope)
		return nil

	case EventSessionDeclined:
		s.applySessionDeclined(envelope)
		return nil

	default:
		// Eventos desconhecidos são confirmados (Ack) para não travar o consumer.
		s.deps.Logf("[supportchat] envelope ignorado eventType=" + envelope.EventType)
		return nil
	}
}

func (s *Service) applySessionOpened(envelope Envelope) {
	s.mu.Lock()

	// Mesma guarda do applyMessage: não troca a conversa ativa por replay de
	// uma sessão anterior.
	if envelope.SessionID != "" && s.state.SessionID != "" && envelope.SessionID != s.state.SessionID {
		s.mu.Unlock()
		s.deps.Logf("[supportchat] SessionOpened de outra sessão ignorado: " + envelope.SessionID)
		return
	}

	s.resetTranscriptForSessionLocked(envelope.SessionID)
	s.state.HasSession = true
	s.state.SessionID = envelope.SessionID
	s.state.TicketID = envelope.TicketID
	s.state.Status = StatusActive
	if s.activeSince.IsZero() {
		s.activeSince = time.Now()
	}
	if s.state.Messages == nil {
		s.state.Messages = []Message{}
	}
	s.mu.Unlock()

	s.emitState("session-opened")
}

func (s *Service) applySessionEnded(envelope Envelope) {
	// Motivo do fim quando o servidor manda um (ex.: "expired" — a expiração do
	// convite chega por este evento, não por um livestream próprio). Só entra no
	// LOG: o estado final é o mesmo para qualquer motivo.
	reason := "servidor"
	if endReason := strings.TrimSpace(envelope.EndReason); endReason != "" {
		reason = "servidor/" + endReason
	}

	s.mu.Lock()

	// Mesma guarda de sessão dos outros eventos: um SessionEnded REENTREGUE
	// (AckWait) de uma conversa anterior encerraria a conversa ativa.
	if envelope.SessionID != "" && s.state.SessionID != "" && envelope.SessionID != s.state.SessionID {
		s.mu.Unlock()
		s.deps.Logf("[supportchat] SessionEnded de outra sessão ignorado: " + envelope.SessionID)
		return
	}

	// Terminal em QUALQUER estado: um convite em "waiting" que o servidor
	// encerra/expira não pode continuar "aguardando confirmação" (era o convite
	// eterno na tela e no console).
	s.state.Status = StatusEnded
	s.state.HasSession = false
	s.state.SessionID = envelope.SessionID
	s.mu.Unlock()

	// Resumo único da sessão (duração + volume): o log do agent já mostrava cada
	// transição, mas reconstruir "quanto durou / quantas mensagens" exigia ler o
	// histórico inteiro do logstore durante um atendimento.
	s.logSessionClosed(envelope.SessionID, reason)
	s.emitState("session-ended")
}

// applySessionDeclined aplica o evento durável de RECUSA/EXPIRAÇÃO do convite.
//
// Por que existe: a recusa só era conhecida pelo próprio processo que fez o
// POST. Se o usuário recusava no cartão e o POST falhava (rede), ou se quem
// recusava era outra máquina/expiração do servidor, o agent ficava em
// "waiting" — cartão na tela e console "Aguardando confirmação" — até o
// polling REST. O servidor agora publica SessionDeclined no MESMO canal
// JetStream durável (DeliverAll) dos demais eventos.
//
// A guarda de sessão é a mesma de applySessionEnded: replay de outra sessão
// (ou de uma conversa já anterior) NÃO pode derrubar a sessão corrente.
func (s *Service) applySessionDeclined(envelope Envelope) {
	reason := "servidor"
	if declineReason := strings.TrimSpace(envelope.Reason); declineReason != "" {
		reason = "servidor/" + declineReason
	} else if endReason := strings.TrimSpace(envelope.EndReason); endReason != "" {
		reason = "servidor/" + endReason
	}

	s.mu.Lock()

	if envelope.SessionID != "" && s.state.SessionID != "" && envelope.SessionID != s.state.SessionID {
		s.mu.Unlock()
		s.deps.Logf("[supportchat] SessionDeclined de outra sessão ignorado: " + envelope.SessionID)
		return
	}

	// Já terminal e sem sessão: replay do mesmo evento (at-least-once). Nada a
	// fazer — reemitir aqui só geraria um log/estado duplicado.
	if s.state.Status == StatusEnded && s.state.SessionID == envelope.SessionID && !s.state.HasSession {
		s.mu.Unlock()
		return
	}

	// A conversa ATIVA de outro id cai na guarda acima; a do MESMO id não pode
	// ser rebaixada por um decline atrasado (ex.: declínio de um convite anterior
	// da mesma máquina chegando depois do aceite).
	if s.state.Status == StatusActive && envelope.SessionID != "" && s.state.SessionID == envelope.SessionID {
		s.mu.Unlock()
		s.deps.Logf("[supportchat] SessionDeclined ignorado: a sessão já está ativa (" + envelope.SessionID + ")")
		return
	}

	// Defensivo: sem SessionID não há como saber QUAL convite foi recusado.
	// Sobrescrever a sessão corrente com vazio apagaria a referência de uma
	// conversa legítima — melhor manter o estado e só registrar.
	if strings.TrimSpace(envelope.SessionID) == "" {
		s.mu.Unlock()
		s.deps.Logf("[supportchat] SessionDeclined sem sessionId ignorado (estado preservado)")
		return
	}

	// Terminal para o convite: nunca deixar "waiting".
	s.state.Status = StatusEnded
	s.state.HasSession = false
	s.state.SessionID = envelope.SessionID
	s.mu.Unlock()

	s.logSessionClosed(envelope.SessionID, reason)
	s.emitState("session-declined")
}

func (s *Service) applyMessage(envelope Envelope) error {
	// Deduplicação por clientMessageId (entrega at-least-once) e por sequência.
	if envelope.ClientMessageID != "" && s.alreadySeen(envelope.ClientMessageID) {
		return nil
	}

	s.mu.Lock()
	if s.state.Messages == nil {
		s.state.Messages = []Message{}
	}
	// BUG LATENTE CORRIGIDO: a guarda olhava a ÚLTIMA mensagem da lista. Com uma
	// bolha OTIMISTA no fim (sequência 0), a comparação nunca rejeitava nada e um
	// envelope antigo (replay do consumer durável) era aplicado por cima da
	// conversa. A referência tem de ser a MAIOR sequência canônica conhecida.
	if envelope.Sequence > 0 {
		if last := s.maxCanonicalSequenceLocked(); last > 0 && envelope.Sequence <= last {
			s.mu.Unlock()
			return nil
		}
	}
	// Guarda de sessão: durante o replay do consumer durável podem chegar
	// envelopes de uma sessão ANTERIOR. Aplicá-los trocaria a sessão corrente e
	// misturaria conversas — descarta.
	if envelope.SessionID != "" && s.state.SessionID != "" && envelope.SessionID != s.state.SessionID {
		s.mu.Unlock()
		s.deps.Logf("[supportchat] envelope de sessão diferente ignorado (replay): " + envelope.SessionID)
		return nil
	}
	if envelope.SessionID != "" {
		s.state.SessionID = envelope.SessionID
	}
	if s.state.Status == StatusNone {
		s.state.Status = StatusActive
	}

	attachments := make([]Attachment, 0, len(envelope.AttachmentIDs))
	for _, id := range envelope.AttachmentIDs {
		attachments = append(attachments, Attachment{ID: id})
	}

	s.state.Messages = append(s.state.Messages, Message{
		ID:              envelope.ClientMessageID,
		ClientMessageID: envelope.ClientMessageID,
		SequenceNumber:  envelope.Sequence,
		AuthorKind:      strings.TrimSpace(envelope.AuthorKind),
		AuthorName:      envelope.AuthorName,
		Body:            envelope.Body,
		CreatedAt:       envelope.TimestampUTC,
		Attachments:     attachments,
	})
	s.trimMessagesLocked()
	s.mu.Unlock()

	s.emitState("message")

	// O envelope do NATS carrega só os IDs dos anexos. Os metadados (nome, tipo,
	// tamanho) chegam no snapshot REST: sem esta busca imediata o anexo ficaria
	// exibido como "anexo" até a próxima reconciliação (até 60s).
	if len(envelope.AttachmentIDs) > 0 {
		go s.enrichAttachmentsFromREST(envelope.SessionID)
	}
	return nil
}

// enrichAttachmentsFromREST busca o snapshot da sessão para completar os
// metadados dos anexos entregues pelo envelope apenas com o ID.
//
// Best-effort e assíncrono: não bloqueia o consumidor do JetStream; se falhar,
// a reconciliação periódica da janela ainda completa os dados depois.
func (s *Service) enrichAttachmentsFromREST(sessionID string) {
	ctxFn := s.deps.Ctx
	if ctxFn == nil {
		return
	}

	s.mu.Lock()
	current := s.state.SessionID
	lastSequence := int64(0)
	if len(s.state.Messages) > 0 {
		lastSequence = s.state.Messages[len(s.state.Messages)-1].SequenceNumber
	}
	s.mu.Unlock()
	if sessionID == "" || current != sessionID {
		return
	}

	base := ctxFn()
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, requestTimeout)
	defer cancel()

	// Só a JANELA RECENTE: a mensagem com anexo acabou de entrar (é a última), e
	// o snapshot de /messages também devolve os metadados dos anexos dela.
	from := lastSequence - attachmentEnrichWindow
	if from < 0 {
		from = 0
	}
	if _, err := s.ListMessages(ctx, from, attachmentEnrichLimit); err != nil {
		s.deps.Logf("[supportchat] enriquecimento de anexos indisponível: " + err.Error())
	}
}

// trimMessagesLocked descarta as mensagens mais antigas acima do teto. A lista
// está ordenada por sequência crescente (as otimistas, sem sequência, ficam no
// fim), então podar a frente remove exatamente as mais antigas.
func (s *Service) trimMessagesLocked() {
	if len(s.state.Messages) <= maxInMemoryMessages {
		return
	}

	drop := len(s.state.Messages) - maxInMemoryMessages
	kept := make([]Message, len(s.state.Messages)-drop)
	copy(kept, s.state.Messages[drop:])
	s.state.Messages = kept
}

// maxCanonicalSequenceLocked devolve a maior sequência canônica conhecida
// (0 quando só existem bolhas otimistas). As canônicas estão ordenadas, então
// basta descer do fim até a primeira com sequência.
//
// O chamador deve estar com s.mu travado.
func (s *Service) maxCanonicalSequenceLocked() int64 {
	for i := len(s.state.Messages) - 1; i >= 0; i-- {
		if sequence := s.state.Messages[i].SequenceNumber; sequence > 0 {
			return sequence
		}
	}
	return 0
}

func (s *Service) alreadySeen(clientMessageID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.seenClientIDs[clientMessageID]; ok {
		return true
	}

	s.seenClientIDs[clientMessageID] = time.Now()
	s.seenOrder = append(s.seenOrder, clientMessageID)

	for len(s.seenOrder) > seenClientIDsLimit {
		oldest := s.seenOrder[0]
		s.seenOrder = s.seenOrder[1:]
		delete(s.seenClientIDs, oldest)
	}

	return false
}

// ── Publicação agent -> servidor (js.Publish com PubAck) ────────────────────

func (s *Service) publishAgentEnvelope(envelope Envelope) error {
	if envelope.ClientMessageID == "" {
		envelope.ClientMessageID = newID()
	}
	if envelope.TimestampUTC == "" {
		envelope.TimestampUTC = time.Now().UTC().Format(time.RFC3339)
	}

	payload, err := json.Marshal(envelope)
	if err != nil {
		return err
	}

	s.natsMu.Lock()
	nc := s.nc
	subject := s.agentSubj
	s.natsMu.Unlock()

	if nc == nil || subject == "" {
		return fmt.Errorf("canal NATS do chat indisponível")
	}

	msg := &nats.Msg{Subject: subject, Data: payload, Header: nats.Header{}}
	// Nats-Msg-Id habilita a deduplicação do JetStream (DuplicateWindow) e é a
	// chave de idempotência usada pelo servidor no insert.
	msg.Header.Set("Nats-Msg-Id", envelope.SessionID+":"+envelope.ClientMessageID)

	if js, jsErr := nc.JetStream(); jsErr == nil {
		if _, pubErr := js.PublishMsg(msg); pubErr == nil {
			return nil
		} else {
			s.deps.Logf("[supportchat] js.Publish falhou (fallback core): " + pubErr.Error())
		}
	}

	return nc.PublishMsg(msg)
}

// ── REST (snapshot, convite, envio de fallback, anexos) ─────────────────────

func (s *Service) baseURL() string {
	cfg := s.currentConfig()
	scheme := strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
	if scheme != "http" && scheme != "https" {
		scheme = "https"
	}
	server := strings.TrimSpace(cfg.ApiServer)
	server = strings.TrimPrefix(strings.TrimPrefix(server, "http://"), "https://")
	return scheme + "://" + strings.TrimRight(server, "/")
}

func (s *Service) currentConfig() DebugConfig {
	if s.deps.GetDebugConfig == nil {
		return DebugConfig{}
	}
	return s.deps.GetDebugConfig()
}

func (s *Service) doJSON(ctx context.Context, method, path string, body any, out any) error {
	cfg := s.currentConfig()
	if strings.TrimSpace(cfg.ApiServer) == "" || strings.TrimSpace(cfg.AuthToken) == "" {
		return fmt.Errorf("configuração da API indisponível")
	}

	var payload []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = encoded
	}

	req, err := http.NewRequestWithContext(ctx, method, s.baseURL()+"/api/v1/agent-auth/me/support-chat"+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := setAgentAuthHeaders(req, cfg); err != nil {
		return err
	}

	client := tlsutil.NewHTTPClient(requestTimeout)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s", apiErrorMessage(resp.StatusCode, string(raw)))
	}

	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// apiErrorMessage traduz a resposta de erro da API para uma mensagem que o
// usuário final entende.
//
// BUG CORRIGIDO: o corpo JSON cru ia direto para o banner da janela do chat
// ("Falha no anexo: HTTP 400: {\"error\":\"Requisição inválida\",\"message\":\"Object
// storage não está configurado..., \"traceId\":\"...\"}"). A janela é do usuário
// final: além de ilegível, o texto não dizia o que fazer. Aqui extraímos o campo
// message do ProblemDetails e traduzimos os casos conhecidos — em especial o
// object storage não configurado, que é o que impede QUALQUER anexo.
func apiErrorMessage(status int, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Sprintf("o servidor recusou a operação (HTTP %d)", status)
	}

	var payload struct {
		Message string `json:"message"`
		Error   string `json:"error"`
		Title   string `json:"title"`
		// Code é o código estável do ProblemDetails da API (ex.: "session_not_active").
		Code string `json:"code"`
	}
	message := ""
	code := ""
	if json.Unmarshal([]byte(raw), &payload) == nil {
		message = strings.TrimSpace(payload.Message)
		if message == "" {
			message = strings.TrimSpace(payload.Error)
		}
		if message == "" {
			message = strings.TrimSpace(payload.Title)
		}
		code = strings.TrimSpace(payload.Code)
	}
	if message == "" {
		// Corpo não-JSON (HTML de proxy, texto puro): corta para não estourar a UI.
		message = raw
		if len(message) > 300 {
			message = message[:300] + "..."
		}
	}

	// O código estável vive no campo code — e NÃO no texto que o usuário lê.
	// BUG CORRIGIDO: o 409 de "sessão não está mais ativa" era detectado
	// procurando a string no corpo; a API devolve code=session_not_active com a
	// mensagem em português, então a tradução nunca casava e a janela mostrava
	// "operação inválida para o estado atual da conversa".
	// BUG CORRIGIDO (revisão): o `code` estavavel era DESCARTADO ao traduzir. O
	// chamador só recebia o texto, e o agent decidia "já resolvido" procurando
	// a frase no texto — frágil e dependente do idioma. O código agora viaja
	// junto (prefixado entre colchetes) para quem precisa decidir sem depender
	// da mensagem localizada.
	if strings.Contains(strings.ToLower(code), "session_not_active") {
		return supportChatErrorCodePrefix(code) + "a conversa não está mais ativa."
	}

	lower := strings.ToLower(message)
	if strings.Contains(lower, "object storage") || strings.Contains(lower, "bucketname") ||
		strings.Contains(lower, "accesskey") || strings.Contains(lower, "endpoint é obrigatório") {
		return "o armazenamento de arquivos (object storage) não está configurado no servidor. " +
			"Peça ao administrador para preencher os dados em Configurações > Servidor — " +
			"enquanto isso, anexos e prints não podem ser enviados."
	}
	if status == http.StatusConflict && strings.Contains(strings.ToLower(code+message), "session_not_active") {
		return supportChatErrorCodePrefix(code) + "a conversa não está mais ativa."
	}
	// Qualquer código estável de sessão (session_declined/session_ended/
	// session_expired/session_active) acompanha a mensagem: é o que permite ao
	// agent tratar "já resolvido" sem depender do texto.
	return supportChatErrorCodePrefix(code) + message
}

// supportChatErrorCodePrefix devolve o código estável da API no formato
// "[code] " para prefixar a mensagem devolvida ao chamador. Vazio quando o
// servidor não mandou um code.
//
// Motivo: apiErrorMessage traduz a mensagem para o usuário final, e essa
// tradução perde o code. Quem precisa DECIDIR (ex.: tratar 409
// "session_declined" como convite já resolvido, sem retentar) precisa do
// código, não do texto.
func supportChatErrorCodePrefix(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return ""
	}
	return "[" + code + "] "
}

// resetTranscriptForSessionLocked descarta a conversa em memória quando a
// sessão MUDA.
//
// BUG CORRIGIDO: o estado guardava as mensagens da conversa anterior; ao iniciar
// uma nova conversa elas permaneciam e se misturavam com as novas — e como os
// sequence_number recomeçam em cada sessão, a ordenação ficava embaralhada
// (mensagens antigas apareciam entre as novas).
//
// O chamador deve estar com s.mu travado.
func (s *Service) resetTranscriptForSessionLocked(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || sessionID == s.state.SessionID {
		return
	}

	s.deps.Logf("[supportchat] nova sessão detectada; descartando a conversa anterior da memória")
	s.state.Messages = []Message{}
	// A deduplicação de clientMessageId também é por sessão.
	s.seenClientIDs = make(map[string]time.Time)
	s.seenOrder = nil
}

// PrepareInvite registra o convite recebido (estado "aguardando confirmação").
// A janela do chat só é aberta DEPOIS do aceite do usuário.
//
// Atalho de PrepareInviteIfIdle para chamadores que já sabem que podem atender
// (testes e retomada). O caminho de produção — o comando TicketChatInvite — usa
// a versão com decisão para não perder a corrida entre dois convites.
func (s *Service) PrepareInvite(invite Invite) {
	_ = s.PrepareInviteIfIdle(invite)
}

// PrepareInviteIfIdle registra o convite SOMENTE se ele puder ser atendido AGORA,
// devolvendo a decisão para o chamador.
//
// Resolve três casos que o antigo check-fora-do-lock deixava passar:
//
//  1. CORRIDA: dois convites quase simultâneos passavam pela checagem "outra
//     conversa ativa?" e o segundo sobrescrevia o estado do primeiro — o
//     servidor ficava esperando resposta de um convite que o usuário nunca viu;
//  2. REENTREGA de convite de uma conversa JÁ ACEITA: PrepareInvite rebaixava o
//     estado para "waiting" e o modal de confirmação reaparecia no meio do
//     atendimento;
//  3. CONVITE SUBSTITUÍDO: o anterior deixava de existir em silêncio (expirava
//     no servidor, com o atendente vendo "aguardando"). Agora o id é devolvido
//     em SupersededSessionID para ser recusado explicitamente.
func (s *Service) PrepareInviteIfIdle(invite Invite) InviteDecision {
	invite.SessionID = strings.TrimSpace(invite.SessionID)
	if invite.SessionID == "" {
		return InviteDecision{Reason: InviteReasonInvalid}
	}

	now := time.Now()

	s.mu.Lock()
	previous := s.state
	decision := InviteDecision{}

	switch {
	case previous.Status == StatusActive && previous.SessionID == invite.SessionID:
		// Reentrega do convite de uma conversa já aceita: NÃO reabre o pedido de
		// confirmação (o modal voltaria a aparecer em cima da conversa em curso).
		decision.Reason = InviteReasonAlreadyActive
		s.mu.Unlock()
		s.deps.Logf("[supportchat] convite reentregue de conversa ja ativa ignorado: " + invite.SessionID)
		return decision

	case previous.Status == StatusActive:
		// Uma sessão por máquina: outra conversa ATIVA manda recusar.
		decision.Reason = InviteReasonBusy
		s.mu.Unlock()
		s.deps.Logf("[supportchat] convite recusado (maquina em outro chat ativo): " + invite.SessionID)
		return decision

	case previous.Status == StatusEnded && previous.SessionID == invite.SessionID:
		// Reentrega de convite de uma conversa JÁ ENCERRADA: não ressuscita o
		// pedido de confirmação (o modal apareceria por cima da conversa que o
		// usuário ainda está lendo e a janela voltaria a flutuar).
		decision.Reason = InviteReasonAlreadyFinished
		s.mu.Unlock()
		s.deps.Logf("[supportchat] convite de conversa ja encerrada ignorado: " + invite.SessionID)
		return decision
	}

	if previous.Status == StatusWaiting && previous.SessionID != "" && previous.SessionID != invite.SessionID {
		// CONVITE NOVO enquanto OUTRO aguarda resposta: o novo SUBSTITUI o
		// anterior (não é "busy").
		//
		// RELATO: "mando convite e a notificação não abre". O convite anterior
		// ainda estava "waiting" aqui e o serviço respondia "busy" — o usuário
		// nunca recebia o pedido novo (mesmo quando o anterior já tinha saído da
		// tela). A sessão antiga é RECUSADA como "replaced" pelo chamador, então
		// o console não fica "aguardando" para sempre.
		decision.SupersededSessionID = previous.SessionID
	}

	s.resetTranscriptForSessionLocked(invite.SessionID)
	s.state.HasSession = true
	s.state.SessionID = invite.SessionID
	s.state.TicketID = strings.TrimSpace(invite.TicketID)
	s.state.TicketTitle = invite.TicketTitle
	s.state.SupportName = invite.SupportName
	s.state.ExpiresAt = strings.TrimSpace(invite.ExpiresAt)
	s.state.Status = StatusWaiting
	s.state.LastError = ""
	if s.state.Messages == nil {
		s.state.Messages = []Message{}
	}
	// waitingSince marca o começo da espera do aceite (relógio da sessão no
	// resumo do log); a conversa ainda não começou, então activeSince fica zero.
	s.waitingSince = now
	s.activeSince = time.Time{}
	decision.Accepted = true
	s.mu.Unlock()

	s.deps.Logf(fmt.Sprintf("[supportchat] convite recebido: sessao=%s chamado=%q atendente=%q",
		invite.SessionID, invite.TicketTitle, invite.SupportName))
	s.emitState("invite")
	return decision
}

// ClearStateIfWaiting limpa o estado SOMENTE se ele ainda é o convite
// "waiting" da sessão informada — checagem atômica sob o lock do serviço.
//
// Evita o TOCTOU do verify: entre "state==waiting" e o ClearState o usuário
// pode aceitar (status vira active) e o clear apagaria a conversa recém-aberta.
func (s *Service) ClearStateIfWaiting(sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	s.mu.Lock()
	if s.state.Status != StatusWaiting || s.state.SessionID != sessionID {
		s.mu.Unlock()
		return false
	}
	s.state = State{Status: StatusNone, Messages: []Message{}}
	s.mu.Unlock()
	s.emitState("clear-waiting")
	return true
}

// ActiveSessionID consulta o servidor APENAS para saber qual sessão está
// pendente/ativa nesta máquina, sem tocar no estado local.
//
// Existe para validar um convite aberto sem o efeito colateral do Refresh:
// Refresh zera o estado "waiting" quando o servidor (temporariamente) responde
// sem sessão — o que apagava o pedido de aceite da janela recém-aberta.
func (s *Service) ActiveSessionID(ctx context.Context) (string, bool, error) {
	var response struct {
		HasSession bool `json:"hasSession"`
		Session    *struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	if err := s.doJSON(ctx, http.MethodGet, "/active", nil, &response); err != nil {
		return "", false, err
	}
	if !response.HasSession || response.Session == nil {
		return "", false, nil
	}
	return strings.TrimSpace(response.Session.ID), true, nil
}

// Refresh faz o snapshot REST do estado (usado na reconciliação).
func (s *Service) Refresh(ctx context.Context) (State, error) {
	return s.refresh(ctx, false)
}

// RefreshKeepingInvite é o snapshot usado pela JANELA ao abrir: quando o
// servidor responde sem sessão ele PRESERVA um convite "waiting" local.
//
// O boot da janela depende desse snapshot para aprender o convite (o evento
// supportchat:state chega antes de a janela existir). O Refresh destrutivo
// apagava o convite numa inconsistência de leitura e a janela abria "SEM
// CONVERSA" — o verify em background então não tinha o que validar.
func (s *Service) RefreshKeepingInvite(ctx context.Context) (State, error) {
	return s.refresh(ctx, true)
}

func (s *Service) refresh(ctx context.Context, preserveWaiting bool) (State, error) {
	var response struct {
		HasSession bool `json:"hasSession"`
		Session    *struct {
			ID           string `json:"id"`
			TicketID     string `json:"ticketId"`
			Status       string `json:"status"`
			StartedBy    string `json:"startedByName"`
			MessageCount int    `json:"messageCount"`
		} `json:"session"`
	}

	if err := s.doJSON(ctx, http.MethodGet, "/active", nil, &response); err != nil {
		return s.State(), err
	}

	var disappearedSession, disappearedStatus string
	disappearedMessages := 0

	s.mu.Lock()
	if !response.HasSession || response.Session == nil {
		// Sem sessão ATIVA no servidor. Quando a conversa local TERMINOU, o
		// estado "ended" (com a thread em memória) é preservado: zerar aqui
		// apagava da tela, na primeira reconciliação depois do fim, exatamente
		// o que o usuário ainda estava lendo — inclusive a nota de quem
		// encerrou. O agent continua sem histórico: nada é persistido.
		//
		// preserveWaiting: o boot da janela do convite não pode destruir o
		// "waiting" por uma leitura transitória; quem decide se o convite é
		// válido é o verify em background (ActiveSessionID + tolerância).
		if preserveWaiting && s.state.Status == StatusWaiting && s.state.SessionID != "" {
			// Mantém o convite local intacto.
		} else if s.state.Status != StatusEnded || s.state.SessionID == "" {
			// Sessão que MORREU no servidor sem evento de fim (expiração por
			// inatividade): o resumo também precisa sair, senão exatamente o caso
			// mais silencioso ficava sem registro no log do agent.
			if s.state.SessionID != "" && (s.state.Status == StatusActive || s.state.Status == StatusWaiting) {
				disappearedSession = s.state.SessionID
				disappearedStatus = s.state.Status
				disappearedMessages = len(s.state.Messages)
			}
			s.state = State{Status: StatusNone, Messages: []Message{}}
		}
	} else {
		s.resetTranscriptForSessionLocked(response.Session.ID)
		s.state.HasSession = true
		s.state.SessionID = response.Session.ID
		s.state.TicketID = response.Session.TicketID
		s.state.SupportName = response.Session.StartedBy
		s.state.Status = normalizeStatus(response.Session.Status)
		// Relógio da sessão: sem isto uma retomada (startup/reconexão) perdia a
		// duração no resumo do log (o resumo sairia com "-").
		switch s.state.Status {
		case StatusActive:
			if s.activeSince.IsZero() {
				s.activeSince = time.Now()
			}
		case StatusWaiting:
			if s.waitingSince.IsZero() {
				s.waitingSince = time.Now()
			}
		}
		if s.state.Messages == nil {
			s.state.Messages = []Message{}
		}
	}
	s.mu.Unlock()

	if disappearedSession != "" {
		reason := "sessao ausente no servidor"
		if disappearedStatus == StatusWaiting {
			reason = "convite expirado/removido no servidor"
		}
		s.logSessionClosedWith(disappearedSession, reason, disappearedMessages)
	}

	// BUGFIX: o snapshot REST precisa trazer TAMBÉM as mensagens. Sem isso, um
	// agent reiniciado no meio de uma conversa (consumer durável já com os
	// offsets acked) reabriria a janela com a conversa VAZIA.
	s.mu.Lock()
	shouldLoadMessages := response.HasSession && s.state.HasSession && s.state.SessionID != ""
	s.mu.Unlock()

	if shouldLoadMessages {
		if _, err := s.ListMessages(ctx, 0, 300); err != nil {
			s.deps.Logf("[supportchat] snapshot de mensagens indisponível: " + err.Error())
		}
	}

	s.emitState("refresh")
	return s.State(), nil
}

// ListMessages busca o delta de mensagens por sequência (snapshot do histórico
// da sessão ativa). Complementa o NATS; não substitui a entrega em tempo real.
func (s *Service) ListMessages(ctx context.Context, afterSequence int64, limit int) ([]Message, error) {
	s.mu.Lock()
	sessionID := s.state.SessionID
	s.mu.Unlock()

	if sessionID == "" {
		return nil, fmt.Errorf("sem sessão de chat ativa")
	}
	if limit <= 0 {
		limit = 200
	}

	path := fmt.Sprintf("/sessions/%s/messages?afterSequence=%d&limit=%d", url.PathEscape(sessionID), afterSequence, limit)
	var messages []Message
	if err := s.doJSON(ctx, http.MethodGet, path, nil, &messages); err != nil {
		return nil, err
	}

	s.mergeMessages(messages)
	s.emitState("list-messages")
	return s.State().Messages, nil
}

func (s *Service) mergeMessages(incoming []Message) {
	if len(incoming) == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state.Messages == nil {
		s.state.Messages = []Message{}
	}

	// BUGFIX (revisão): descarta a bolha otimista (SequenceNumber == 0) SÓ quando
	// a mensagem canônica correspondente chega — casada por clientMessageId.
	//
	// Antes qualquer snapshot descartava TODAS as otimistas: se o envio saísse
	// por NATS (processo único, sem o serviço), o refresh() imediato apagava a
	// mensagem recém-enviada e ela nunca reaparecia na janela (o agent não recebe
	// de volta as próprias mensagens pelo canal do servidor).
	confirmed := make(map[string]struct{}, len(incoming))
	for _, message := range incoming {
		if message.SequenceNumber > 0 {
			key := strings.TrimSpace(message.ClientMessageID)
			if key == "" {
				key = message.ID
			}
			if key != "" {
				confirmed[key] = struct{}{}
			}
		}
	}

	canonical := make([]Message, 0, len(s.state.Messages))
	for _, message := range s.state.Messages {
		if message.SequenceNumber > 0 {
			canonical = append(canonical, message)
			continue
		}
		if _, ok := confirmed[message.ID]; ok {
			continue // substituída pela versão persistida
		}
		canonical = append(canonical, message)
	}
	s.state.Messages = canonical

	existing := make(map[int64]int, len(s.state.Messages))
	for i, m := range s.state.Messages {
		existing[m.SequenceNumber] = i
	}

	for _, message := range incoming {
		if index, ok := existing[message.SequenceNumber]; ok {
			mergeAttachmentMetadata(&s.state.Messages[index], message.Attachments)
			continue
		}
		s.state.Messages = append(s.state.Messages, message)
		existing[message.SequenceNumber] = len(s.state.Messages) - 1
	}

	sortMessages(s.state.Messages)
	s.trimMessagesLocked()
}

// mergeAttachmentMetadata completa os metadados dos anexos já conhecidos.
//
// BUG CORRIGIDO: o preenchimento só acontecia quando a mensagem não tinha
// NENHUM anexo. Como o envelope do NATS passou a trazer os IDs, o snapshot REST
// deixou de completar nome/tipo/tamanho e o anexo ficaria eternamente exibido
// como "anexo". Agora os metadados são casados por ID.
func mergeAttachmentMetadata(message *Message, incoming []Attachment) {
	if message == nil || len(incoming) == 0 {
		return
	}
	if len(message.Attachments) == 0 {
		message.Attachments = incoming
		return
	}

	byID := make(map[string]Attachment, len(incoming))
	for _, attachment := range incoming {
		if attachment.ID != "" {
			byID[attachment.ID] = attachment
		}
	}

	for i, attachment := range message.Attachments {
		rich, ok := byID[attachment.ID]
		if !ok {
			continue
		}
		if message.Attachments[i].FileName == "" {
			message.Attachments[i].FileName = rich.FileName
		}
		if message.Attachments[i].ContentType == "" {
			message.Attachments[i].ContentType = rich.ContentType
		}
		if message.Attachments[i].SizeBytes == 0 {
			message.Attachments[i].SizeBytes = rich.SizeBytes
		}
		if message.Attachments[i].UploadedBy == "" {
			message.Attachments[i].UploadedBy = rich.UploadedBy
		}
	}
}

// SendMessage envia uma mensagem do usuário final pelo NATS (com PubAck) e cai
// para o REST apenas se o canal estiver indisponível.
func (s *Service) SendMessage(ctx context.Context, body string, attachmentIDs []string) error {
	body = strings.TrimSpace(body)

	// BUG CORRIGIDO: enviar SÓ o anexo (print sem legenda) era recusado aqui e
	// no servidor ("a mensagem não pode ser vazia"). O botão de anexo da janela
	// permite esse envio, então a imagem nunca saía.
	if body == "" && len(attachmentIDs) == 0 {
		return fmt.Errorf("mensagem vazia")
	}

	s.mu.Lock()
	sessionID := s.state.SessionID
	s.mu.Unlock()

	if sessionID == "" {
		return fmt.Errorf("sem sessão de chat ativa")
	}

	clientMessageID := newID()
	envelope := Envelope{
		EventType:       EventMessage,
		SessionID:       sessionID,
		AuthorKind:      "enduser",
		Body:            body,
		AttachmentIDs:   attachmentIDs,
		ClientMessageID: clientMessageID,
	}

	if err := s.publishAgentEnvelope(envelope); err == nil {
		// Otimista: aparece imediatamente; a sequência canônica chega no
		// snapshot seguinte do console/janela.
		//
		// Os anexos entram só com o ID (é o que o envelope carrega); nome/tipo
		// vêm no snapshot REST, casados por ID em mergeAttachmentMetadata.
		attachments := make([]Attachment, 0, len(attachmentIDs))
		for _, id := range attachmentIDs {
			if strings.TrimSpace(id) != "" {
				attachments = append(attachments, Attachment{ID: id})
			}
		}
		s.mu.Lock()
		s.state.Messages = append(s.state.Messages, Message{
			ID:              clientMessageID,
			ClientMessageID: clientMessageID,
			AuthorKind:      "enduser",
			Body:            body,
			CreatedAt:       time.Now().UTC().Format(time.RFC3339),
			Attachments:     attachments,
		})
		s.trimMessagesLocked()
		s.mu.Unlock()
		s.emitState("message-sent-nats")
		return nil
	} else {
		s.deps.Logf("[supportchat] envio via NATS falhou; usando REST: " + err.Error())
	}

	var sent Message
	payload := map[string]any{
		"body":            body,
		"attachmentIds":   attachmentIDs,
		"clientMessageId": clientMessageID,
	}
	path := fmt.Sprintf("/sessions/%s/messages", url.PathEscape(sessionID))
	if err := s.doJSON(ctx, http.MethodPost, path, payload, &sent); err != nil {
		return err
	}

	s.mergeMessages([]Message{sent})
	s.emitState("message-sent-rest")
	return nil
}

// RespondInviteForSession responde ao convite de UMA sessão SEM tocar no estado
// da conversa ativa.
//
// BUGFIX: a recusa por "busy" acontece enquanto OUTRO chat está ativo. Usar o
// caminho que dependia de s.state.SessionID reescrevia a sessão corrente e o
// usuário ficava sem conseguir enviar mensagens (o envio ia para a sessão
// recusada).
func (s *Service) RespondInviteForSession(ctx context.Context, sessionID string, accepted bool, reason string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("sessão de convite ausente")
	}

	payload := map[string]any{"accepted": accepted}
	if strings.TrimSpace(reason) != "" {
		payload["reason"] = reason
	}

	path := fmt.Sprintf("/sessions/%s/invite-response", url.PathEscape(sessionID))
	return s.doJSON(ctx, http.MethodPost, path, payload, nil)
}

// RespondInviteAcceptedForSession reporta o ACEITE de uma sessão específica e
// move o estado local para Active QUANDO a sessão é a corrente.
//
// Por que existe: no modo companion o cartão do convite é criado na UI e o
// estado "waiting" vive no SERVIÇO. Se a UI reportasse o aceite apenas quando
// o estado local fosse "waiting" (que ela pode não ter, ou que pode ter sido
// trocado por outra sessão), o POST simplesmente não saía: o servidor
// reentregava o pedido e o cartão reaparecia ("às vezes não abre").
//
// O POST agora é SEMPRE feito para a sessão do cartão; o estado local só é
// alterado se a sessão ainda for a corrente (outra conversa ativa nunca é
// reescrita).
func (s *Service) RespondInviteAcceptedForSession(ctx context.Context, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("sessão de convite ausente")
	}

	if err := s.RespondInviteForSession(ctx, sessionID, true, ""); err != nil {
		return err
	}

	s.mu.Lock()
	waiting := s.waitingSince
	if s.state.SessionID != "" && s.state.SessionID == sessionID {
		s.state.Status = StatusActive
		if s.activeSince.IsZero() {
			s.activeSince = time.Now()
		}
	}
	s.mu.Unlock()

	waited := time.Duration(0)
	if !waiting.IsZero() {
		waited = time.Since(waiting)
	}
	s.deps.Logf(fmt.Sprintf("[supportchat] conversa iniciada: sessao=%s (aceite apos %s)",
		sessionID, waited.Round(time.Second)))

	s.emitState("invite-response")
	return nil
}

// RespondInvite responde ao convite da sessão corrente (caminho do aceite, que
// move o estado local para Active).
func (s *Service) RespondInvite(ctx context.Context, accepted bool, reason string) error {
	s.mu.Lock()
	sessionID := s.state.SessionID
	s.mu.Unlock()

	if sessionID == "" {
		return fmt.Errorf("sem convite de chat pendente")
	}

	payload := map[string]any{"accepted": accepted}
	if strings.TrimSpace(reason) != "" {
		payload["reason"] = reason
	}

	path := fmt.Sprintf("/sessions/%s/invite-response", url.PathEscape(sessionID))
	if err := s.doJSON(ctx, http.MethodPost, path, payload, nil); err != nil {
		return err
	}

	s.mu.Lock()
	waiting := s.waitingSince
	if accepted {
		s.state.Status = StatusActive
		s.activeSince = time.Now()
	} else {
		s.state.Status = StatusEnded
		s.state.HasSession = false
	}
	s.mu.Unlock()

	if accepted {
		waited := time.Duration(0)
		if !waiting.IsZero() {
			waited = time.Since(waiting)
		}
		s.deps.Logf(fmt.Sprintf("[supportchat] conversa iniciada: sessao=%s (aceite apos %s)",
			sessionID, waited.Round(time.Second)))
	} else {
		reasonText := strings.TrimSpace(reason)
		if reasonText == "" {
			reasonText = "user_denied"
		}
		s.deps.Logf(fmt.Sprintf("[supportchat] convite recusado pelo usuario: sessao=%s motivo=%s", sessionID, reasonText))
	}

	s.emitState("invite-response")
	return nil
}

// End encerra a sessão (usuário fechou a janela ou pediu encerramento).
func (s *Service) End(ctx context.Context, reason string) error {
	s.mu.Lock()
	sessionID := s.state.SessionID
	s.mu.Unlock()

	if sessionID == "" {
		return nil
	}

	if strings.TrimSpace(reason) == "" {
		reason = "user_closed"
	}

	payload := map[string]any{"reason": reason}
	path := fmt.Sprintf("/sessions/%s/end", url.PathEscape(sessionID))

	// Até 3 tentativas: encerrar é best-effort do lado da máquina (a sessão
	// também expira por inatividade no servidor).
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			}
		}
		if lastErr = s.doJSON(ctx, http.MethodPost, path, payload, nil); lastErr == nil {
			break
		}
	}

	s.mu.Lock()
	s.state.Status = StatusEnded
	s.state.HasSession = false
	s.mu.Unlock()
	s.logSessionClosed(sessionID, reason)
	s.emitState("end")

	if lastErr != nil {
		s.deps.Logf("[supportchat] encerramento remoto falhou após retries: " + lastErr.Error())
	}
	return lastErr
}

// ClearState descarta o estado local (chamado ao fechar a janela). Garante que
// nada de conversa permaneça em memória após o encerramento.
func (s *Service) ClearState() {
	s.mu.Lock()
	s.state = State{Status: StatusNone, Messages: []Message{}}
	s.seenClientIDs = make(map[string]time.Time)
	s.seenOrder = nil
	s.waitingSince = time.Time{}
	s.activeSince = time.Time{}
	s.mu.Unlock()
}

// logSessionClosed registra o RESUMO da sessão encerrada (uma linha por
// conversa): duração, volume e motivo. O log do agent já registrava cada
// transição, mas responder "quanto durou e quantas mensagens" durante um
// atendimento exigia varrer o histórico do logstore.
func (s *Service) logSessionClosed(sessionID, reason string) {
	s.mu.Lock()
	messages := len(s.state.Messages)
	s.mu.Unlock()

	s.logSessionClosedWith(sessionID, reason, messages)
}

// logSessionClosedWith é a versão com o volume já capturado (ver Refresh: o
// estado é zerado antes do resumo).
func (s *Service) logSessionClosedWith(sessionID, reason string, messages int) {
	s.mu.Lock()
	active := s.activeSince
	waiting := s.waitingSince
	// CONSOME os relógios da sessão: o mesmo fim chega por dois caminhos (o
	// encerramento pelo usuário via REST + o evento SessionEnded do servidor).
	// Sem consumir, o log ganhava DUAS linhas de resumo para a mesma conversa —
	// a segunda sem duração e com outro motivo ("servidor"), o que confunde
	// qualquer diagnóstico pelo logs.db.
	s.activeSince = time.Time{}
	s.waitingSince = time.Time{}
	s.mu.Unlock()

	// Nada para resumir (sessão já resumida/estado limpo): não polui o log.
	if active.IsZero() && waiting.IsZero() {
		return
	}

	duration := "-"
	switch {
	case !active.IsZero():
		duration = time.Since(active).Round(time.Second).String()
	case !waiting.IsZero():
		// Encerrou antes do aceite (convite expirado/recusado).
		duration = "nao aceito (" + time.Since(waiting).Round(time.Second).String() + ")"
	}

	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = "-"
	}
	if strings.TrimSpace(reason) == "" {
		reason = "-"
	}

	s.deps.Logf(fmt.Sprintf("[supportchat] conversa encerrada: sessao=%s motivo=%s duracao=%s mensagens=%d",
		sessionID, reason, duration, messages))
}

// FetchAttachmentPolicy consulta no SERVIDOR a política de anexos do chat
// (allowlist de tipos MIME e tamanho máximo) para a janela montar o seletor.
//
// Em erro devolve o PADRÃO junto com o erro: a janela nunca fica sem política
// (o servidor segue validando no presign/complete, então cair no default não
// abre brecha — só evita travar o seletor de arquivos).
func (s *Service) FetchAttachmentPolicy(ctx context.Context) (AttachmentPolicy, error) {
	policy := DefaultAttachmentPolicy()

	if err := s.doJSON(ctx, http.MethodGet, "/attachment-policy", nil, &policy); err != nil {
		return DefaultAttachmentPolicy(), err
	}

	// Resposta vazia/parcial não pode desligar o seletor nem zerar o limite.
	defaults := DefaultAttachmentPolicy()
	if len(policy.AllowedContentTypes) == 0 {
		policy.AllowedContentTypes = defaults.AllowedContentTypes
	}
	if policy.MaxFileSizeBytes <= 0 {
		policy.MaxFileSizeBytes = defaults.MaxFileSizeBytes
	}

	return policy, nil
}

// DownloadAttachment baixa os bytes de um anexo do chat (o webview não tem o
// token do agent; o download é feito pelo Go e devolvido em base64).
func (s *Service) DownloadAttachment(ctx context.Context, attachmentID string) ([]byte, string, error) {
	s.mu.Lock()
	sessionID := s.state.SessionID
	s.mu.Unlock()

	if sessionID == "" {
		return nil, "", fmt.Errorf("sem sessão de chat ativa")
	}

	cfg := s.currentConfig()
	if strings.TrimSpace(cfg.ApiServer) == "" || strings.TrimSpace(cfg.AuthToken) == "" {
		return nil, "", fmt.Errorf("configuração da API indisponível")
	}

	endpoint := fmt.Sprintf("%s/api/v1/agent-auth/me/support-chat/sessions/%s/attachments/%s",
		s.baseURL(), url.PathEscape(sessionID), url.PathEscape(attachmentID))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", err
	}
	if err := setAgentAuthHeaders(req, cfg); err != nil {
		return nil, "", err
	}

	client := tlsutil.NewHTTPClient(requestTimeout)
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// Teto defensivo (32 MiB) — o servidor já limita por TicketAttachmentSettings.
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, "", err
	}

	return data, resp.Header.Get("Content-Type"), nil
}

// ── Anexos ──────────────────────────────────────────────────────────────────

// UploadAttachment executa o fluxo presigned completo (prepare -> PUT -> complete).
func (s *Service) UploadAttachment(ctx context.Context, fileName, contentType string, data []byte) (string, error) {
	s.mu.Lock()
	sessionID := s.state.SessionID
	s.mu.Unlock()

	if sessionID == "" {
		return "", fmt.Errorf("sem sessão de chat ativa")
	}

	preparePayload := map[string]any{
		"fileName":    fileName,
		"contentType": contentType,
		"sizeBytes":   len(data),
	}

	var prepared struct {
		AttachmentID string `json:"attachmentId"`
		ObjectKey    string `json:"objectKey"`
		UploadURL    string `json:"uploadUrl"`
		HTTPMethod   string `json:"httpMethod"`
	}

	preparePath := fmt.Sprintf("/sessions/%s/attachments/presigned-upload", url.PathEscape(sessionID))
	if err := s.doJSON(ctx, http.MethodPost, preparePath, preparePayload, &prepared); err != nil {
		return "", err
	}

	method := strings.ToUpper(strings.TrimSpace(prepared.HTTPMethod))
	if method == "" {
		method = http.MethodPut
	}

	// O PUT no storage é feito pelo Go (não pelo webview) — evita origem/CORS
	// do WebView2 contra o provedor de object storage.
	uploadReq, err := http.NewRequestWithContext(ctx, method, prepared.UploadURL, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	uploadReq.Header.Set("Content-Type", contentType)

	uploadClient := tlsutil.NewHTTPClient(requestTimeout)
	uploadResp, err := uploadClient.Do(uploadReq)
	if err != nil {
		return "", err
	}
	defer uploadResp.Body.Close()

	if uploadResp.StatusCode < 200 || uploadResp.StatusCode >= 300 {
		return "", fmt.Errorf("upload HTTP %d", uploadResp.StatusCode)
	}

	completePayload := map[string]any{
		"attachmentId": prepared.AttachmentID,
		"objectKey":    prepared.ObjectKey,
		"fileName":     fileName,
		"contentType":  contentType,
		"sizeBytes":    len(data),
	}

	completePath := fmt.Sprintf("/sessions/%s/attachments/complete-upload", url.PathEscape(sessionID))
	if err := s.doJSON(ctx, http.MethodPost, completePath, completePayload, nil); err != nil {
		return "", err
	}

	return prepared.AttachmentID, nil
}
