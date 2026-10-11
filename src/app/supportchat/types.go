// Package supportchat implementa o chat de suporte vinculado ao chamado no
// lado do AGENT.
//
// Características deliberadas:
//   - o histórico NÃO é persistido no agent (nem SQLite, nem chat.db): o estado
//     vive em memória enquanto a janela do chat existe. O histórico canônico é
//     do servidor e só é consultável pelo console web;
//   - a entrega em tempo real é 100% NATS (sem polling): o canal
//     servidor -> agent é um consumer JetStream durável (replay após
//     reconexão/restart) e o canal agent -> servidor publica com PubAck;
//   - o REST é usado apenas para snapshot/abertura, anexos (presign) e
//     fallback de envio.
package supportchat

// Status possíveis do chat no lado do agent.
const (
	StatusNone    = "none"
	StatusWaiting = "waiting"
	StatusActive  = "active"
	StatusEnded   = "ended"
)

// DebugConfig é a visão mínima da configuração do agent usada pelo serviço.
type DebugConfig struct {
	AgentID   string
	ApiScheme string
	ApiServer string
	AuthToken string
}

// Attachment é um anexo de mensagem do chat.
type Attachment struct {
	ID          string `json:"id"`
	FileName    string `json:"fileName"`
	ContentType string `json:"contentType"`
	SizeBytes   int64  `json:"sizeBytes"`
	UploadedBy  string `json:"uploadedBy,omitempty"`
}

// Message é uma mensagem do chat.
type Message struct {
	ID             string `json:"id"`
	SequenceNumber int64  `json:"sequenceNumber"`
	// ClientMessageID é a chave de idempotência do EMISSOR. O servidor devolve a
	// mesma chave na mensagem canônica, o que permite trocar a bolha otimista
	// (SequenceNumber == 0) pela versão persistida sem heurística de corpo/data.
	ClientMessageID string       `json:"clientMessageId,omitempty"`
	AuthorKind      string       `json:"authorKind"`
	AuthorName      string       `json:"authorName,omitempty"`
	Body            string       `json:"body"`
	CreatedAt       string       `json:"createdAt"`
	Attachments     []Attachment `json:"attachments,omitempty"`
}

// State é o estado exposto à janela do chat.
type State struct {
	HasSession  bool      `json:"hasSession"`
	SessionID   string    `json:"sessionId,omitempty"`
	TicketID    string    `json:"ticketId,omitempty"`
	TicketTitle string    `json:"ticketTitle,omitempty"`
	SupportName string    `json:"supportName,omitempty"`
	Status      string    `json:"status"`
	Connected   bool      `json:"connected"`
	LastError   string    `json:"lastError,omitempty"`
	Messages    []Message `json:"messages"`
}

// Invite é o payload do comando TicketChatInvite.
type Invite struct {
	SessionID   string `json:"sessionId"`
	TicketID    string `json:"ticketId"`
	TicketTitle string `json:"ticketTitle"`
	SupportName string `json:"supportUserName"`
	ExpiresAt   string `json:"expiresAtUtc"`
}

// Envelope é o contrato do stream do chat de suporte (JSON camelCase; espelha
// Discovery.Core.DTOs.TicketChatEnvelope).
type Envelope struct {
	EventType       string   `json:"eventType"`
	SessionID       string   `json:"sessionId"`
	TicketID        string   `json:"ticketId"`
	AgentID         string   `json:"agentId"`
	Sequence        int64    `json:"sequence,omitempty"`
	AuthorKind      string   `json:"authorKind,omitempty"`
	AuthorName      string   `json:"authorName,omitempty"`
	Body            string   `json:"body,omitempty"`
	AttachmentIDs   []string `json:"attachmentIds,omitempty"`
	ClientMessageID string   `json:"clientMessageId,omitempty"`
	EndReason       string   `json:"endReason,omitempty"`
	Accepted        *bool    `json:"accepted,omitempty"`
	Reason          string   `json:"reason,omitempty"`
	TimestampUTC    string   `json:"timestampUtc,omitempty"`
}

// Tipos de evento do envelope (espelham TicketChatEventTypes no servidor).
const (
	EventSessionOpened  = "SessionOpened"
	EventMessage        = "Message"
	EventSessionEnded   = "SessionEnded"
	EventInviteResponse = "InviteResponse"
)
