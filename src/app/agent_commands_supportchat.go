package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	wailsnotifications "github.com/wailsapp/wails/v3/pkg/services/notifications"

	"discovery/app/agentcommands"
	"discovery/app/supportchat"
)

// ── Comando TicketChatInvite ────────────────────────────────────────────────
//
// Fluxo: o atendente inicia o chat no console -> o servidor envia este comando
// -> o agent mostra um pedido de confirmação (modal na janela principal,
// trazida à frente, + toast nativo quando o app está no tray) -> no ACEITE o
// agent responde ao servidor e abre a JANELA INDEPENDENTE do chat.

// supportChatInviteNotificationID identifica o pedido de confirmação.
const supportChatInviteNotificationID = "discovery.supportchat.invite"

// handleTicketChatInviteCommand trata o convite de chat de suporte.
func (a *App) handleTicketChatInviteCommand(parent context.Context, payload any) (bool, int, string, string) {
	invite, err := parseTicketChatInvitePayload(payload)
	if err != nil {
		return true, 2, "", err.Error()
	}

	invite.SessionID = strings.TrimSpace(invite.SessionID)
	invite.TicketID = strings.TrimSpace(invite.TicketID)
	if invite.SessionID == "" || invite.TicketID == "" {
		return true, 2, "", "payload de convite de chat sem sessionId/ticketId"
	}

	svc := a.supportChatService()

	// Uma sessão por máquina: se já existe OUTRO chat ativo, recusa o convite
	// imediatamente (o console mostra "máquina ocupada em outro chat").
	if current := svc.State(); current.Status == supportchat.StatusActive &&
		current.SessionID != "" && current.SessionID != invite.SessionID {
		a.respondSupportChatInvite(invite.SessionID, false, "busy")
		return true, 1, "", "maquina ocupada em outro chat de suporte"
	}

	svc.PrepareInvite(invite)

	// O modal de confirmação é renderizado na janela principal: garante que ela
	// esteja visível (o agent pode estar minimizado no tray) e dispara um toast
	// nativo como segundo canal de aviso.
	a.ShowMainWindow()
	a.notifySupportChatInvite(invite)

	// O prazo do modal segue o `expiresAtUtc` do convite (o servidor marca a
	// sessão como expirada nesse instante); 120s é apenas o fallback.
	timeout := resolveSupportChatInviteTimeoutSeconds(invite.ExpiresAt)
	response := a.DispatchNotification(NotificationDispatchRequest{
		NotificationID: supportChatInviteNotificationID + ":" + invite.SessionID,
		IdempotencyKey: supportChatInviteNotificationID + ":" + invite.SessionID,
		Title:          "Suporte iniciou um chat",
		Message:        buildSupportChatInviteMessage(invite),
		Mode:           "require_confirmation",
		Severity:       "medium",
		EventType:      "support_chat_invite",
		Layout:         "modal",
		TimeoutSeconds: timeout,
		Metadata: map[string]any{
			"sessionId":   invite.SessionID,
			"ticketId":    invite.TicketID,
			"ticketTitle": invite.TicketTitle,
			"supportChat": true,
		},
	})

	body, _ := json.Marshal(response)

	switch response.Result {
	case "approved":
		// Só abre a janela com CONSENTIMENTO EXPLÍCITO do usuário.
		//
		// `AgentAction == "user_decision"` é o único caminho em que o modal
		// devolveu um clique. O rollout do servidor pode rebaixar
		// require_confirmation para notify_only (EnableRequireConfirmation=false)
		// e nesse caso o Dispatch retorna approved automaticamente — abrir a
		// janela aí violaria o requisito de confirmação na máquina.
		if response.AgentAction != "user_decision" {
			a.respondSupportChatInvite(invite.SessionID, false, "confirmation_disabled")
			a.clearSupportChatIfCurrent(invite.SessionID)
			return true, 1, string(body), "confirmacao obrigatoria desabilitada por politica do servidor"
		}

		a.respondSupportChatInvite(invite.SessionID, true, "")
		a.openSupportChatWindow()
		return true, 0, string(body), ""

	case "denied":
		a.respondSupportChatInvite(invite.SessionID, false, "user_denied")
		a.clearSupportChatIfCurrent(invite.SessionID)
		return true, 10, string(body), "usuario recusou o chat de suporte"

	case "timeout_policy_applied":
		a.respondSupportChatInvite(invite.SessionID, false, "timeout")
		a.clearSupportChatIfCurrent(invite.SessionID)
		return true, 124, string(body), "timeout de confirmacao do chat de suporte"

	default:
		// Distingue política de indisponibilidade: sem isso o console mostraria
		// "agent offline" quando o que houve foi bloqueio de notificação.
		reason := "offline"
		if response.AgentAction == "disabled_by_rollout" {
			reason = "notifications_disabled"
		}

		a.respondSupportChatInvite(invite.SessionID, false, reason)
		a.clearSupportChatIfCurrent(invite.SessionID)
		return true, 1, string(body), strings.TrimSpace(response.Message)
	}
}

