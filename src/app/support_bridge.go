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

// GetTicketTemplates expõe os modelos de abertura de chamado para o formulário
// da aba Suporte. Lista tipada (o MCP usa ListAgentTicketTemplates em JSON) para
// o binding JS receber questions/fields estruturados.
func (a *App) GetTicketTemplates() ([]TicketTemplateOption, error) {
	if err := a.requireSupportSvc(); err != nil {
		return []TicketTemplateOption{}, err
	}
	raw, err := a.SupportSvc.ListAgentTicketTemplates()
	if err != nil {
		return []TicketTemplateOption{}, err
	}
	var templates []TicketTemplateOption
	if err := json.Unmarshal(raw, &templates); err != nil {
		return []TicketTemplateOption{}, err
	}
	if templates == nil {
		templates = []TicketTemplateOption{}
	}
	return templates, nil
}

// GetTicketDepartmentFields expõe os campos personalizados públicos de um
// departamento para o formulário de abertura do suporte. Esses campos valem
// para todo chamado do departamento, com ou sem template.
func (a *App) GetTicketDepartmentFields(departmentID string) ([]TicketDepartmentField, error) {
	if err := a.requireSupportSvc(); err != nil {
		return []TicketDepartmentField{}, err
	}
	return a.SupportSvc.GetTicketDepartmentFields(departmentID)
}

// ListDepartmentFieldsJSON expõe os campos personalizados de um departamento
// como JSON para as ferramentas MCP da IA (create_ticket/customFields).
func (a *App) ListDepartmentFieldsJSON(departmentID string) (json.RawMessage, error) {
	if err := a.requireSupportSvc(); err != nil {
		return nil, err
	}
	fields, err := a.SupportSvc.GetTicketDepartmentFields(departmentID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(fields)
}

// GetTicketFields expõe os campos personalizados do departamento do chamado
// (com os valores gravados) para o detalhe da aba Suporte.
func (a *App) GetTicketFields(ticketID string) ([]TicketFieldValue, error) {
	if err := a.requireSupportSvc(); err != nil {
		return []TicketFieldValue{}, err
	}
	return a.SupportSvc.GetTicketFields(ticketID)
}

// GetTicketAnswers expõe as respostas do mini questionário do template do
// chamado para o detalhe da aba Suporte.
func (a *App) GetTicketAnswers(ticketID string) ([]TicketAnswer, error) {
	if err := a.requireSupportSvc(); err != nil {
		return []TicketAnswer{}, err
	}
	return a.SupportSvc.GetTicketAnswers(ticketID)
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

// ReopenAgentTicket reabre um chamado encerrado via MCP tool e devolve o
// ticket atualizado como JSON. Reabrir descarta a avaliação anterior e volta a
// permitir comentários.
func (a *App) ReopenAgentTicket(ticketID, reason string) (json.RawMessage, error) {
	if err := a.requireSupportSvc(); err != nil {
		return nil, err
	}
	ticket, err := a.SupportSvc.ReopenSupportTicket(ticketID, reason)
	if err != nil {
		return nil, err
	}
	return json.Marshal(ticket)
}

// RateAgentTicket avalia (CSAT 1..5) um chamado encerrado via MCP tool e
// devolve o ticket atualizado como JSON.
func (a *App) RateAgentTicket(ticketID string, rating int, feedback string) (json.RawMessage, error) {
	if err := a.requireSupportSvc(); err != nil {
		return nil, err
	}
	ticket, err := a.SupportSvc.RateSupportTicket(ticketID, rating, feedback)
	if err != nil {
		return nil, err
	}
	return json.Marshal(ticket)
}

// ReopenSupportTicket reabre um chamado encerrado do agent (libera novos
// comentários). A avaliação anterior é descartada pelo servidor.
func (a *App) ReopenSupportTicket(ticketID string) (APITicket, error) {
	if err := a.requireSupportSvc(); err != nil {
		return APITicket{}, err
	}
	return a.SupportSvc.ReopenSupportTicket(ticketID, "")
}

// RateSupportTicket registra a avaliação (CSAT 1..5) e o feedback de um
// chamado encerrado.
func (a *App) RateSupportTicket(ticketID string, rating int, feedback string) (APITicket, error) {
	if err := a.requireSupportSvc(); err != nil {
		return APITicket{}, err
	}
	return a.SupportSvc.RateSupportTicket(ticketID, rating, feedback)
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

func (a *App) CreateAgentTicket(title, description string, priority int, category, templateID, departmentID, customFieldsJSON, templateAnswersJSON string) (json.RawMessage, error) {
	if err := a.requireSupportSvc(); err != nil {
		return nil, err
	}
	return a.SupportSvc.CreateAgentTicket(title, description, priority, category, templateID, departmentID, customFieldsJSON, templateAnswersJSON)
}

// ListTicketDepartmentsJSON expõe os departamentos de abertura de chamado
// (globais + do cliente) para a IA escolher o responsável pelo atendimento.
func (a *App) ListTicketDepartmentsJSON() (json.RawMessage, error) {
	if err := a.requireSupportSvc(); err != nil {
		return nil, err
	}
	options, err := a.SupportSvc.GetTicketOptions()
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"departments": options.Departments})
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
