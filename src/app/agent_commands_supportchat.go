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
//
// O convite pode ser de conversa VINCULADA A CHAMADO ou AVULSA (sem chamado,
// aberta pela lista/detalhe do agent). Só o sessionId é obrigatório; o ticketId
// vem vazio na conversa avulsa.

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
	if invite.SessionID == "" {
		return true, 2, "", "payload de convite de chat sem sessionId"
	}

	// Guarda contra convite ATRASADO: o servidor reentrega comandos não
	// confirmados depois de alguns minutos (Nats:CommandRedelivery), então este
	// comando pode chegar bem depois do vencimento — inclusive de uma sessão que
	// já foi aceita e encerrada. Sem esta checagem o agente abria a janela do
	// chat "do nada", vazia, no meio do trabalho do usuário.
	//
	// NÃO respondemos "recusado" ao servidor aqui: a decisão usa o relógio da
	// máquina (que pode estar adiantado) e responder reescreveria o estado de
	// uma sessão que o servidor ainda considera pendente. O estado no servidor é
	// conferido em BACKGROUND por verifySupportChatInviteWindow (a janela abre
	// imediatamente para não depender da latência/consistência do REST).
	if invite.ExpiredAt(time.Now()) {
		a.Logs.Append("[supportchat] convite ignorado: expirado em " + invite.ExpiresAt + " (sessionId=" + invite.SessionID + ")")
		return true, 1, "", "convite de chat expirado"
	}

	// Reentrega do convite DEPOIS de o usuário já ter respondido no cartão: a
	// resposta ao servidor é uma chamada de rede e, nesse intervalo, o estado
	// local ainda é "waiting". Repreparar aqui reemitiria o convite (e poderia
	// limpar o transcript) para uma sessão que o usuário já respondeu.
	if a.supportChatInviteNotificationWasAnswered(invite.SessionID) {
		a.Logs.Append("[supportchat] convite reentregue apos resposta no cartao; ignorado (sessionId=" + invite.SessionID + ")")
		body, _ := json.Marshal(map[string]any{
			"alreadyAnswered": true,
			"sessionId":       invite.SessionID,
		})
		return true, 0, string(body), ""
	}

	svc := a.supportChatService()

	// Decisão ATÔMICA no serviço (ver PrepareInviteIfIdle): o antigo
	// "checa Estado() e depois PrepareInvite" era um check-then-act — dois
	// convites quase simultâneos passavam os dois pela checagem e o segundo
	// sobrescrevia o primeiro, deixando o servidor esperando resposta de um
	// convite que o usuário nunca viu.
	decision := svc.PrepareInviteIfIdle(invite)

	// Convite ANTERIOR que aguardava resposta (mesma máquina, outra sessão) foi
	// substituído: recusa no servidor para ele não ficar "aguardando" até expirar.
	if decision.SupersededSessionID != "" {
		a.Logs.Append("[supportchat] convite substituido por outro mais recente: " + decision.SupersededSessionID)
		// O cartão nativo do convite anterior não pode ficar órfão na tela. A
		// marca também impede que uma reentrega tardia desse convite substitua o
		// convite novo que o usuário está vendo.
		a.closeSupportChatInviteNotification(decision.SupersededSessionID)
		a.markSupportChatInviteNotificationAnswered(decision.SupersededSessionID)
		go a.respondSupportChatInvite(decision.SupersededSessionID, false, "replaced")
	}

	// Só segue para o convite quando ele foi REALMENTE registrado — decisão
	// explícita, sem depender de "o motivo veio vazio".
	if !decision.Accepted {
		switch decision.Reason {
		case supportchat.InviteReasonAlreadyActive:
			// Reentrega do convite de uma conversa JÁ ACEITA: nada de reabrir o
			// pedido de confirmação (o modal aparecia em cima da conversa em curso).
			// Só garante que a janela está aberta e na frente.
			if a.app != nil {
				go a.openSupportChatWindow()
			}
			body, _ := json.Marshal(map[string]any{
				"alreadyActive": true,
				"sessionId":     invite.SessionID,
			})
			return true, 0, string(body), ""

		case supportchat.InviteReasonBusy:
			// Uma sessão por máquina (o console mostra "máquina ocupada em outro chat").
			a.respondSupportChatInvite(invite.SessionID, false, supportchat.InviteReasonBusy)
			return true, 1, "", "maquina ocupada em outro chat de suporte"

		case supportchat.InviteReasonAlreadyFinished:
			// Nada a abrir: a conversa já terminou. Abrir a janela aqui mostraria
			// uma conversa encerrada (ou vazia) "do nada".
			body, _ := json.Marshal(map[string]any{
				"alreadyFinished": true,
				"sessionId":       invite.SessionID,
			})
			return true, 0, string(body), ""

		default:
			return true, 2, "", "convite de chat invalido"
		}
	}

	// A confirmação acontece DENTRO da janela do chat (não mais num modal da
	// interface principal). Quem abre a janela é o processo de UI: no modo
	// serviço não existe Wails, então o convite é reemitido pelo IPC.
	//
	// BUG CORRIGIDO: antes o handler mostrava um modal na janela principal e, no
	// aceite, chamava openSupportChatWindow() DENTRO DO SERVIÇO — onde a.app é
	// nil. A janela do chat só acabava abrindo pela retomada no startup da UI,
	// que é exatamente o sintoma "só abre quando fecho e reabro o agent".
	// O alerta visual agora é o cartão nativo (ou o toast, no fallback de
	// presentSupportChatInvite): não dispara um segundo toast em paralelo.
	a.EmitEvent("supportchat:invite",
		"sessionId", invite.SessionID,
		"ticketId", invite.TicketID,
		"ticketTitle", invite.TicketTitle,
		"supportName", invite.SupportName)

	if !a.hasSupportChatUI() {
		// Sem interface NÃO existe onde mostrar o convite — mas a UI pode estar
		// subindo agora (o watchdog acabou de ser acionado). Antes o convite era
		// recusado na hora, então uma UI que aparecesse 2 s depois encontrava o
		// convite já morto: o suporte via "usuário recusou" e o usuário nunca
		// soube que houve convite.
		//
		// Agora há uma janela de tolerância: se a UI aparecer dentro dela, o
		// convite é reemitido e a janela abre flutuando (ver openSupportChatWindow).
		// Só depois do prazo o agent recusa explicitamente, para o suporte não
		// ficar esperando o convite expirar.
		a.requestUIWatchdogSpawn("chat_invite")
		a.scheduleSupportChatInviteRedelivery(invite)

		body, _ := json.Marshal(map[string]any{
			"pendingConfirmation": true,
			"sessionId":           invite.SessionID,
			"ui":                  "chat-window",
			"awaitingUi":          true,
		})
		return true, 0, string(body), ""
	}

	// Processo ÚNICO (UI standalone, sem serviço): a superfície do convite pode
	// ser aberta AQUI, sem depender do frontend. O evento para a janela principal
	// é best-effort e, no cenário que mais dói — agent recém-iniciado no tray, com
	// a janela principal nunca criada (criação preguiçosa) —, não existe webview
	// carregado para receber o evento: o convite ficava engolido até a
	// reconciliação de 60s. Apresentar no Go elimina essa dependência.
	if a.app != nil {
		// Cartão nativo (sem ação padrão) ou, se indisponível, a janela antiga.
		// presentSupportChatInvite também inicia a validação em background.
		go a.presentSupportChatInvite(invite)
	}

	body, _ := json.Marshal(map[string]any{
		"pendingConfirmation": true,
		"sessionId":           invite.SessionID,
		"ui":                  "chat-window",
	})

	return true, 0, string(body), ""
}

