// Package tickets encapsula a lógica de gestão de tickets do agente
// (chamadas HTTP à API v1), separada do App.
package tickets

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"discovery/app/core/tlsutil"
	"discovery/app/debug"
	"discovery/app/netutil"
)

// TicketSummary representa um ticket retornado pela API.
type TicketSummary struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	Category        string `json:"category"`
	Priority        string `json:"priority"`
	WorkflowStateID string `json:"workflowStateId"`
	SlaExpiresAt    string `json:"slaExpiresAt"`
	SlaBreached     bool   `json:"slaBreached"`
	DaysOpen        int    `json:"daysOpen"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`
	ClosedAt        string `json:"closedAt"`
}

// CreateTicketRequest é o payload para POST /me/tickets.
type CreateTicketRequest struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Priority    string `json:"priority"`
}

// AddCommentRequest é o payload para POST /me/tickets/{id}/comments.
type AddCommentRequest struct {
	Content string `json:"content"`
}

// WorkflowStateRequest é o payload para PATCH /me/tickets/{id}/workflow-state.
type WorkflowStateRequest struct {
	WorkflowStateID string `json:"workflowStateId"`
}

// CloseTicketRequest é o payload para POST /me/tickets/{id}/close.
type CloseTicketRequest struct {
	Rating int    `json:"rating"`
	Notes  string `json:"notes"`
}

// Deps são as dependências injetadas no Service.
type Deps struct {
	// GetDebugConfig retorna a configuração de conexão do agente.
	GetDebugConfig func() debug.Config
}

// Service encapsula as chamadas HTTP de gestão de tickets.
type Service struct {
	getDebugConfig func() debug.Config
}

// New cria um Service de tickets.
func New(deps Deps) *Service {
	return &Service{getDebugConfig: deps.GetDebugConfig}
}

// GetMyTickets lista tickets do agente.
func (s *Service) GetMyTickets(ctx context.Context, workflowStateID string) ([]TicketSummary, error) {
	cfg := s.getDebugConfig()
	endpoint := cfg.APIScheme() + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/tickets"
	if workflowStateID != "" {
		q := url.Values{}
		q.Set("workflowStateId", workflowStateID)
		endpoint += "?" + q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.AuthToken, cfg.AgentID); err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := tlsutil.NewHTTPClient(15 * time.Second).Do(req)
	if err != nil {
		return nil, fmt.Errorf("get tickets: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("get tickets HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var tickets []TicketSummary
	if err := json.Unmarshal(body, &tickets); err != nil {
		return nil, fmt.Errorf("get tickets parse: %w", err)
	}
	return tickets, nil
}

// CreateMyTicket cria um novo ticket.
func (s *Service) CreateMyTicket(ctx context.Context, reqBody CreateTicketRequest) (*TicketSummary, error) {
	cfg := s.getDebugConfig()
	endpoint := cfg.APIScheme() + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/tickets"

	payload, _ := json.Marshal(reqBody)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.AuthToken, cfg.AgentID); err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := tlsutil.NewHTTPClient(15 * time.Second).Do(req)
	if err != nil {
		return nil, fmt.Errorf("create ticket: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("create ticket HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var ticket TicketSummary
	if err := json.Unmarshal(body, &ticket); err != nil {
		return nil, fmt.Errorf("create ticket parse: %w", err)
	}
	return &ticket, nil
}

// AddMyTicketComment adiciona um comentário ao ticket.
func (s *Service) AddMyTicketComment(ctx context.Context, ticketID string, content string) error {
	cfg := s.getDebugConfig()
	endpoint := cfg.APIScheme() + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/tickets/" +
		url.PathEscape(ticketID) + "/comments"

	payload, _ := json.Marshal(AddCommentRequest{Content: content})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.AuthToken, cfg.AgentID); err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := tlsutil.NewHTTPClient(10 * time.Second).Do(req)
	if err != nil {
		return fmt.Errorf("add comment: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("add comment HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// CloseMyTicket fecha e avalia um ticket.
func (s *Service) CloseMyTicket(ctx context.Context, ticketID string, rating int, notes string) error {
	cfg := s.getDebugConfig()
	endpoint := cfg.APIScheme() + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/tickets/" +
		url.PathEscape(ticketID) + "/close"

	payload, _ := json.Marshal(CloseTicketRequest{Rating: rating, Notes: notes})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.AuthToken, cfg.AgentID); err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := tlsutil.NewHTTPClient(10 * time.Second).Do(req)
	if err != nil {
		return fmt.Errorf("close ticket: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("close ticket HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