// respondSupportChatInvite reporta a decisão ao servidor (best-effort).
//
// O aceite usa o caminho da sessão corrente (transita o estado para Active). A
// recusa NUNCA depende nem altera o estado corrente: quando o motivo é "busy"
// existe outra conversa ativa na máquina e reescrever o estado deixaria o
// usuário sem conseguir enviar mensagens.
func (a *App) respondSupportChatInvite(sessionID string, accepted bool, reason string) {
	svc := a.supportChatService()

	ctx, cancel := context.WithTimeout(a.supportChatContext(), 20*time.Second)
	defer cancel()

	var err error
	if accepted {
		err = svc.RespondInvite(ctx, true, reason)
	} else {
		err = svc.RespondInviteForSession(ctx, sessionID, false, reason)
	}

	if err != nil {
		a.Logs.Append("[supportchat] falha ao reportar a resposta do convite: " + err.Error())
	}
}

// notifySupportChatInvite dispara o toast nativo (best-effort).
func (a *App) notifySupportChatInvite(invite supportchat.Invite) {
	a.supportChatMu.Lock()
	ns := a.nativeNotifications
	a.supportChatMu.Unlock()

	if ns == nil {
		return
	}

	opts := wailsnotifications.NotificationOptions{
		ID:    supportChatInviteNotificationID,
		Title: "Suporte iniciou um chat",
		Body:  buildSupportChatInviteMessage(invite),
		Data:  map[string]interface{}{"kind": "supportchat.invite", "sessionId": invite.SessionID},
	}

	if err := ns.SendNotification(opts); err != nil {
		a.Logs.Append("[supportchat] toast nativo indisponível: " + err.Error())
	}
}

// clearSupportChatIfCurrent só limpa o estado quando ele pertence à sessão
// informada — nunca descarta a conversa ATIVA de outro chamado.
func (a *App) clearSupportChatIfCurrent(sessionID string) {
	svc := a.supportChatService()
	state := svc.State()

	if state.SessionID == "" || state.SessionID == sessionID {
		svc.ClearState()
	}
}

// resolveSupportChatInviteTimeoutSeconds converte `expiresAtUtc` (RFC3339) no
// timeout do modal de confirmação, limitado a uma janela defensiva.
func resolveSupportChatInviteTimeoutSeconds(expiresAt string) int {
	const (
		fallback = 120
		minimal  = 30
		maximum  = 600
	)

	expiresAt = strings.TrimSpace(expiresAt)
	if expiresAt == "" {
		return fallback
	}

	deadline, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return fallback
	}

	seconds := int(time.Until(deadline).Seconds())
	if seconds < minimal {
		return minimal
	}
	if seconds > maximum {
		return maximum
	}

	return seconds
}

func buildSupportChatInviteMessage(invite supportchat.Invite) string {
	support := strings.TrimSpace(invite.SupportName)
	if support == "" {
		support = "O suporte"
	}

	if title := strings.TrimSpace(invite.TicketTitle); title != "" {
		return fmt.Sprintf("%s abriu um chat no chamado '%s'. Deseja atender agora?", support, title)
	}

	return fmt.Sprintf("%s abriu um chat de suporte. Deseja atender agora?", support)
}

// ── Helpers de dispatch ─────────────────────────────────────────────────────

func isTicketChatInviteCommandType(cmdType string) bool {
	return agentcommands.IsTicketChatInviteCommandType(cmdType)
}

func parseTicketChatInvitePayload(payload any) (supportchat.Invite, error) {
	return agentcommands.ParseTicketChatInvitePayload(payload)
}
