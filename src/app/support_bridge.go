package app

import "encoding/json"

func (a *App) GetAgentInfo() (AgentInfo, error) {
	if err := a.requireSupportSvc(); err != nil {
		return AgentInfo{}, err
	}
	return a.SupportSvc.GetAgentInfo()
}

func (a *App) GetSupportTickets() ([]APITicket, error) {
	if err := a.requireSupportSvc(); err != nil {
		return []APITicket{}, err
	}
	return a.SupportSvc.GetSupportTickets()
}

func (a *App) GetTicketOptions() (TicketOptions, error) {
	if err := a.requireSupportSvc(); err != nil {
		return TicketOptions{}, err
	}
	return a.SupportSvc.GetTicketOptions()
}

func (a *App) CreateSupportTicket(input CreateTicketInput) (APITicket, error) {
	if err := a.requireSupportSvc(); err != nil {
		return APITicket{}, err
	}
	return a.SupportSvc.CreateSupportTicket(input)
}

func (a *App) GetSupportTicketDetails(ticketID string) (APITicket, error) {
	if err := a.requireSupportSvc(); err != nil {
		return APITicket{}, err
	}
	return a.SupportSvc.GetSupportTicketDetails(ticketID)
}

func (a *App) GetTicketWorkflowStates() ([]APIWorkflowState, error) {
	if err := a.requireSupportSvc(); err != nil {
		return []APIWorkflowState{}, err
	}
	return a.SupportSvc.GetTicketWorkflowStates()
}

func (a *App) GetTicketComments(ticketID string) ([]TicketComment, error) {
	if err := a.requireSupportSvc(); err != nil {
		return []TicketComment{}, err
	}
	return a.SupportSvc.GetTicketComments(ticketID)
}

func (a *App) AddTicketCommentWithOptions(ticketID, content string) (TicketComment, error) {
	if err := a.requireSupportSvc(); err != nil {
		return TicketComment{}, err
	}
	return a.SupportSvc.AddTicketCommentWithOptions(ticketID, content)
}

func (a *App) AddTicketComment(ticketID, author, content string) error {
	if err := a.requireSupportSvc(); err != nil {
		return err
	}
	return a.SupportSvc.AddTicketComment(ticketID, author, content)
}

func (a *App) GetKnowledgeBaseArticles() []KnowledgeArticle {
	if err := a.requireSupportSvc(); err != nil {
		return []KnowledgeArticle{}
	}
	return a.SupportSvc.GetKnowledgeBaseArticles()
}

// RefreshKnowledgeBase limpa o cache local e recarrega os artigos da API.
func (a *App) RefreshKnowledgeBase() error {
	if err := a.requireSupportSvc(); err != nil {
		return err
	}
	return a.SupportSvc.RefreshKnowledgeBase()
}

func (a *App) CloseSupportTicket(ticketID string, input CloseTicketInput) (APITicket, error) {
	if err := a.requireSupportSvc(); err != nil {
		return APITicket{}, err
	}
	return a.SupportSvc.CloseSupportTicket(ticketID, input)
}

func (a *App) CloseAgentTicket(ticketID string, rating *int, comment, workflowStateID string) (json.RawMessage, error) {
	if err := a.requireSupportSvc(); err != nil {
		return nil, err
	}
	return a.SupportSvc.CloseAgentTicket(ticketID, rating, comment, workflowStateID)
}

func (a *App) GetAgentInfoJSON() (json.RawMessage, error) {
	if err := a.requireSupportSvc(); err != nil {
		return nil, err
	}
	return a.SupportSvc.GetAgentInfoJSON()
}

func (a *App) ListAgentTickets() (json.RawMessage, error) {
	if err := a.requireSupportSvc(); err != nil {
		return nil, err
	}
	return a.SupportSvc.ListAgentTickets()
}

func (a *App) GetAgentTicketDetails(ticketID string) (json.RawMessage, error) {
	if err := a.requireSupportSvc(); err != nil {
		return nil, err
	}
	return a.SupportSvc.GetAgentTicketDetails(ticketID)
}

func (a *App) AddAgentTicketComment(ticketID, content string) (json.RawMessage, error) {
	if err := a.requireSupportSvc(); err != nil {
		return nil, err
	}
	return a.SupportSvc.AddAgentTicketComment(ticketID, content)
}

func (a *App) CreateAgentTicket(title, description string, priority int, category, templateID, customFieldsJSON, templateAnswersJSON string) (json.RawMessage, error) {
	if err := a.requireSupportSvc(); err != nil {
		return nil, err
	}
	return a.SupportSvc.CreateAgentTicket(title, description, priority, category, templateID, customFieldsJSON, templateAnswersJSON)
}

func (a *App) ListAgentTicketTemplates() (json.RawMessage, error) {
	if err := a.requireSupportSvc(); err != nil {
		return nil, err
	}
	return a.SupportSvc.ListAgentTicketTemplates()
}

func (a *App) GetKnowledgeArticles(category string) ([]KnowledgeArticle, error) {
	if err := a.requireSupportSvc(); err != nil {
		return []KnowledgeArticle{}, err
	}
	return a.SupportSvc.GetKnowledgeArticles(category)
}

func (a *App) GetKnowledgeArticleDetails(articleID string) (KnowledgeArticle, error) {
	if err := a.requireSupportSvc(); err != nil {
		return KnowledgeArticle{}, err
	}
	return a.SupportSvc.GetKnowledgeArticleDetails(articleID)
}

func (a *App) GetKnowledgeArticlePages(articleID string) ([]KnowledgePage, error) {
	if err := a.requireSupportSvc(); err != nil {
		return []KnowledgePage{}, err
	}
	return a.SupportSvc.GetKnowledgeArticlePages(articleID)
}
