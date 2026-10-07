package app

import (
	"encoding/json"

	appsupport "discovery/app/support"
)

// Os métodos abaixo são os bindings Wails consumidos pelo frontend. Em modo
// companion a UI não tem DB (decisão D3): o helper supportReadOrLocal /
// supportWriteOrLocal roteia o RPC ao serviço (dono do SQLite e dos snapshots
// offline) e só executa o caminho local no modo serviço/standalone. Ver
// support_companion.go e ipc_rpc_support.go.

func (a *App) GetAgentInfo() (AgentInfo, error) {
	if err := a.requireSupportSvc(); err != nil {
		return AgentInfo{}, err
	}
	return supportReadOrLocal(a, "support:agent_info", nil, func() (AgentInfo, error) {
		return a.SupportSvc.GetAgentInfo()
	})
}

func (a *App) GetSupportTickets() ([]APITicket, error) {
	if err := a.requireSupportSvc(); err != nil {
		return []APITicket{}, err
	}
	list, err := supportReadOrLocal(a, "support:tickets", nil, func() (appsupport.SupportTicketList, error) {
		return a.SupportSvc.GetSupportTicketList()
	})
	if err != nil {
		return []APITicket{}, err
	}
	return list.Tickets, nil
}

// GetSupportTicketsWithStatus expõe a listagem + o flag de cache offline
// (stale) para a aba Suporte avisar que os dados podem estar desatualizados.
func (a *App) GetSupportTicketsWithStatus() (appsupport.SupportTicketList, error) {
	if err := a.requireSupportSvc(); err != nil {
		return appsupport.SupportTicketList{}, err
	}
	return supportReadOrLocal(a, "support:tickets", nil, func() (appsupport.SupportTicketList, error) {
		return a.SupportSvc.GetSupportTicketList()
	})
}

func (a *App) GetTicketOptions() (TicketOptions, error) {
	if err := a.requireSupportSvc(); err != nil {
		return TicketOptions{}, err
	}
	return supportReadOrLocal(a, "support:options", nil, func() (TicketOptions, error) {
		return a.SupportSvc.GetTicketOptions()
	})
}

// GetTicketTemplates expõe os modelos de abertura de chamado para o formulário
// da aba Suporte. Lista tipada (o MCP usa ListAgentTicketTemplates em JSON) para
// o binding JS receber questions/fields estruturados.
func (a *App) GetTicketTemplates() ([]TicketTemplateOption, error) {
	if err := a.requireSupportSvc(); err != nil {
		return []TicketTemplateOption{}, err
	}
	raw, err := supportReadOrLocal(a, "support:templates", nil, func() (json.RawMessage, error) {
		return a.SupportSvc.ListAgentTicketTemplates()
	})
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
	return supportReadOrLocal(a, "support:department_fields", map[string]any{"departmentId": departmentID}, func() ([]TicketDepartmentField, error) {
		return a.SupportSvc.GetTicketDepartmentFields(departmentID)
	})
}

// ListDepartmentFieldsJSON expõe os campos personalizados de um departamento
// como JSON para as ferramentas MCP da IA (create_ticket/customFields).
func (a *App) ListDepartmentFieldsJSON(departmentID string) (json.RawMessage, error) {
	if err := a.requireSupportSvc(); err != nil {
		return nil, err
	}
	fields, err := supportReadOrLocal(a, "support:department_fields", map[string]any{"departmentId": departmentID}, func() ([]TicketDepartmentField, error) {
		return a.SupportSvc.GetTicketDepartmentFields(departmentID)
	})
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
	return supportReadOrLocal(a, "support:ticket_fields", map[string]any{"ticketId": ticketID}, func() ([]TicketFieldValue, error) {
		return a.SupportSvc.GetTicketFields(ticketID)
	})
}

// GetTicketAnswers expõe as respostas do mini questionário do template do
// chamado para o detalhe da aba Suporte.
func (a *App) GetTicketAnswers(ticketID string) ([]TicketAnswer, error) {
	if err := a.requireSupportSvc(); err != nil {
		return []TicketAnswer{}, err
	}
	return supportReadOrLocal(a, "support:ticket_answers", map[string]any{"ticketId": ticketID}, func() ([]TicketAnswer, error) {
		return a.SupportSvc.GetTicketAnswers(ticketID)
	})
}

func (a *App) CreateSupportTicket(input CreateTicketInput) (APITicket, error) {
	if err := a.requireSupportSvc(); err != nil {
		return APITicket{}, err
	}
	return supportWriteOrLocal(a, "support:create", map[string]any{"input": input}, func() (APITicket, error) {
		return a.SupportSvc.CreateSupportTicket(input)
	})
}

func (a *App) GetSupportTicketDetails(ticketID string) (APITicket, error) {
	if err := a.requireSupportSvc(); err != nil {
		return APITicket{}, err
	}
	return supportReadOrLocal(a, "support:ticket", map[string]any{"ticketId": ticketID}, func() (APITicket, error) {
		return a.SupportSvc.GetSupportTicketDetails(ticketID)
	})
}

