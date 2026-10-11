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
	// seenClientIDsLimit limita o mapa de deduplicação em memória.
	seenClientIDsLimit = 256
	// supportChatAckWait é o AckWait do consumer durável do canal servidor -> agent.
	supportChatAckWait = 30 * time.Second
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

	natsMu     sync.Mutex
	nc         *nats.Conn
	serverSub  *nats.Subscription
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

// OnNATSConnected monta os subjects e assina o canal servidor -> agent com
// consumer JetStream durável (fallback core NATS com log explícito do risco).
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
	if s.serverSub != nil {
		_ = s.serverSub.Unsubscribe()
		s.serverSub = nil
	}
	s.nc = nc
	s.serverSubj = serverSubject
	s.agentSubj = agentSubject
	s.natsReady = true
	s.natsMu.Unlock()

	handler := func(msg *nats.Msg) {
		err := s.HandleEnvelope(msg.Data)
		if msg.Reply != "" {
			if err != nil {
				_ = msg.Nak()
			} else {
				_ = msg.Ack()
			}
		}
	}

	durable := "agent-" + strings.ReplaceAll(strings.ToLower(agentID), "-", "")
	if len(durable) > 80 {
		durable = durable[:80]
	}
	durable += "-supportchat"

	if js, err := nc.JetStream(); err == nil {
		sub, subErr := js.Subscribe(serverSubject, handler,
			nats.Durable(durable),
			nats.ManualAck(),
			nats.AckExplicit(),
			nats.AckWait(supportChatAckWait),
			nats.DeliverAll(),
			nats.MaxAckPending(64),
		)
		if subErr == nil {
			s.natsMu.Lock()
			s.serverSub = sub
			s.natsMu.Unlock()
			s.deps.Logf("[supportchat][jetstream] replay ativo durable=" + durable)
			s.emitState("nats-connected-jetstream")
			return
		}
		s.deps.Logf(fmt.Sprintf("[supportchat][jetstream] indisponivel (fallback core, SEM replay): %v", subErr))
	} else {
		s.deps.Logf(fmt.Sprintf("[supportchat][jetstream] indisponivel (fallback core, SEM replay): %v", err))
	}

	if sub, err := nc.Subscribe(serverSubject, handler); err != nil {
		s.deps.Logf("[supportchat] falha ao assinar canal core: " + err.Error())
	} else {
		s.natsMu.Lock()
		s.serverSub = sub
		s.natsMu.Unlock()
	}

	s.emitState("nats-connected-core")
}

// OnNATSDisconnected limpa a conexão (a janela mostra o banner de reconexão).
func (s *Service) OnNATSDisconnected() {
	s.natsMu.Lock()
	s.nc = nil
	s.serverSub = nil
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

	s.state.HasSession = true
	s.state.SessionID = envelope.SessionID
	s.state.TicketID = envelope.TicketID
	s.state.Status = StatusActive
	if s.state.Messages == nil {
		s.state.Messages = []Message{}
	}
	s.mu.Unlock()

	s.emitState("session-opened")
}

