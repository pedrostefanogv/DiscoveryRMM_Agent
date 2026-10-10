package app

// ipc_rpc_support.go — handlers RPC do serviço para Suporte e Base de
// Conhecimento (PLANO_SEPARACAO_SERVICO_UI.md, decisão D3: DB único, dono =
// serviço).
//
// Contexto: a UI companion NÃO abre o SQLite do serviço. Sem estes RPCs, os
// caches offline de chamados (tickets:backup:*) e da base de conhecimento
// (knowledge:backup:*) ficavam inalcançáveis na tela — a UI só tinha o caminho
// de rede e falhava quando o servidor estava fora do ar, mesmo com o snapshot
// local válido gravado pelo serviço. Os handlers aqui expõem o SupportSvc do
// serviço (que tem DB, identidade durável e snapshots) à UI companion.

import (
	"encoding/json"
	"strings"

	appsupport "discovery/app/support"
)

// ipcRPCError padroniza a resposta de erro do RPC.
func ipcRPCError(err error) map[string]any {
	if err == nil {
		return map[string]any{"ok": false, "error": "erro desconhecido"}
	}
	return map[string]any{"ok": false, "error": err.Error()}
}

// ipcRPCResult serializa um resultado de domínio como json.RawMessage dentro
// do envelope {"ok","data":{"result"}}. O cliente reidrata o tipo concreto —
// o dispatcher não precisa conhecer cada DTO.
func ipcRPCResult(v any) map[string]any {
	raw, err := json.Marshal(v)
	if err != nil {
		return ipcRPCError(err)
	}
	return map[string]any{"ok": true, "data": map[string]any{"result": json.RawMessage(raw)}}
}

func ipcRPCString(payload map[string]any, key string) string {
	s, _ := payload[key].(string)
	return strings.TrimSpace(s)
}

func ipcRPCInt(payload map[string]any, key string) int {
	switch n := payload[key].(type) {
	case float64:
		return int(n)
	case float32:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
	}
	return 0
}

