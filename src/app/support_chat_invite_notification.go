package app

import (
	"strings"
	"time"

	"discovery/app/supportchat"
)

// ── Notificação nativa do convite de chat do suporte ────────────────────────
//
// O convite deixou de abrir a janela inteira do chat antes do aceite: agora ele
// aparece como um CARTÃO NATIVO no canto inferior direito, no mesmo estilo do
// prompt de autorização de acesso remoto (tema do Windows, topmost, sem roubar
// a tarefa do usuário). A janela do chat só é criada depois que o usuário
// escolhe "Abrir chat".
//
// IMPORTANTE (sem ação padrão): diferente do consentimento de acesso remoto,
// este cartão NÃO tem contagem regressiva, NÃO tem botão pré-focado e NÃO
// decide sozinho no fim do tempo. Quem responde é sempre o usuário; se o
// convite expirar no servidor, o verify/reconciliação fecha o cartão.
//
// A decisão do usuário é aplicada aqui, no processo de UI: aceite reporta ao
// servidor e abre a janela; recusa reporta e mantém a janela fechada.

// supportChatInviteNotificationResult é a escolha do usuário no cartão.
type supportChatInviteNotificationResult struct {
	// SessionID amarra a decisão ao convite que o cartão representava: entre o
	// clique e o processamento o convite pode ter sido substituído/encerrado.
	SessionID string
	Accepted  bool
}

// supportChatInviteNotificationAnsweredTTL é por quanto tempo a sessão que o
// usuário já respondeu no cartão fica marcada como respondida.
//
// Necessário porque a resposta ao servidor é uma chamada de rede: durante ela o
// estado local ainda é "waiting", e uma reentrega do evento (IPC/JS/NATS) nesse
// intervalo recriaria o cartão para um convite que o usuário acabou de
// responder. O TTL cobre com folga a latência da resposta e a reentrega.
const supportChatInviteNotificationAnsweredTTL = 2 * time.Minute

// Labels/textos do cartão. Ficam aqui (fora do arquivo Windows) para os testes
// de contrato rodarem em qualquer plataforma.
func supportChatInviteNotificationTitle() string    { return "O suporte quer abrir o chat" }
func supportChatInviteNotificationSubtitle() string { return "Chat de suporte" }
func supportChatInviteNotificationAllow() string    { return "Abrir chat" }
func supportChatInviteNotificationDeny() string     { return "Agora não" }
func supportChatInviteNotificationHint() string {
	return "Esc recusa · sem ação automática"
}

// supportChatInviteNotificationMessage monta a mensagem do cartão a partir do
// convite (atendente e chamado, quando existirem).
func supportChatInviteNotificationMessage(invite supportchat.Invite) string {
	support := strings.TrimSpace(invite.SupportName)
	if support == "" {
		support = "O suporte"
	}

	message := support + " quer abrir o chat com você"
	if title := strings.TrimSpace(invite.TicketTitle); title != "" {
		message += " sobre o chamado \"" + title + "\""
	}
	return message + ". Deseja abrir a conversa agora?"
}