func (a *App) GetTicketWorkflowStates() ([]APIWorkflowState, error) {
	if err := a.requireSupportSvc(); err != nil {
		return []APIWorkflowState{}, err
	}
	return supportReadOrLocal(a, "support:workflow_states", nil, func() ([]APIWorkflowState, error) {
		return a.SupportSvc.GetTicketWorkflowStates()
	})
}

func (a *App) GetTicketComments(ticketID string) ([]TicketComment, error) {
	if err := a.requireSupportSvc(); err != nil {
		return []TicketComment{}, err
	}
	return supportReadOrLocal(a, "support:comments", map[string]any{"ticketId": ticketID}, func() ([]TicketComment, error) {
		return a.SupportSvc.GetTicketComments(ticketID)
	})
}

func (a *App) AddTicketCommentWithOptions(ticketID, content string) (TicketComment, error) {
	if err := a.requireSupportSvc(); err != nil {
		return TicketComment{}, err
	}
	return supportWriteOrLocal(a, "support:comment_add", map[string]any{"ticketId": ticketID, "content": content}, func() (TicketComment, error) {
		return a.SupportSvc.AddTicketCommentWithOptions(ticketID, content)
	})
}

func (a *App) AddTicketComment(ticketID, author, content string) error {
	if err := a.requireSupportSvc(); err != nil {
		return err
	}
	_, err := supportWriteOrLocal(a, "support:comment_add_legacy", map[string]any{"ticketId": ticketID, "author": author, "content": content}, func() (struct{}, error) {
		return struct{}{}, a.SupportSvc.AddTicketComment(ticketID, author, content)
	})
	return err
}

// GetKnowledgeBaseArticlesWithStatus expõe os artigos + o flag de cache offline
// (stale) para a UI avisar que os dados podem estar desatualizados.
func (a *App) GetKnowledgeBaseArticlesWithStatus() (appsupport.KnowledgeArticleList, error) {
	if err := a.requireSupportSvc(); err != nil {
		return appsupport.KnowledgeArticleList{}, err
	}
	return supportReadOrLocal(a, "knowledge:list", nil, func() (appsupport.KnowledgeArticleList, error) {
		return a.SupportSvc.GetKnowledgeBaseArticleList()
	})
}

func (a *App) GetKnowledgeBaseArticles() []KnowledgeArticle {
	list, err := a.GetKnowledgeBaseArticlesWithStatus()
	if err != nil || list.Articles == nil {
		return []KnowledgeArticle{}
	}
	return list.Articles
}

// RefreshKnowledgeBase limpa o cache local e recarrega os artigos da API.
func (a *App) RefreshKnowledgeBase() error {
	if err := a.requireSupportSvc(); err != nil {
		return err
	}
	_, err := supportWriteOrLocal(a, "knowledge:refresh", nil, func() (struct{}, error) {
		return struct{}{}, a.SupportSvc.RefreshKnowledgeBase()
	})
	return err
}

func (a *App) CloseSupportTicket(ticketID string, input CloseTicketInput) (APITicket, error) {
	if err := a.requireSupportSvc(); err != nil {
		return APITicket{}, err
	}
	return supportWriteOrLocal(a, "support:close", map[string]any{"ticketId": ticketID, "input": input}, func() (APITicket, error) {
		return a.SupportSvc.CloseSupportTicket(ticketID, input)
	})
}

// ReopenAgentTicket reabre um chamado encerrado via MCP tool e devolve o
// ticket atualizado como JSON. Reabrir descarta a avaliação anterior e volta a
// permitir comentários.
func (a *App) ReopenAgentTicket(ticketID, reason string) (json.RawMessage, error) {
	if err := a.requireSupportSvc(); err != nil {
		return nil, err
	}
	ticket, err := supportWriteOrLocal(a, "support:reopen", map[string]any{"ticketId": ticketID, "reason": reason}, func() (APITicket, error) {
		return a.SupportSvc.ReopenSupportTicket(ticketID, reason)
	})
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
	ticket, err := supportWriteOrLocal(a, "support:rate", map[string]any{"ticketId": ticketID, "rating": float64(rating), "feedback": feedback}, func() (APITicket, error) {
		return a.SupportSvc.RateSupportTicket(ticketID, rating, feedback)
	})
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
	return supportWriteOrLocal(a, "support:reopen", map[string]any{"ticketId": ticketID}, func() (APITicket, error) {
		return a.SupportSvc.ReopenSupportTicket(ticketID, "")
	})
}

// RateSupportTicket registra a avaliação (CSAT 1..5) e o feedback de um
// chamado encerrado.
func (a *App) RateSupportTicket(ticketID string, rating int, feedback string) (APITicket, error) {
	if err := a.requireSupportSvc(); err != nil {
		return APITicket{}, err
	}
	return supportWriteOrLocal(a, "support:rate", map[string]any{"ticketId": ticketID, "rating": float64(rating), "feedback": feedback}, func() (APITicket, error) {
		return a.SupportSvc.RateSupportTicket(ticketID, rating, feedback)
	})
}