// Constantes da janela de tolerância do convite sem UI.
const (
	// supportChatInviteUIWait é quanto tempo o agent espera a UI subir antes de
	// recusar o convite. Cobre a subida do processo de UI + WebView2 depois do
	// requestUIWatchdogSpawn (tipicamente 2–8 s em máquina fria).
	supportChatInviteUIWait = 25 * time.Second
	// supportChatInviteUIPoll é o intervalo de verificação da UI.
	supportChatInviteUIPoll = 500 * time.Millisecond
)

// scheduleSupportChatInviteRedelivery espera a UI aparecer e reemite o convite
// para ela; se a UI não aparecer dentro de supportChatInviteUIWait, recusa o
// convite (motivo "no_ui") e limpa o estado local.
//
// Roda em goroutine: o handler do comando precisa responder ao servidor na hora
// (o resultado sai como "pendingConfirmation").
func (a *App) scheduleSupportChatInviteRedelivery(invite supportchat.Invite) {
	if a == nil || strings.TrimSpace(invite.SessionID) == "" {
		return
	}

	go func() {
		deadline := time.NewTimer(supportChatInviteUIWait)
		defer deadline.Stop()

		ticker := time.NewTicker(supportChatInviteUIPoll)
		defer ticker.Stop()

		for {
			select {
			case <-a.supportChatContext().Done():
				return

			case <-deadline.C:
				// ÚLTIMA conferência: a UI pode ter subido entre o último poll
				// (500 ms) e o prazo. Recusar aqui mataria um convite que já está
				// chegando na tela — exatamente o sintoma que esta janela de
				// tolerância existe para eliminar.
				if a.hasSupportChatUI() {
					a.redeliverSupportChatInvite(invite)
					return
				}

				// Só recusa se o convite AINDA é o pendente: se outra sessão o
				// substituiu (ou ele já foi respondido), não mexe no estado.
				if state := a.supportChatService().State(); state.Status != supportchat.StatusWaiting ||
					state.SessionID != invite.SessionID {
					a.Logs.Append("[supportchat] prazo de espera da UI venceu, mas o convite nao esta mais pendente; nada a recusar (sessionId=" + invite.SessionID + ")")
					return
				}

				a.Logs.Append("[supportchat] convite sem UI em " + supportChatInviteUIWait.String() +
					"; recusando para o suporte nao ficar esperando (sessionId=" + invite.SessionID + ")")
				a.respondSupportChatInvite(invite.SessionID, false, "no_ui")
				a.clearSupportChatIfCurrent(invite.SessionID)
				return

			case <-ticker.C:
				if !a.hasSupportChatUI() {
					continue
				}

				a.redeliverSupportChatInvite(invite)
				return
			}
		}
	}()
}

// redeliverSupportChatInvite reemite o convite para a UI que passou a existir:
// evento supportchat:invite (a UI abre a janela pelo GO, sem depender do JS da
// janela principal — ver openSupportChatInviteFromIPC) + toast nativo.
//
// O estado "waiting" já foi registrado pelo PrepareInviteIfIdle no handler do
// comando; aqui só falta a SUPERFÍCIE onde o usuário dá o aceite.
func (a *App) redeliverSupportChatInvite(invite supportchat.Invite) {
	a.Logs.Append("[supportchat] UI disponivel; reemitindo o convite para a tela (sessionId=" + invite.SessionID + ")")
	// O alerta visual agora é o cartão nativo (ou o toast, no fallback de
	// presentSupportChatInvite): não dispara um segundo toast em paralelo.
	a.EmitEvent("supportchat:invite",
		"sessionId", invite.SessionID,
		"ticketId", invite.TicketID,
		"ticketTitle", invite.TicketTitle,
		"supportName", invite.SupportName)
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
	// UIAuthenticatedCount (não ClientCount): conexão de probe/não autenticada
	// não é UI — contá-la impedia o watchdog de relançar a UI de verdade.
	return a.ipcServer != nil && a.ipcServer.UIAuthenticatedCount() > 0
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
