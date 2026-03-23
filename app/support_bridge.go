package app

import (
	"encoding/json"
	"fmt"

	"discovery/internal/models"
)

func (a *App) GetAgentInfo() (AgentInfo, error) {
	if a == nil || a.supportSvc == nil {
		return AgentInfo{}, fmt.Errorf("support service indisponivel")
	}
	return a.supportSvc.GetAgentInfo()
}

func (a *App) GetSupportTickets() ([]APITicket, error) {
	if a == nil || a.supportSvc == nil {
		return []APITicket{}, fmt.Errorf("support service indisponivel")
	}
	return a.supportSvc.GetSupportTickets()
}

func (a *App) CreateSupportTicket(input CreateTicketInput) (APITicket, error) {
	if a == nil || a.supportSvc == nil {
		return APITicket{}, fmt.Errorf("support service indisponivel")
	}
	return a.supportSvc.CreateSupportTicket(input)
}

func (a *App) GetSupportTicketDetails(ticketID string) (APITicket, error) {
	if a == nil || a.supportSvc == nil {
		return APITicket{}, fmt.Errorf("support service indisponivel")
	}
	return a.supportSvc.GetSupportTicketDetails(ticketID)
}

func (a *App) GetTicketWorkflowStates() ([]APIWorkflowState, error) {
	if a == nil || a.supportSvc == nil {
		return []APIWorkflowState{}, fmt.Errorf("support service indisponivel")
	}
	return a.supportSvc.GetTicketWorkflowStates()
}

func (a *App) GetTicketComments(ticketID string) ([]TicketComment, error) {
	if a == nil || a.supportSvc == nil {
		return []TicketComment{}, fmt.Errorf("support service indisponivel")
	}
	return a.supportSvc.GetTicketComments(ticketID)
}

func (a *App) AddTicketCommentWithOptions(ticketID, content string, isInternal bool) (TicketComment, error) {
	if a == nil || a.supportSvc == nil {
		return TicketComment{}, fmt.Errorf("support service indisponivel")
	}
	return a.supportSvc.AddTicketCommentWithOptions(ticketID, content, isInternal)
}

func (a *App) AddTicketComment(ticketID, author, content string) error {
	if a == nil || a.supportSvc == nil {
		return fmt.Errorf("support service indisponivel")
	}
	return a.supportSvc.AddTicketComment(ticketID, author, content)
}

func (a *App) CloseSupportTicket(ticketID string, input CloseTicketInput) (APITicket, error) {
	if a == nil || a.supportSvc == nil {
		return APITicket{}, fmt.Errorf("support service indisponivel")
	}
	return a.supportSvc.CloseSupportTicket(ticketID, input)
}

func (a *App) CloseAgentTicket(ticketID string, rating *int, comment, workflowStateID string) (json.RawMessage, error) {
	if a == nil || a.supportSvc == nil {
		return nil, fmt.Errorf("support service indisponivel")
	}
	return a.supportSvc.CloseAgentTicket(ticketID, rating, comment, workflowStateID)
}

func (a *App) GetAgentInfoJSON() (json.RawMessage, error) {
	if a == nil || a.supportSvc == nil {
		return nil, fmt.Errorf("support service indisponivel")
	}
	return a.supportSvc.GetAgentInfoJSON()
}

func (a *App) ListAgentTickets() (json.RawMessage, error) {
	if a == nil || a.supportSvc == nil {
		return nil, fmt.Errorf("support service indisponivel")
	}
	return a.supportSvc.ListAgentTickets()
}

func (a *App) GetAgentTicketDetails(ticketID string) (json.RawMessage, error) {
	if a == nil || a.supportSvc == nil {
		return nil, fmt.Errorf("support service indisponivel")
	}
	return a.supportSvc.GetAgentTicketDetails(ticketID)
}

func (a *App) AddAgentTicketComment(ticketID, content string, isInternal bool) (json.RawMessage, error) {
	if a == nil || a.supportSvc == nil {
		return nil, fmt.Errorf("support service indisponivel")
	}
	return a.supportSvc.AddAgentTicketComment(ticketID, content, isInternal)
}

func (a *App) CreateAgentTicket(title, description string, priority int, category string) (json.RawMessage, error) {
	if a == nil || a.supportSvc == nil {
		return nil, fmt.Errorf("support service indisponivel")
	}
	return a.supportSvc.CreateAgentTicket(title, description, priority, category)
}

func (a *App) GetKnowledgeArticles(category string) ([]KnowledgeArticle, error) {
	if a == nil || a.supportSvc == nil {
		return []KnowledgeArticle{}, fmt.Errorf("support service indisponivel")
	}
	return a.supportSvc.GetKnowledgeArticles(category)
}

func (a *App) GetKnowledgeArticleDetails(articleID string) (KnowledgeArticle, error) {
	if a == nil || a.supportSvc == nil {
		return KnowledgeArticle{}, fmt.Errorf("support service indisponivel")
	}
	return a.supportSvc.GetKnowledgeArticleDetails(articleID)
}

// Allow support service to reuse configuration types.
func (a *App) GetAgentConfiguration() AgentConfiguration {
	return a.getAgentConfiguration()
}

func (a *App) getAgentConfiguration() AgentConfiguration {
	a.agentConfigMu.RLock()
	cfg := a.agentConfig
	a.agentConfigMu.RUnlock()
	return cfg
}

// GetAgentConfiguration is already defined in app.go; keep this proxy for support usage.
func (a *App) GetAgentConfigurationForSupport() AgentConfiguration {
	return a.GetAgentConfiguration()
}

func (a *App) GetLogsOverview() (models.LogOverview, error) {
	if a == nil || a.supportSvc == nil {
		return models.LogOverview{}, fmt.Errorf("support service indisponivel")
	}
	return a.supportSvc.GetLogsOverview()
}
