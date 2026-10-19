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

import (
	"strings"
	"time"
)

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
//
// TicketID é OPCIONAL: conversas avulsas (sem chamado), abertas pela
// lista/detalhe do agent no console, chegam com o campo vazio/ausente.
type Invite struct {
	SessionID   string `json:"sessionId"`
	TicketID    string `json:"ticketId,omitempty"`
	TicketTitle string `json:"ticketTitle"`
	SupportName string `json:"supportUserName"`
	ExpiresAt   string `json:"expiresAtUtc"`
}

// InviteDecision é o resultado de registrar um convite recebido.
//
// Existe porque o registro do convite é um check-then-act: entre "posso atender
// este convite?" e "registre o convite" o estado pode mudar (dois convites
// chegando quase juntos, ou o mesmo convite reentregue pelo servidor). Devolver
// a decisão para o chamador elimina a corrida e permite responder ao servidor o
// motivo exato.
type InviteDecision struct {
	// Accepted indica que o convite foi registrado e a janela deve abrir.
	Accepted bool
	// Reason é o motivo estável de uma recusa, para reportar ao servidor:
	// "busy" (outra conversa ATIVA) ou "already_active" (o convite é reentrega de
	// uma conversa já aceita e não deve pedir confirmação de novo).
	Reason string
	// SupersededSessionID é a sessão de um convite ANTERIOR que estava aguardando
	// resposta e foi substituída por este. Precisa ser RECUSADA no servidor,
	// senão ele fica esperando a expiração e o atendente vê "aguardando" para um
	// convite que o usuário nunca viu.
	SupersededSessionID string
}

// Motivos de recusa de convite (InviteDecision.Reason).
const (
	InviteReasonBusy          = "busy"
	InviteReasonAlreadyActive = "already_active"
	// InviteReasonAlreadyFinished é a reentrega de um convite de uma conversa
	// que já TERMINOU (sessão "ended" na mesma máquina). Sem esta guarda o
	// estado voltava para "waiting" e a janela aberta em leitura ganhava o modal
	// de convite por cima da conversa encerrada — e voltava a flutuar.
	InviteReasonAlreadyFinished = "already_finished"
	InviteReasonInvalid         = "invalid_invite"
)

// AttachmentPolicy é a dica de UI (allowlist de tipos e tamanho máximo) para o
// seletor de arquivos da janela do chat.
//
// A AUTORIDADE continua sendo o servidor (TicketAttachmentSettings validado no
// presign e no complete). Esta política só evita que o usuário escolha um
// arquivo que será recusado depois — antes era um espelho hardcoded no agent e o
// erro só aparecia no envio.
type AttachmentPolicy struct {
	Enabled             bool     `json:"enabled"`
	AllowedContentTypes []string `json:"allowedContentTypes"`
	MaxFileSizeBytes    int64    `json:"maxFileSizeBytes"`
}

// DefaultAttachmentPolicy é o espelho da política padrão UNIFICADA do servidor
// (TicketAttachmentSettings: 10 MB e allowlist MIME; sem executáveis), usado
// quando a consulta falha. O limite maior de "parte dedicada do chamado"
// (maxLargeFileSizeBytes) NÃO se aplica ao chat de suporte.
func DefaultAttachmentPolicy() AttachmentPolicy {
	return AttachmentPolicy{
		Enabled:             true,
		AllowedContentTypes: []string{"image/jpeg", "image/png", "image/webp", "application/pdf"},
		MaxFileSizeBytes:    10 * 1024 * 1024,
	}
}

// ExpiredAt informa se o convite já venceu no instante informado.
//
// A entrega do comando NÃO é pontual: o servidor reentrega comandos não
// confirmados depois de alguns minutos (Nats:CommandRedelivery). Sem esta
// checagem, o eco tardio de um convite de uma sessão já encerrada reabria a
// janela do chat "do nada" no PC do usuário.
//
// Sem expiresAtUtc (payload antigo/ausente) o convite é tratado como válido —
// a validação de estado no servidor continua sendo a rede de segurança.
func (i Invite) ExpiredAt(now time.Time) bool {
	raw := strings.TrimSpace(i.ExpiresAt)
	if raw == "" {
		return false
	}

	expiresAt, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return false
	}

	// Folga para não descartar um convite válido por desvio de relógio da
	// máquina em relação ao servidor.
	return now.After(expiresAt.Add(inviteExpiryGrace))
}

// inviteExpiryGrace é a folga aplicada ao vencimento do convite.
//
// O filtro AUTORITATIVO é o estado da sessão no servidor (ver
// verifySupportChatInviteWindow); aqui é só um corte barato de convites
// claramente velhos. A reentrega que motivou a guarda chegou ~6 min depois de
// criada — quase 4 min após o vencimento, bem acima desta folga — e nenhum
// relógio razoavelmente sincronizado erra 2 minutos.
const inviteExpiryGrace = 2 * time.Minute

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