func (a *App) CloseAgentTicket(ticketID string, rating *int, comment, workflowStateID string) (json.RawMessage, error) {
	if err := a.requireSupportSvc(); err != nil {
		return nil, err
	}
	payload := map[string]any{"ticketId": ticketID, "comment": comment, "workflowStateId": workflowStateID}
	if rating != nil {
		payload["rating"] = float64(*rating)
	}
	return supportWriteOrLocal(a, "support:agent_ticket_close", payload, func() (json.RawMessage, error) {
		return a.SupportSvc.CloseAgentTicket(ticketID, rating, comment, workflowStateID)
	})
}

func (a *App) GetAgentInfoJSON() (json.RawMessage, error) {
	if err := a.requireSupportSvc(); err != nil {
		return nil, err
	}
	return supportReadOrLocal(a, "support:agent_info_json", nil, func() (json.RawMessage, error) {
		return a.SupportSvc.GetAgentInfoJSON()
	})
}

func (a *App) ListAgentTickets() (json.RawMessage, error) {
	if err := a.requireSupportSvc(); err != nil {
		return nil, err
	}
	return supportReadOrLocal(a, "support:agent_tickets", nil, func() (json.RawMessage, error) {
		return a.SupportSvc.ListAgentTickets()
	})
}

func (a *App) GetAgentTicketDetails(ticketID string) (json.RawMessage, error) {
	if err := a.requireSupportSvc(); err != nil {
		return nil, err
	}
	return supportReadOrLocal(a, "support:agent_ticket", map[string]any{"ticketId": ticketID}, func() (json.RawMessage, error) {
		return a.SupportSvc.GetAgentTicketDetails(ticketID)
	})
}

func (a *App) AddAgentTicketComment(ticketID, content string) (json.RawMessage, error) {
	if err := a.requireSupportSvc(); err != nil {
		return nil, err
	}
	return supportWriteOrLocal(a, "support:agent_comment_add", map[string]any{"ticketId": ticketID, "content": content}, func() (json.RawMessage, error) {
		return a.SupportSvc.AddAgentTicketComment(ticketID, content)
	})
}

func (a *App) CreateAgentTicket(title, description string, priority int, category, templateID, departmentID, customFieldsJSON, templateAnswersJSON string) (json.RawMessage, error) {
	if err := a.requireSupportSvc(); err != nil {
		return nil, err
	}
	payload := map[string]any{
		"title":               title,
		"description":         description,
		"priority":            float64(priority),
		"category":            category,
		"templateId":          templateID,
		"departmentId":        departmentID,
		"customFieldsJson":    customFieldsJSON,
		"templateAnswersJson": templateAnswersJSON,
	}
	return supportWriteOrLocal(a, "support:agent_ticket_create", payload, func() (json.RawMessage, error) {
		return a.SupportSvc.CreateAgentTicket(title, description, priority, category, templateID, departmentID, customFieldsJSON, templateAnswersJSON)
	})
}

// ListTicketDepartmentsJSON expõe os departamentos de abertura de chamado
// (globais + do cliente) para a IA escolher o responsável pelo atendimento.
func (a *App) ListTicketDepartmentsJSON() (json.RawMessage, error) {
	if err := a.requireSupportSvc(); err != nil {
		return nil, err
	}
	options, err := supportReadOrLocal(a, "support:options", nil, func() (TicketOptions, error) {
		return a.SupportSvc.GetTicketOptions()
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"departments": options.Departments})
}

func (a *App) ListAgentTicketTemplates() (json.RawMessage, error) {
	if err := a.requireSupportSvc(); err != nil {
		return nil, err
	}
	return supportReadOrLocal(a, "support:templates", nil, func() (json.RawMessage, error) {
		return a.SupportSvc.ListAgentTicketTemplates()
	})
}

func (a *App) GetKnowledgeArticles(category string) ([]KnowledgeArticle, error) {
	if err := a.requireSupportSvc(); err != nil {
		return []KnowledgeArticle{}, err
	}
	return supportReadOrLocal(a, "knowledge:articles", map[string]any{"category": category}, func() ([]KnowledgeArticle, error) {
		return a.SupportSvc.GetKnowledgeArticles(category)
	})
}

func (a *App) GetKnowledgeArticleDetails(articleID string) (KnowledgeArticle, error) {
	if err := a.requireSupportSvc(); err != nil {
		return KnowledgeArticle{}, err
	}
	return supportReadOrLocal(a, "knowledge:article", map[string]any{"articleId": articleID}, func() (KnowledgeArticle, error) {
		return a.SupportSvc.GetKnowledgeArticleDetails(articleID)
	})
}

func (a *App) GetKnowledgeArticlePages(articleID string) ([]KnowledgePage, error) {
	if err := a.requireSupportSvc(); err != nil {
		return []KnowledgePage{}, err
	}
	return supportReadOrLocal(a, "knowledge:pages", map[string]any{"articleId": articleID}, func() ([]KnowledgePage, error) {
		return a.SupportSvc.GetKnowledgeArticlePages(articleID)
	})
}
