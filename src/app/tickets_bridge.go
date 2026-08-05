package app

import (
	"context"
	"errors"

	"discovery/app/tickets"
)

// errTicketsSvcUnavailable é retornado quando o serviço de tickets não foi
// inicializado.
var errTicketsSvcUnavailable = errors.New("serviço de tickets indisponível")

// ── Agent Ticket Bridge (API v1) ────────────────────────────────────────────
//
// Bridge Wails-bound que delega a lógica HTTP de gestão de tickets para o
// pacote tickets/. Os tipos (TicketSummary, CreateTicketRequest, etc.) são
// re-exportados para preservar os bindings do frontend.

// TicketSummary representa um ticket retornado pela API.
type TicketSummary = tickets.TicketSummary

// CreateTicketRequest é o payload para POST /me/tickets.
type CreateTicketRequest = tickets.CreateTicketRequest

// AddCommentRequest é o payload para POST /me/tickets/{id}/comments.
type AddCommentRequest = tickets.AddCommentRequest

// WorkflowStateRequest é o payload para PATCH /me/tickets/{id}/workflow-state.
type WorkflowStateRequest = tickets.WorkflowStateRequest

// CloseTicketRequest é o payload para POST /me/tickets/{id}/close.
type CloseTicketRequest = tickets.CloseTicketRequest

// GetMyTickets lista tickets do agente.
func (a *App) GetMyTickets(ctx context.Context, workflowStateID string) ([]TicketSummary, error) {
	if a.ticketsSvc == nil {
		return nil, errTicketsSvcUnavailable
	}
	return a.ticketsSvc.GetMyTickets(ctx, workflowStateID)
}

// CreateMyTicket cria um novo ticket.
func (a *App) CreateMyTicket(ctx context.Context, reqBody CreateTicketRequest) (*TicketSummary, error) {
	if a.ticketsSvc == nil {
		return nil, errTicketsSvcUnavailable
	}
	return a.ticketsSvc.CreateMyTicket(ctx, reqBody)
}

// AddMyTicketComment adiciona um comentário ao ticket.
func (a *App) AddMyTicketComment(ctx context.Context, ticketID string, content string) error {
	if a.ticketsSvc == nil {
		return errTicketsSvcUnavailable
	}
	return a.ticketsSvc.AddMyTicketComment(ctx, ticketID, content)
}

// CloseMyTicket fecha e avalia um ticket.
func (a *App) CloseMyTicket(ctx context.Context, ticketID string, rating int, notes string) error {
	if a.ticketsSvc == nil {
		return errTicketsSvcUnavailable
	}
	return a.ticketsSvc.CloseMyTicket(ctx, ticketID, rating, notes)
}