// ipcRPCDecodeInput reidrata um objeto/valor JSON do payload no tipo do domínio.
func ipcRPCDecodeInput(payload map[string]any, key string, out any) error {
	raw, err := json.Marshal(payload[key])
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// ipcRPCSupportCall centraliza o guard do serviço + marshal do resultado.
func (a *App) ipcRPCSupportCall(fn func() (any, error)) map[string]any {
	if err := a.requireSupportSvc(); err != nil {
		return ipcRPCError(err)
	}
	out, err := fn()
	if err != nil {
		return ipcRPCError(err)
	}
	return ipcRPCResult(out)
}

// ── Identidade / chamados ──────────────────────────────────────────────────

func (a *App) ipcRPCSupportAgentInfo() map[string]any {
	return a.ipcRPCSupportCall(func() (any, error) { return a.SupportSvc.GetAgentInfo() })
}

func (a *App) ipcRPCSupportAgentInfoJSON() map[string]any {
	return a.ipcRPCSupportCall(func() (any, error) { return a.SupportSvc.GetAgentInfoJSON() })
}

func (a *App) ipcRPCSupportTicketList() map[string]any {
	return a.ipcRPCSupportCall(func() (any, error) { return a.SupportSvc.GetSupportTicketList() })
}

func (a *App) ipcRPCSupportTicket(payload map[string]any) map[string]any {
	ticketID := ipcRPCString(payload, "ticketId")
	return a.ipcRPCSupportCall(func() (any, error) { return a.SupportSvc.GetSupportTicketDetails(ticketID) })
}

func (a *App) ipcRPCSupportTicketFields(payload map[string]any) map[string]any {
	ticketID := ipcRPCString(payload, "ticketId")
	return a.ipcRPCSupportCall(func() (any, error) { return a.SupportSvc.GetTicketFields(ticketID) })
}

func (a *App) ipcRPCSupportTicketAnswers(payload map[string]any) map[string]any {
	ticketID := ipcRPCString(payload, "ticketId")
	return a.ipcRPCSupportCall(func() (any, error) { return a.SupportSvc.GetTicketAnswers(ticketID) })
}

func (a *App) ipcRPCSupportComments(payload map[string]any) map[string]any {
	ticketID := ipcRPCString(payload, "ticketId")
	return a.ipcRPCSupportCall(func() (any, error) { return a.SupportSvc.GetTicketComments(ticketID) })
}

func (a *App) ipcRPCSupportOptions() map[string]any {
	return a.ipcRPCSupportCall(func() (any, error) { return a.SupportSvc.GetTicketOptions() })
}

func (a *App) ipcRPCSupportDepartmentFields(payload map[string]any) map[string]any {
	departmentID := ipcRPCString(payload, "departmentId")
	return a.ipcRPCSupportCall(func() (any, error) { return a.SupportSvc.GetTicketDepartmentFields(departmentID) })
}

func (a *App) ipcRPCSupportDepartmentFormSchema(payload map[string]any) map[string]any {
	departmentID := ipcRPCString(payload, "departmentId")
	return a.ipcRPCSupportCall(func() (any, error) { return a.SupportSvc.GetTicketDepartmentFormSchema(departmentID) })
}

func (a *App) ipcRPCSupportWorkflowStates() map[string]any {
	return a.ipcRPCSupportCall(func() (any, error) { return a.SupportSvc.GetTicketWorkflowStates() })
}

func (a *App) ipcRPCSupportTemplates() map[string]any {
	return a.ipcRPCSupportCall(func() (any, error) { return a.SupportSvc.ListAgentTicketTemplates() })
}

func (a *App) ipcRPCSupportAgentTickets() map[string]any {
	return a.ipcRPCSupportCall(func() (any, error) { return a.SupportSvc.ListAgentTickets() })
}

func (a *App) ipcRPCSupportAgentTicket(payload map[string]any) map[string]any {
	ticketID := ipcRPCString(payload, "ticketId")
	return a.ipcRPCSupportCall(func() (any, error) { return a.SupportSvc.GetAgentTicketDetails(ticketID) })
}

// ── Mutações ───────────────────────────────────────────────────────────────

func (a *App) ipcRPCSupportCreate(payload map[string]any) map[string]any {
	if err := a.requireSupportSvc(); err != nil {
		return ipcRPCError(err)
	}
	var input appsupport.CreateTicketInput
	if err := ipcRPCDecodeInput(payload, "input", &input); err != nil {
		return ipcRPCError(err)
	}
	ticket, err := a.SupportSvc.CreateSupportTicket(input)
	if err != nil {
		return ipcRPCError(err)
	}
	return ipcRPCResult(ticket)
}

func (a *App) ipcRPCSupportAddComment(payload map[string]any) map[string]any {
	ticketID := ipcRPCString(payload, "ticketId")
	content, _ := payload["content"].(string)
	return a.ipcRPCSupportCall(func() (any, error) {
		return a.SupportSvc.AddTicketCommentWithOptions(ticketID, content)
	})
}

func (a *App) ipcRPCSupportAddCommentLegacy(payload map[string]any) map[string]any {
	ticketID := ipcRPCString(payload, "ticketId")
	author := ipcRPCString(payload, "author")
	content, _ := payload["content"].(string)
	return a.ipcRPCSupportCall(func() (any, error) {
		return struct{}{}, a.SupportSvc.AddTicketComment(ticketID, author, content)
	})
}

func (a *App) ipcRPCSupportClose(payload map[string]any) map[string]any {
	if err := a.requireSupportSvc(); err != nil {
		return ipcRPCError(err)
	}
	ticketID := ipcRPCString(payload, "ticketId")
	var input appsupport.CloseTicketInput
	if err := ipcRPCDecodeInput(payload, "input", &input); err != nil {
		return ipcRPCError(err)
	}
	ticket, err := a.SupportSvc.CloseSupportTicket(ticketID, input)
	if err != nil {
		return ipcRPCError(err)
	}
	return ipcRPCResult(ticket)
}

func (a *App) ipcRPCSupportReopen(payload map[string]any) map[string]any {
	ticketID := ipcRPCString(payload, "ticketId")
	reason := ipcRPCString(payload, "reason")
	return a.ipcRPCSupportCall(func() (any, error) {
		return a.SupportSvc.ReopenSupportTicket(ticketID, reason)
	})
}

func (a *App) ipcRPCSupportRate(payload map[string]any) map[string]any {
	ticketID := ipcRPCString(payload, "ticketId")
	rating := ipcRPCInt(payload, "rating")
	feedback, _ := payload["feedback"].(string)
	return a.ipcRPCSupportCall(func() (any, error) {
		return a.SupportSvc.RateSupportTicket(ticketID, rating, feedback)
	})
}

func (a *App) ipcRPCSupportAgentTicketCreate(payload map[string]any) map[string]any {
	title := ipcRPCString(payload, "title")
	description, _ := payload["description"].(string)
	priority := ipcRPCInt(payload, "priority")
	category := ipcRPCString(payload, "category")
	templateID := ipcRPCString(payload, "templateId")
	departmentID := ipcRPCString(payload, "departmentId")
	customFields, _ := payload["customFieldsJson"].(string)
	templateAnswers, _ := payload["templateAnswersJson"].(string)
	return a.ipcRPCSupportCall(func() (any, error) {
		return a.SupportSvc.CreateAgentTicket(title, description, priority, category, templateID, departmentID, customFields, templateAnswers)
	})
}

func (a *App) ipcRPCSupportAgentTicketClose(payload map[string]any) map[string]any {
	ticketID := ipcRPCString(payload, "ticketId")
	comment, _ := payload["comment"].(string)
	workflowStateID := ipcRPCString(payload, "workflowStateId")
	var rating *int
	if _, ok := payload["rating"]; ok && payload["rating"] != nil {
		r := ipcRPCInt(payload, "rating")
		rating = &r
	}
	return a.ipcRPCSupportCall(func() (any, error) {
		return a.SupportSvc.CloseAgentTicket(ticketID, rating, comment, workflowStateID)
	})
}

func (a *App) ipcRPCSupportAgentCommentAdd(payload map[string]any) map[string]any {
	ticketID := ipcRPCString(payload, "ticketId")
	content, _ := payload["content"].(string)
	return a.ipcRPCSupportCall(func() (any, error) {
		return a.SupportSvc.AddAgentTicketComment(ticketID, content)
	})
}

// ── Base de conhecimento ───────────────────────────────────────────────────

func (a *App) ipcRPCKnowledgeList() map[string]any {
	return a.ipcRPCSupportCall(func() (any, error) { return a.SupportSvc.GetKnowledgeBaseArticleList() })
}

func (a *App) ipcRPCKnowledgeArticles(payload map[string]any) map[string]any {
	category := ipcRPCString(payload, "category")
	return a.ipcRPCSupportCall(func() (any, error) { return a.SupportSvc.GetKnowledgeArticles(category) })
}

func (a *App) ipcRPCKnowledgeArticle(payload map[string]any) map[string]any {
	articleID := ipcRPCString(payload, "articleId")
	return a.ipcRPCSupportCall(func() (any, error) { return a.SupportSvc.GetKnowledgeArticleDetails(articleID) })
}

func (a *App) ipcRPCKnowledgePages(payload map[string]any) map[string]any {
	articleID := ipcRPCString(payload, "articleId")
	return a.ipcRPCSupportCall(func() (any, error) { return a.SupportSvc.GetKnowledgeArticlePages(articleID) })
}

func (a *App) ipcRPCKnowledgeRefresh() map[string]any {
	if err := a.requireSupportSvc(); err != nil {
		return ipcRPCError(err)
	}
	if err := a.SupportSvc.RefreshKnowledgeBase(); err != nil {
		return ipcRPCError(err)
	}
	return map[string]any{"ok": true, "data": map[string]any{}}
}