// showSupportChatInviteNotification exibe o cartão nativo do convite.
//
// Devolve false quando a plataforma não tem cartão nativo (não-Windows) ou a
// janela não pôde ser criada — nesse caso o chamador mantém o fluxo antigo
// (janela do chat com o modal de convite), que continua sendo o fallback.
func (a *App) showSupportChatInviteNotification(invite supportchat.Invite) bool {
	if a == nil {
		return false
	}
	sessionID := strings.TrimSpace(invite.SessionID)
	if sessionID == "" {
		return false
	}

	// Serializa a abertura: evento IPC + handler JS + reentrega podem chegar ao
	// mesmo tempo e, sem isto, dois cartões eram criados antes do registro.
	a.supportChatNotifyShowMu.Lock()
	defer a.supportChatNotifyShowMu.Unlock()

	now := time.Now()
	var cancels []func()

	a.supportChatNotifyMu.Lock()
	if a.supportChatNotify == nil {
		a.supportChatNotify = make(map[string]func())
	}
	if a.supportChatNotifyAnswered == nil {
		a.supportChatNotifyAnswered = make(map[string]time.Time)
	}
	for id, until := range a.supportChatNotifyAnswered {
		if now.After(until) {
			delete(a.supportChatNotifyAnswered, id)
		}
	}
	// Reentrega do mesmo convite (evento IPC + JS + redelivery): o cartão já
	// está na tela; não abre outro.
	if _, ok := a.supportChatNotify[sessionID]; ok {
		a.supportChatNotifyMu.Unlock()
		return true
	}
	// O usuário já respondeu este convite e a resposta ainda está indo ao
	// servidor (estado local temporariamente "waiting"): não reabre o cartão.
	if _, ok := a.supportChatNotifyAnswered[sessionID]; ok {
		a.supportChatNotifyMu.Unlock()
		return true
	}
	// Só existe um convite pendente por máquina; um cartão de sessão anterior
	// não pode ficar órfão na tela.
	for id, cancel := range a.supportChatNotify {
		cancels = append(cancels, cancel)
		delete(a.supportChatNotify, id)
	}
	a.supportChatNotifyMu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}

	resultCh, cancel, ok := startSupportChatInviteNotification(a.supportChatContext(), invite)
	if !ok {
		return false
	}

	a.supportChatNotifyMu.Lock()
	a.supportChatNotify[sessionID] = cancel
	a.supportChatNotifyMu.Unlock()

	// Rechecagem pós-criação: entre o começo da abertura e o registro acima o
	// convite pode ter sido respondido/encerrado por outro caminho (ex.: a
	// janela fallback, um evento de estado ou o servidor). Nesse caso o cartão
	// recém-criado não pode ficar órfão na tela.
	state := a.supportChatService().State()
	if state.Status != supportchat.StatusWaiting || state.SessionID != sessionID ||
		a.supportChatInviteNotificationWasAnswered(sessionID) {
		a.closeSupportChatInviteNotification(sessionID)
		return true
	}

	go a.runSupportChatInviteNotificationDecision(sessionID, resultCh)
	return true
}

// supportChatInviteNotificationWasAnswered informa se o usuário já respondeu
// este convite no cartão (resposta ainda em voo ou recém-reportada).
func (a *App) supportChatInviteNotificationWasAnswered(sessionID string) bool {
	if a == nil || strings.TrimSpace(sessionID) == "" {
		return false
	}
	sessionID = strings.TrimSpace(sessionID)

	now := time.Now()
	a.supportChatNotifyMu.Lock()
	defer a.supportChatNotifyMu.Unlock()
	for id, until := range a.supportChatNotifyAnswered {
		if now.After(until) {
			delete(a.supportChatNotifyAnswered, id)
		}
	}
	_, ok := a.supportChatNotifyAnswered[sessionID]
	return ok
}

// markSupportChatInviteNotificationAnswered registra que o usuário respondeu o
// convite no cartão. Ver supportChatInviteNotificationAnsweredTTL.
func (a *App) markSupportChatInviteNotificationAnswered(sessionID string) {
	if a == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	a.supportChatNotifyMu.Lock()
	if a.supportChatNotifyAnswered == nil {
		a.supportChatNotifyAnswered = make(map[string]time.Time)
	}
	a.supportChatNotifyAnswered[sessionID] = time.Now().Add(supportChatInviteNotificationAnsweredTTL)
	a.supportChatNotifyMu.Unlock()
}

// forgetSupportChatInviteNotification remove o registro sem cancelar o cartão
// (usado quando o próprio cartão já devolveu a decisão).
func (a *App) forgetSupportChatInviteNotification(sessionID string) {
	a.supportChatNotifyMu.Lock()
	delete(a.supportChatNotify, sessionID)
	a.supportChatNotifyMu.Unlock()
}

// supportChatInviteNotificationPending informa se há cartão aberto da sessão.
func (a *App) supportChatInviteNotificationPending(sessionID string) bool {
	if a == nil || strings.TrimSpace(sessionID) == "" {
		return false
	}
	a.supportChatNotifyMu.Lock()
	defer a.supportChatNotifyMu.Unlock()
	_, ok := a.supportChatNotify[strings.TrimSpace(sessionID)]
	return ok
}

// closeSupportChatInviteNotification fecha o cartão do convite informado, se
// existir. Usado quando o convite é respondido por outro caminho (janela
// fallback), substituído, expirado ou o chat termina.
func (a *App) closeSupportChatInviteNotification(sessionID string) {
	if a == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}

	a.supportChatNotifyMu.Lock()
	cancel := a.supportChatNotify[sessionID]
	delete(a.supportChatNotify, sessionID)
	a.supportChatNotifyMu.Unlock()

	if cancel != nil {
		cancel()
	}
}