func (s *Service) applySessionEnded(envelope Envelope) {
	s.mu.Lock()

	// Mesma guarda de sessão dos outros eventos: um SessionEnded REENTREGUE
	// (AckWait) de uma conversa anterior encerraria a conversa ativa.
	if envelope.SessionID != "" && s.state.SessionID != "" && envelope.SessionID != s.state.SessionID {
		s.mu.Unlock()
		s.deps.Logf("[supportchat] SessionEnded de outra sessão ignorado: " + envelope.SessionID)
		return
	}

	s.state.Status = StatusEnded
	s.state.HasSession = false
	s.state.SessionID = envelope.SessionID
	s.mu.Unlock()

	s.emitState("session-ended")
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
	if envelope.Sequence > 0 && len(s.state.Messages) > 0 {
		last := s.state.Messages[len(s.state.Messages)-1]
		if envelope.Sequence <= last.SequenceNumber {
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
		ID:             envelope.ClientMessageID,
		SequenceNumber: envelope.Sequence,
		AuthorKind:     strings.TrimSpace(envelope.AuthorKind),
		AuthorName:     envelope.AuthorName,
		Body:           envelope.Body,
		CreatedAt:      envelope.TimestampUTC,
		Attachments:    attachments,
	})
	s.mu.Unlock()

	s.emitState("message")
	return nil
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
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// PrepareInvite registra o convite recebido (estado "aguardando confirmação").
// A janela do chat só é aberta DEPOIS do aceite do usuário.
func (s *Service) PrepareInvite(invite Invite) {
	s.mu.Lock()
	s.state.HasSession = true
	s.state.SessionID = strings.TrimSpace(invite.SessionID)
	s.state.TicketID = strings.TrimSpace(invite.TicketID)
	s.state.TicketTitle = invite.TicketTitle
	s.state.SupportName = invite.SupportName
	s.state.Status = StatusWaiting
	s.state.LastError = ""
	if s.state.Messages == nil {
		s.state.Messages = []Message{}
	}
	s.mu.Unlock()

	s.emitState("invite")
}

// Refresh faz o snapshot REST do estado (usado ao abrir a janela e na
// reconciliação após queda de NATS). Não é polling.
func (s *Service) Refresh(ctx context.Context) (State, error) {
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

	s.mu.Lock()
	if !response.HasSession || response.Session == nil {
		// Sem sessão ativa: zera o estado local (nada de histórico no agent).
		s.state = State{Status: StatusNone, Messages: []Message{}}
	} else {
		s.state.HasSession = true
		s.state.SessionID = response.Session.ID
		s.state.TicketID = response.Session.TicketID
		s.state.SupportName = response.Session.StartedBy
		s.state.Status = normalizeStatus(response.Session.Status)
		if s.state.Messages == nil {
			s.state.Messages = []Message{}
		}
	}
	s.mu.Unlock()

	// BUGFIX: o snapshot REST precisa trazer TAMBÉM as mensagens. Sem isso, um
	// agent reiniciado no meio de uma conversa (consumer durável já com os
	// offsets acked) reabriria a janela com a conversa VAZIA.
	s.mu.Lock()
	shouldLoadMessages := s.state.HasSession && s.state.SessionID != ""
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

	// BUGFIX: as bolhas otimistas do usuário (SequenceNumber == 0, ainda sem
	// sequência canônica) seriam duplicadas quando o servidor devolvesse a
	// mensagem real. Sempre que um snapshot canônico chega, os placeholders
	// otimistas são descartados.
	canonical := make([]Message, 0, len(s.state.Messages))
	for _, message := range s.state.Messages {
		if message.SequenceNumber > 0 {
			canonical = append(canonical, message)
		}
	}
	s.state.Messages = canonical

	existing := make(map[int64]int, len(s.state.Messages))
	for i, m := range s.state.Messages {
		existing[m.SequenceNumber] = i
	}

	for _, message := range incoming {
		if index, ok := existing[message.SequenceNumber]; ok {
			// Preenche anexos que o envelope do NATS não carrega.
			if len(message.Attachments) > 0 && len(s.state.Messages[index].Attachments) == 0 {
				s.state.Messages[index].Attachments = message.Attachments
			}
			continue
		}
		s.state.Messages = append(s.state.Messages, message)
		existing[message.SequenceNumber] = len(s.state.Messages) - 1
	}

	sortMessages(s.state.Messages)
}

// SendMessage envia uma mensagem do usuário final pelo NATS (com PubAck) e cai
// para o REST apenas se o canal estiver indisponível.
func (s *Service) SendMessage(ctx context.Context, body string, attachmentIDs []string) error {
	body = strings.TrimSpace(body)
	if body == "" {
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
		s.mu.Lock()
		s.state.Messages = append(s.state.Messages, Message{
			ID:         clientMessageID,
			AuthorKind: "enduser",
			Body:       body,
			CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		})
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
	if accepted {
		s.state.Status = StatusActive
	} else {
		s.state.Status = StatusEnded
		s.state.HasSession = false
	}
	s.mu.Unlock()

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
	s.mu.Unlock()
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
