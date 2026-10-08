package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"discovery/app/core/consent"
	applocale "discovery/app/services/locale"
)

// RequestToolConsent é o gate de autorização do chat: pede aprovação do
// USUÁRIO antes de a IA executar uma ação com efeito no computador (gravar
// arquivo, instalar/desinstalar programa, parar serviço, reiniciar...).
//
// É POR AÇÃO (não existe "permitir sempre") e fail-closed: qualquer resposta
// diferente do rótulo localizado de aprovação conta como NEGATIVA. O texto do
// diálogo é localizado (pt/en/es) por core/consent.
//
// Motivação: as tools destrutivas dependiam de um parâmetro confirm=true que o
// PRÓPRIO LLM preenchia — não havia confirmação real do usuário. Agora o gate
// em services/chat consulta core/mcp.ToolConsentFor antes de executar a tool.
func (a *App) RequestToolConsent(ctx context.Context, req consent.Request) (bool, error) {
	if ctx == nil {
		ctx = a.ctx
	}

	dialog := consent.Build(req, applocale.DetectPreferredLocale())
	optionsJSON, _ := json.Marshal([]string{dialog.Approve, dialog.Deny})
	answer, askErr := a.AskUserContext(ctx, dialog.Question, string(optionsJSON), "false")
	if askErr != nil {
		a.logToolConsent("cancelado", req, askErr.Error())
		return false, askErr
	}

	if !strings.EqualFold(strings.TrimSpace(answer), dialog.Approve) {
		a.logToolConsent("negado", req, "resposta do usuario: "+strings.TrimSpace(answer))
		return false, nil
	}

	a.logToolConsent("autorizado", req, req.Target)
	return true, nil
}

// logToolConsent registra a decisão no log do agente (auditoria).
func (a *App) logToolConsent(outcome string, req consent.Request, detail string) {
	if a == nil {
		return
	}
	a.Logs.Append(fmt.Sprintf("[consent] %s kind=%s action=%s target=%q %s",
		outcome, req.Kind, req.Action, req.Target, detail))
}
