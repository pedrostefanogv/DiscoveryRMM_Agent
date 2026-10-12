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

	// Guarda contra convite ATRASADO: o servidor reentrega comandos não
	// confirmados depois de alguns minutos (Nats:CommandRedelivery), então este
	// comando pode chegar bem depois do vencimento — inclusive de uma sessão que
	// já foi aceita e encerrada. Sem esta checagem o agente abria a janela do
	// chat "do nada", vazia, no meio do trabalho do usuário.
	//
	// NÃO respondemos "recusado" ao servidor aqui: a decisão usa o relógio da
	// máquina (que pode estar adiantado) e responder reescreveria o estado de
	// uma sessão que o servidor ainda considera pendente. O filtro autoritativo
	// é o estado no servidor, checado em SupportChatOpenInviteWindow.
	if invite.ExpiredAt(time.Now()) {
		a.Logs.Append("[supportchat] convite ignorado: expirado em " + invite.ExpiresAt + " (sessionId=" + invite.SessionID + ")")
		return true, 1, "", "convite de chat expirado"
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

	// A confirmação acontece DENTRO da janela do chat (não mais num modal da
	// interface principal). Quem abre a janela é o processo de UI: no modo
	// serviço não existe Wails, então o convite é reemitido pelo IPC.
	//
	// BUG CORRIGIDO: antes o handler mostrava um modal na janela principal e, no
	// aceite, chamava openSupportChatWindow() DENTRO DO SERVIÇO — onde a.app é
	// nil. A janela do chat só acabava abrindo pela retomada no startup da UI,
	// que é exatamente o sintoma "só abre quando fecho e reabro o agent".
	a.notifySupportChatInvite(invite)
	a.EmitEvent("supportchat:invite",
		"sessionId", invite.SessionID,
		"ticketId", invite.TicketID,
		"ticketTitle", invite.TicketTitle,
		"supportName", invite.SupportName)

	if !a.hasSupportChatUI() {
		// Sem interface não há como o usuário confirmar: recusa explícita para o
		// suporte não ficar esperando o convite expirar.
		a.respondSupportChatInvite(invite.SessionID, false, "no_ui")
		a.clearSupportChatIfCurrent(invite.SessionID)
		return true, 1, "", "agent sem interface de usuario para confirmar o chat"
	}

	body, _ := json.Marshal(map[string]any{
		"pendingConfirmation": true,
		"sessionId":           invite.SessionID,
		"ui":                  "chat-window",
	})

	return true, 0, string(body), ""
}

// hasSupportChatUI informa se existe uma interface capaz de renderizar a janela
// do chat e coletar a confirmação do usuário.
//
//   - processo de UI (Wails): a.app != nil;
//   - modo serviço: pelo menos uma UI companion conectada no IPC.
func (a *App) hasSupportChatUI() bool {
	if a.app != nil {
		return true
	}
	return a.ipcServer != nil && a.ipcServer.ClientCount() > 0
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
