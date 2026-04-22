package app

import "encoding/json"

func (a *App) GetAgentInfo() (AgentInfo, error) {
	if err := a.requireSupportSvc(); err != nil {
		return AgentInfo{}, err
	}
	return a.supportSvc.GetAgentInfo()
}

func (a *App) GetSupportTickets() ([]APITicket, error) {
	if err := a.requireSupportSvc(); err != nil {
		return []APITicket{}, err
	}
	return a.supportSvc.GetSupportTickets()
}

func (a *App) CreateSupportTicket(input CreateTicketInput) (APITicket, error) {
	if err := a.requireSupportSvc(); err != nil {
		return APITicket{}, err
	}
	return a.supportSvc.CreateSupportTicket(input)
}

func (a *App) GetSupportTicketDetails(ticketID string) (APITicket, error) {
	if err := a.requireSupportSvc(); err != nil {
		return APITicket{}, err
	}
	return a.supportSvc.GetSupportTicketDetails(ticketID)
}

func (a *App) GetTicketWorkflowStates() ([]APIWorkflowState, error) {
	if err := a.requireSupportSvc(); err != nil {
		return []APIWorkflowState{}, err
	}
	return a.supportSvc.GetTicketWorkflowStates()
}

func (a *App) GetTicketComments(ticketID string) ([]TicketComment, error) {
	if err := a.requireSupportSvc(); err != nil {
		return []TicketComment{}, err
	}
	return a.supportSvc.GetTicketComments(ticketID)
}

func (a *App) AddTicketCommentWithOptions(ticketID, content string, isInternal bool) (TicketComment, error) {
	if err := a.requireSupportSvc(); err != nil {
		return TicketComment{}, err
	}
	return a.supportSvc.AddTicketCommentWithOptions(ticketID, content, isInternal)
}

func (a *App) AddTicketComment(ticketID, author, content string) error {
	if err := a.requireSupportSvc(); err != nil {
		return err
	}
	return a.supportSvc.AddTicketComment(ticketID, author, content)
}

func (a *App) CloseSupportTicket(ticketID string, input CloseTicketInput) (APITicket, error) {
	if err := a.requireSupportSvc(); err != nil {
		return APITicket{}, err
	}
	return a.supportSvc.CloseSupportTicket(ticketID, input)
}

func (a *App) CloseAgentTicket(ticketID string, rating *int, comment, workflowStateID string) (json.RawMessage, error) {
	if err := a.requireSupportSvc(); err != nil {
		return nil, err
	}
	return a.supportSvc.CloseAgentTicket(ticketID, rating, comment, workflowStateID)
}

func (a *App) GetAgentInfoJSON() (json.RawMessage, error) {
	if err := a.requireSupportSvc(); err != nil {
		return nil, err
	}
	return a.supportSvc.GetAgentInfoJSON()
}

func (a *App) ListAgentTickets() (json.RawMessage, error) {
	if err := a.requireSupportSvc(); err != nil {
		return nil, err
	}
	return a.supportSvc.ListAgentTickets()
}

func (a *App) GetAgentTicketDetails(ticketID string) (json.RawMessage, error) {
	if err := a.requireSupportSvc(); err != nil {
		return nil, err
	}
	return a.supportSvc.GetAgentTicketDetails(ticketID)
}

func (a *App) AddAgentTicketComment(ticketID, content string, isInternal bool) (json.RawMessage, error) {
	if err := a.requireSupportSvc(); err != nil {
		return nil, err
	}
	return a.supportSvc.AddAgentTicketComment(ticketID, content, isInternal)
}

func (a *App) CreateAgentTicket(title, description string, priority int, category string) (json.RawMessage, error) {
	if err := a.requireSupportSvc(); err != nil {
		return nil, err
	}
	return a.supportSvc.CreateAgentTicket(title, description, priority, category)
}

func (a *App) GetKnowledgeArticles(category string) ([]KnowledgeArticle, error) {
	if err := a.requireSupportSvc(); err != nil {
		return []KnowledgeArticle{}, err
	}
	return a.supportSvc.GetKnowledgeArticles(category)
}

func (a *App) GetKnowledgeArticleDetails(articleID string) (KnowledgeArticle, error) {
	if err := a.requireSupportSvc(); err != nil {
		return KnowledgeArticle{}, err
	}
	return a.supportSvc.GetKnowledgeArticleDetails(articleID)
}