// closeAllSupportChatInviteNotifications fecha qualquer cartão de convite
// pendente (troca de sessão, fim do chat, desligamento do agent).
func (a *App) closeAllSupportChatInviteNotifications() {
	if a == nil {
		return
	}

	a.supportChatNotifyMu.Lock()
	cancels := make([]func(), 0, len(a.supportChatNotify))
	for sessionID, cancel := range a.supportChatNotify {
		cancels = append(cancels, cancel)
		delete(a.supportChatNotify, sessionID)
	}
	a.supportChatNotifyMu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
}

// runSupportChatInviteNotificationDecision aguarda a escolha do usuário no
// cartão e aplica a resposta (aceite abre a janela do chat; recusa só reporta).
func (a *App) runSupportChatInviteNotificationDecision(sessionID string, resultCh <-chan supportChatInviteNotificationResult) {
	var (
		result supportChatInviteNotificationResult
		ok     bool
	)

	select {
	case <-a.supportChatContext().Done():
		a.forgetSupportChatInviteNotification(sessionID)
		a.closeSupportChatInviteNotification(sessionID)
		return
	case result, ok = <-resultCh:
	}

	a.forgetSupportChatInviteNotification(sessionID)
	if !ok {
		// Cartão fechado sem decisão (cancelado por outro caminho): nada a
		// reportar ao servidor.
		return
	}

	// Marca ANTES de falar com o servidor: a chamada de rede deixa o estado
	// local em "waiting" e uma reentrega nesse intervalo não pode reabrir o
	// cartão de um convite que o usuário já respondeu.
	a.markSupportChatInviteNotificationAnswered(sessionID)

	// A resposta é aplicada à sessão CORRENTE pelo serviço. Se o convite do
	// cartão não é mais o pendente (substituído/encerrado enquanto o cartão
	// estava na tela), descarta o clique em vez de aceitar a sessão errada.
	if result.SessionID != "" && result.SessionID != sessionID {
		a.Logs.Append("[supportchat] decisao do cartao descartada: sessao do cartao nao confere (cartao=" +
			result.SessionID + " alvo=" + sessionID + ")")
		return
	}
	if state := a.supportChatService().State(); state.Status != supportchat.StatusWaiting || state.SessionID != sessionID {
		a.Logs.Append("[supportchat] decisao do cartao descartada: convite nao esta mais pendente (sessao " + sessionID + ")")
		return
	}

	if result.Accepted {
		a.Logs.Append("[supportchat] convite aceito no cartao nativo (sessao " + sessionID + ")")
		if err := a.SupportChatRespondInvite(true, ""); err != nil {
			a.Logs.Append("[supportchat] falha ao reportar o aceite do cartao nativo: " + err.Error())
		}
		// Abre mesmo se o reporte falhou: a janela mostra o estado/erro e o
		// usuário não perde o contexto do convite que acabou de aceitar.
		if a.app != nil {
			a.openSupportChatWindow()
		}
		return
	}

	a.Logs.Append("[supportchat] convite recusado no cartao nativo (sessao " + sessionID + ")")
	if err := a.SupportChatRespondInvite(false, "user_denied"); err != nil {
		a.Logs.Append("[supportchat] falha ao reportar a recusa do cartao nativo: " + err.Error())
	}
}

// presentSupportChatInvite exibe o cartão nativo e cai para a janela do chat
// quando o cartão não está disponível. Centraliza o caminho usado pelo evento
// IPC, pelo handler standalone e pela retomada no startup.
func (a *App) presentSupportChatInvite(invite supportchat.Invite) {
	if strings.TrimSpace(invite.SessionID) == "" {
		return
	}
	// No processo de SERVIÇO (sem Wails) não existe janela nativa para o
	// usuário: o cartão nasceria invisível no desktop da sessão 0. A superfície
	// correta é a UI companion, que recebe o evento via IPC.
	if a.app == nil {
		if a.RuntimeFlags.ServiceMode {
			a.EmitEvent("supportchat:invite",
				"sessionId", invite.SessionID,
				"ticketId", invite.TicketID,
				"ticketTitle", invite.TicketTitle,
				"supportName", invite.SupportName)
		}
		return
	}
	if a.showSupportChatInviteNotification(invite) {
		a.startSupportChatInviteVerification(invite.SessionID)
		return
	}
	if err := a.supportChatContext().Err(); err != nil {
		// Encerrando: não abre uma janela nova durante o shutdown.
		return
	}
	// Fallback (não-Windows ou cartão indisponível): fluxo antigo, com toast,
	// janela do chat e modal de convite.
	a.notifySupportChatInvite(invite)
	a.openSupportChatWindow()
	a.startSupportChatInviteVerification(invite.SessionID)
}
