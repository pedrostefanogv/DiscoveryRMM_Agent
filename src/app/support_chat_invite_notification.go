package app

import (
	"context"
	"strings"
	"sync"
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
// A decisão do usuário é aplicada aqui, no processo de UI: a escolha vai ao
// SERVIDOR pelo próprio cartão (reportSupportChatInviteDecision) e só depois o
// cartão sai da tela — aceite abre a janela do chat; recusa mantém fechada.

// supportChatInviteNotificationResult é a escolha do usuário no cartão.
type supportChatInviteNotificationResult struct {
	// SessionID amarra a decisão ao convite que o cartão representava: entre o
	// clique e o processamento o convite pode ter sido substituído/encerrado.
	SessionID string
	Accepted  bool
	// Error é o resultado do envio da decisão ao SERVIDOR (nil = entregue).
	//
	// O cartão reporta a escolha antes de sair da tela, então ele já sabe se o
	// servidor aceitou a recusa; o consumidor da decisão usa isto para logar.
	Error error
}

// supportChatInviteNotificationAnsweredTTL é por quanto tempo a sessão que o
// usuário já respondeu no cartão fica marcada como respondida.
//
// Necessário porque a resposta ao servidor é uma chamada de rede: durante ela o
// estado local ainda é "waiting", e uma reentrega do evento (IPC/JS/NATS) nesse
// intervalo recriaria o cartão para um convite que o usuário acabou de
// responder. O TTL cobre com folga a latência da resposta e a reentrega.
const supportChatInviteNotificationAnsweredTTL = 2 * time.Minute

// supportChatInviteReportWait limita a espera do cartão pela resposta do
// servidor. O cartão fica na tela com "avisando o suporte…" até este prazo; é
// curto de propósito porque a janela do chat/da recusa só fecha depois dele.
const supportChatInviteReportWait = 8 * time.Second

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
//
// intentional indica REENTREGA INTENCIONAL do mesmo convite (novo comando do
// servidor, tipicamente o botão "Reenviar convite" do console): o cartão da
// tela é substituído por um novo em vez de ser dado como já apresentado.
func (a *App) showSupportChatInviteNotification(invite supportchat.Invite, intentional bool) bool {
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
	//
	// EXCEÇÃO (reentrega INTENCIONAL): quando o atendente usa "Reenviar convite"
	// o comando chega com outro CommandId e espera nova pergunta na máquina. Se
	// o cartão anterior não está mais na tela — usuário fechou no X, Esc, ou a
	// resposta se perdeu — reapresenta em vez de confirmar ao servidor um pedido
	// que o usuário não está vendo.
	existingCancel, cardOnScreen := a.supportChatNotify[sessionID]
	if cardOnScreen && !intentional {
		a.supportChatNotifyMu.Unlock()
		return true
	}
	// O usuário já respondeu este convite e a resposta ainda está indo ao
	// servidor (estado local temporariamente "waiting"): não reabre o cartão.
	if _, answered := a.supportChatNotifyAnswered[sessionID]; answered && !intentional {
		a.supportChatNotifyMu.Unlock()
		return true
	}
	if cardOnScreen {
		// Reenvio pedido pelo suporte: fecha o cartão atual e abre outro (abaixo)
		// com os dados novos do convite.
		delete(a.supportChatNotify, sessionID)
		a.Logs.Append("[supportchat] reenvio do convite solicitado pelo suporte; reapresentando o cartao (sessao " + sessionID + ")")
	}
	// Só existe um convite pendente por máquina; um cartão de sessão anterior
	// não pode ficar órfão na tela.
	for id, cancel := range a.supportChatNotify {
		cancels = append(cancels, cancel)
		delete(a.supportChatNotify, id)
	}
	a.supportChatNotifyMu.Unlock()

	if existingCancel != nil && cardOnScreen {
		cancels = append(cancels, existingCancel)
	}
	for _, cancel := range cancels {
		cancel()
	}

	resultCh, cancel, ok := startSupportChatInviteNotification(a.supportChatContext(), invite, a.reportSupportChatInviteDecision)
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
		(!intentional && a.supportChatInviteNotificationWasAnswered(sessionID)) {
		a.closeSupportChatInviteNotification(sessionID)
		return true
	}

	// Reenvio intencional do suporte: a nova pergunta precisa de fato aparecer
	// (o cartão nasce topmost e passa na frente) e a resposta anterior deixa de
	// valer — senão um clique nesta pergunta seria descartado como "já
	// respondido".
	if intentional {
		a.clearSupportChatInviteNotificationAnswered(sessionID)
	}

	go a.runSupportChatInviteNotificationDecision(invite, cancel, resultCh)
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

// clearSupportChatInviteNotificationAnswered libera a marca de "já respondido"
// da sessão (usada quando o suporte REENVIA o convite: a nova pergunta precisa
// de uma nova resposta).
func (a *App) clearSupportChatInviteNotificationAnswered(sessionID string) {
	if a == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	a.supportChatNotifyMu.Lock()
	delete(a.supportChatNotifyAnswered, strings.TrimSpace(sessionID))
	a.supportChatNotifyMu.Unlock()
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
func (a *App) runSupportChatInviteNotificationDecision(invite supportchat.Invite, cancel func(), resultCh <-chan supportChatInviteNotificationResult) {
	sessionID := strings.TrimSpace(invite.SessionID)
	var (
		result supportChatInviteNotificationResult
		ok     bool
	)

	// Rede de segurança do cartão: se a janela nativa falhar em fechar (thread
	// ocupada, hwnd inválido), o cartão não pode ficar na tela depois de
	// respondido. O canceller fecha a janela nativa por dentro (WM_CLOSE); o
	// cartão já respondido não perde a decisão (abort é no-op com done=true).
	var (
		guardOnce sync.Once
		guardDone = make(chan struct{})
	)
	go func() {
		timer := time.NewTimer(supportChatInviteCardCloseGuard)
		defer timer.Stop()
		select {
		case <-guardDone:
			return
		case <-a.supportChatContext().Done():
			return
		case <-timer.C:
		}
		select {
		case <-guardDone:
			return
		default:
		}
		guardOnce.Do(func() {
			a.Logs.Append("[supportchat] cartao do convite ainda na tela apos o prazo; fechando pela rede de seguranca (sessao " + sessionID + ")")
			if cancel != nil {
				cancel()
			}
			a.closeSupportChatInviteNotification(sessionID)
		})
	}()
	defer guardOnce.Do(func() { close(guardDone) })

	select {
	case <-a.supportChatContext().Done():
		a.forgetSupportChatInviteNotification(sessionID)
		a.closeSupportChatInviteNotification(sessionID)
		return
	case result, ok = <-resultCh:
	}

	a.forgetSupportChatInviteNotification(sessionID)
	if !ok {
		// Cartão fechado SEM decisão (cancelado por outro caminho — convite
		// substituído, expirado no servidor, chat encerrado): nada a reportar.
		//
		// O estado local NÃO é limpo aqui: esse caminho também roda quando o
		// usuário ACABOU de recusar (o serviço marca o estado e o cartão é
		// cancelado na sequência) — limpar apagaria o "ended" logo depois do
		// POST e deixaria uma reentrega tardia do comando reabrir o cartão.
		return
	}

	// Marca ANTES de falar com o servidor: a chamada de rede deixa o estado
	// local em "waiting" e uma reentrega nesse intervalo não pode reabrir o
	// cartão de um convite que o usuário já respondeu. Também impede que a
	// reentrada da UI (ver replaySupportChatInviteAfterUIReconnect) rearme o
	// pedido depois de o usuário ter recusado.
	a.markSupportChatInviteNotificationAnswered(sessionID)

	// O ENVIO ao servidor acontece DENTRO do cartão (reportSupportChatInviteDecision),
	// antes de a janela nativa sair da tela — ver o cartão em
	// support_chat_invite_notification_windows.go. Em modo companion o cartão é
	// criado na UI e o serviço só publica por IPC: a decisão do cartão é a
	// AUTORIDADE.
	if result.Error != nil {
		a.Logs.Append("[supportchat] falha ao reportar a resposta do cartao nativo: " + result.Error.Error())
	}

	// A resposta é aplicada à sessão CORRENTE pelo serviço. Se o convite do
	// cartão não é mais o pendente (substituído/encerrado enquanto o cartão
	// estava na tela), descarta o clique em vez de aceitar a sessão errada.
	if result.SessionID != "" && result.SessionID != sessionID {
		a.Logs.Append("[supportchat] decisao do cartao descartada: sessao do cartao nao confere (cartao=" +
			result.SessionID + " alvo=" + sessionID + ")")
		return
	}
	state := a.supportChatService().State()
	if state.Status == supportchat.StatusActive && state.SessionID == sessionID {
		// ACEITE CONCLUÍDO: o POST já foi feito pelo cartão (reportSupportChatInviteDecision),
		// que também já pediu a abertura da janela. Aqui só garantimos a janela —
		// sem descartar a decisão. Antes este caminho tratava o convite já ativo
		// como "decisão descartada" e o cartão ficava na tela sem a janela abrir.
		if result.Accepted && a.app != nil && !a.supportChatWindowOpen() {
			a.Logs.Append("[supportchat] aceite confirmado apos o cartao; garantindo a janela do chat (sessao " + sessionID + ")")
			a.hideSupportChatInviteSurface()
			a.openSupportChatWindow()
		}
		return
	}
	if state.Status != supportchat.StatusWaiting || state.SessionID != sessionID {
		a.Logs.Append("[supportchat] decisao do cartao descartada: convite nao esta mais pendente (sessao " + sessionID + ")")
		return
	}

	if result.Accepted {
		a.Logs.Append("[supportchat] convite aceito no cartao nativo (sessao " + sessionID + ")")
		// Fluxo FALLBACK (janela do chat com o modal de aceite): a janela que
		// apresentava o convite sai de cena ANTES de abrir a nova. Sem isto o
		// usuário ficava com DUAS janelas iguais na tela (a antiga, já respondida,
		// e a conversa), e a reconciliação podia reabrir a antiga.
		a.hideSupportChatInviteSurface()
		// Estado local -> active e snapshot da conversa. Roda em goroutine: a
		// janela do chat precisa abrir JÁ (a abertura é independente do REST) e
		// o carregamento das mensagens não pode atrasar a janela.
		go func() {
			if err := a.SupportChatRespondInvite(true, ""); err != nil {
				a.Logs.Append("[supportchat] falha ao concluir o aceite no cartao nativo: " + err.Error())
			}
		}()
		// Abre mesmo se o reporte falhou: a janela mostra o estado/erro e o
		// usuário não perde o contexto do convite que acabou de aceitar.
		if a.app != nil {
			a.openSupportChatWindow()
		}
		return
	}

	a.Logs.Append("[supportchat] convite recusado no cartao nativo (sessao " + sessionID + ")")
	// Recusa: descarta o estado e garante que nenhuma janela do chat fique
	// aberta (caminho fallback). O servidor já foi avisado pelo cartão.
	a.supportChatService().ClearState()
	a.closeSupportChatWindow()
}

// reportSupportChatInviteDecision leva a escolha do cartão ao SERVIDOR.
//
// Chamado pelo cartão nativo EM PARALELO ao fechamento (a tela não espera
// rede): o cartão some ao clique e o POST segue em goroutine. O motivo
// reportado é sempre o do USUÁRIO (user_denied): a recusa automática por "sem
// interface" é outro caminho (scheduleSupportChatInviteRedelivery).
func (a *App) reportSupportChatInviteDecision(result supportChatInviteNotificationResult) error {
	if a == nil || strings.TrimSpace(result.SessionID) == "" {
		return nil
	}

	// Mesma marca do caminho do resultado: durante o POST o estado local ainda é
	// "waiting" e uma reentrega não pode reabrir o cartão.
	a.markSupportChatInviteNotificationAnswered(result.SessionID)

	ctx, cancel := context.WithTimeout(a.supportChatContext(), supportChatInviteReportWait)
	defer cancel()

	reason := ""
	if !result.Accepted {
		reason = "user_denied"
	}

	var err error
	if result.Accepted {
		// O convite LOCAL que este cartão representava ainda está pendente? Se já
		// não estiver (outra sessão assumiu, ou o processo trocou de papel), o
		// aceite vai pelas funções PURAS do serviço — antes o POST simplesmente
		// não acontecia e o servidor reentrega o comando, reaparecendo o cartão.
		wasCurrentPending := false
		if state := a.supportChatService().State(); state.Status == supportchat.StatusWaiting && state.SessionID == result.SessionID {
			wasCurrentPending = true
		}

		// BUG RELATADO (agent no tray, UI relançada pelo watchdog): o cartão era
		// criado no processo de UI, mas o estado "waiting" vivo estava no
		// SERVIÇO (outro processo). A UI via "waiting" e o POST saía; ou via
		// "none"/outra sessão e o aceite era engolido — o console ficava
		// "Aguardando confirmação" e o usuário clicava de novo.
		//
		// O aceite NUNCA é condicional: é reportado para a sessão do CARTÃO.
		err = a.supportChatService().RespondInviteAcceptedForSession(ctx, result.SessionID)
		if err == nil && wasCurrentPending {
			// Estado local -> active + snapshot da conversa.
			if _, rerr := a.supportChatService().Refresh(ctx); rerr != nil {
				a.Logs.Append("[supportchat] snapshot apos o aceite indisponivel: " + rerr.Error())
			}
		}
		if err == nil {
			// ACEITE: a janela do chat abre JÁ, sem passar pela rechecagem de
			// estado do processo de UI (ver runSupportChatInviteNotificationDecision).
			// Foi essa rechecagem que, numa corrida com o evento do servidor,
			// descartou o aceite e deixou o cartão na tela sem insistir na
			// abertura da janela.
			if a.app != nil {
				a.hideSupportChatInviteSurface()
				go a.openSupportChatWindow()
			}
		}
	} else {
		// A recusa não depende do estado corrente: ela é reportada para a sessão
		// do cartão mesmo que outro convite tenha assumido no meio do caminho.
		err = a.supportChatService().RespondInviteForSession(ctx, result.SessionID, false, reason)
	}

	if err != nil {
		return err
	}

	decision := "recusado"
	if result.Accepted {
		decision = "aceito"
	}
	a.Logs.Append("[supportchat] convite " + decision + " no cartao nativo e reportado ao servidor (sessao " + result.SessionID + ")")
	return nil
}

// replaySupportChatInviteAfterUIReconnect reapresenta o convite pendente quando a
// UI (companion) se conecta ao serviço.
//
// BUG RELATADO: o atendente abria o chat, o cartão aparecia na máquina e, ao
// recusar, o console continuava mostrando "Aguardando confirmação" para sempre.
//
// O convite é entregue pelo canal unicast do NATS, que NÃO tem replay: se a
// resposta (recusa/aceite) se perder no caminho — UI recém-subida, pipe de IPC
// ainda conectando, processo trocando — o servidor reentrega o COMANDO alguns
// minutos depois. Antes essa reentrega virava uma nova pergunta ao usuário numa
// sessão ainda "Pending": ele recusava de novo, mas o resultado ficava ambíguo
// e o console não saía de "aguardando".
//
// Aqui, a cada (re)conexão da UI, o serviço consulta o servidor: se a sessão
// ainda está pendente, o cartão é reapresentado (ao usuário E ao atendente, já
// que um "Reenviar convite" também chega por aqui); se não existe mais, o
// estado local é limpo.
func (a *App) replaySupportChatInviteAfterUIReconnect() {
	if a == nil {
		return
	}

	state := a.supportChatService().State()
	if state.Status != supportchat.StatusWaiting || strings.TrimSpace(state.SessionID) == "" {
		return
	}

	ctx, cancel := context.WithTimeout(a.supportChatContext(), supportChatCallWait)
	defer cancel()

	activeID, hasSession, err := a.supportChatService().ActiveSessionID(ctx)
	if err != nil {
		a.Logs.Append("[supportchat] reentrega do convite: servidor indisponível — " + err.Error())
		return
	}

	// A sessão deixou de ser a pendente (foi respondida/encerrada/substituída):
	// nada a reexibir.
	current := a.supportChatService().State()
	if current.Status != supportchat.StatusWaiting || current.SessionID != state.SessionID {
		return
	}
	if !hasSession || activeID != state.SessionID {
		a.Logs.Append("[supportchat] convite " + state.SessionID + " nao esta mais pendente no servidor; limpando o estado local")
		a.clearSupportChatIfCurrent(state.SessionID)
		if hasSession {
			// Há outra conversa VIVA na máquina que esta UI ainda não conhece
			// (ex.: aceita enquanto a UI estava desconectada). Só limpar deixaria
			// o chat invisível até a reconciliação dos próximos 30 s — avisa a
			// UI para adotar a sessão (ver adoptSupportChatSession).
			a.EmitEvent("supportchat:replay", map[string]any{
				"sessionId": activeID,
				"previous":  state.SessionID,
			})
		}
		return
	}

	// Aceite CONCLUÍDO (o servidor já abriu a sessão): a marca de "já respondido"
	// cumpriu o papel e não pode ficar barrando a próxima pergunta.
	a.clearSupportChatInviteNotificationAnswered(state.SessionID)

	// Já respondido no cartão (resposta recente): não reabre.
	if a.supportChatInviteNotificationWasAnswered(state.SessionID) {
		return
	}

	// Convite VELHO não volta para a tela.
	//
	// RELATO: "se fecho o agent no tray e abro de novo, a notificação aparece
	// mas já é velha/atrasada". O estado local é em MEMÓRIA e só se perde no
	// restart do processo; quando a UI volta (tray) ele pode ter sobrevivido ao
	// tempo do convite. O cartão só é reapresentado se o convite ainda está
	// dentro da validade que o servidor mandou (expiresAtUtc).
	invite := supportchat.Invite{
		SessionID:   state.SessionID,
		TicketID:    state.TicketID,
		TicketTitle: state.TicketTitle,
		SupportName: state.SupportName,
		ExpiresAt:   state.ExpiresAt,
	}
	if invite.ExpiredAt(time.Now()) {
		a.Logs.Append("[supportchat] convite " + invite.SessionID + " expirado no relogio local; nao reapresentando (limpeza)")
		a.clearSupportChatIfCurrent(invite.SessionID)
		a.closeSupportChatInviteNotification(invite.SessionID)
		return
	}

	a.Logs.Append("[supportchat] UI reconectada com convite pendente; reapresentando (sessao " + state.SessionID + ")")
	a.presentSupportChatInvite(invite)
}

// adoptSupportChatSession adota a sessão que o SERVIÇO conhece mas esta UI
// ainda não (estado pedido pelo evento supportchat:replay).
//
// Acontece quando o usuário aceita enquanto a UI companion estava
// desconectada: a UI fica em "waiting"/"none" e, sem isto, o chat ficaria
// invisível até a reconciliação periódica (30 s). Aqui a ação é imediata e
// idempotente: um Refresh traz status da sessão + mensagens.
func (a *App) adoptSupportChatSession(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if a == nil || a.app == nil || sessionID == "" {
		return
	}

	a.closeAllSupportChatInviteNotifications()
	// O convite anterior não vale mais: marca como respondido para uma reentrega
	// tardia não reabrir o cartão por cima da conversa que está começando.
	if previous := strings.TrimSpace(a.supportChatService().State().SessionID); previous != "" && previous != sessionID {
		a.markSupportChatInviteNotificationAnswered(previous)
	}

	a.Logs.Append("[supportchat] adotando sessao viva do servico (sessao " + sessionID + ")")
	go func() {
		ctx, cancel := context.WithTimeout(a.supportChatContext(), supportChatCallWait)
		defer cancel()

		state, err := a.supportChatService().Refresh(ctx)
		if err != nil {
			a.Logs.Append("[supportchat] adocao da sessao falhou: " + err.Error())
			return
		}
		if !state.HasSession || state.SessionID != sessionID {
			// O servidor não confirma a sessão: deixa a reconciliação decidir.
			return
		}
		if state.Status == supportchat.StatusWaiting {
			// Ainda pendente: a superfície correta continua sendo o cartão.
			a.ensureSupportChatInviteSurface(state)
			return
		}
		a.openSupportChatWindow()
	}()
}

// supportChatInviteCardCloseGuard é o prazo após o qual o processo de UI força
// o fechamento de um cartão de convite que já foi respondido e continua na tela.
//
// RELATO: "cliquei em Agora não e o banner ficou travado". O POST pode demorar
// (ou o fechamento nativo falhar) e o usuário não pode ficar com o cartão
// preso: passado o prazo, o cancelador do cartão o fecha de qualquer forma.
const supportChatInviteCardCloseGuard = 4 * time.Second

// supportChatNotifyCardPending informa se o cartão da sessão ainda está
// registrado como aberto (o cancelamento o remove do mapa).
func (a *App) supportChatNotifyCardPending(sessionID string) bool {
	if a == nil {
		return false
	}
	a.supportChatNotifyMu.Lock()
	defer a.supportChatNotifyMu.Unlock()
	_, ok := a.supportChatNotify[strings.TrimSpace(sessionID)]
	return ok
}

// supportChatInviteAnswerWindow é por quanto tempo uma resposta recente do
// usuário (aceite/recusa no cartão) ainda pode explicar um convite que o
// servidor considera pendente. Depois disso, "o servidor reenviou o convite e a
// nossa resposta não chegou": a pergunta precisa aparecer de novo.
//
// Curto de propósito: cobre o redelivery pós-reconexão (segundos a ~1 min) sem
// deixar um convite pendente invisível por todo o TTL de 2 min da marca.
const supportChatInviteAnswerWindow = 90 * time.Second

// supportChatInviteRecentlyAnswered informa se o usuário respondeu este convite
// há menos de supportChatInviteAnswerWindow.
func (a *App) supportChatInviteRecentlyAnswered(sessionID string) bool {
	if a == nil || strings.TrimSpace(sessionID) == "" {
		return false
	}
	sessionID = strings.TrimSpace(sessionID)

	a.supportChatNotifyMu.Lock()
	defer a.supportChatNotifyMu.Unlock()
	until, ok := a.supportChatNotifyAnswered[sessionID]
	if !ok {
		return false
	}
	// Validade restante maior que (TTL - janela) ⇒ respondido há menos de
	// supportChatInviteAnswerWindow.
	return time.Until(until) > supportChatInviteNotificationAnsweredTTL-supportChatInviteAnswerWindow
}

// clearStaleSupportChatInviteAnswer apaga a marca de "já respondido" quando a
// resposta do usuário já não explica o convite pendente.
//
// BUG DA REVISÃO: a marca vale 2 min; se o POST da resposta falhava (servidor
// fora no momento do clique), o REDELIVERY do comando só chegava depois — e
// era engolido por "já respondido". O convite ficava pendente no console e o
// cartão não reaparecia para o usuário decidir de novo.
func (a *App) clearStaleSupportChatInviteAnswer(sessionID string) bool {
	if a == nil || strings.TrimSpace(sessionID) == "" {
		return false
	}
	sessionID = strings.TrimSpace(sessionID)
	if a.supportChatInviteRecentlyAnswered(sessionID) {
		return false
	}

	a.supportChatNotifyMu.Lock()
	until, ok := a.supportChatNotifyAnswered[sessionID]
	// Só limpa a marca AINDA válida (expirada já não suprime nada).
	if ok && time.Now().Before(until) {
		delete(a.supportChatNotifyAnswered, sessionID)
	} else {
		ok = false
	}
	a.supportChatNotifyMu.Unlock()

	if ok {
		a.Logs.Append("[supportchat] resposta anterior do usuario nao foi entregue ao servidor; convite pode ser reapresentado (" +
			sessionID + ")")
	}
	return ok
}

// presentSupportChatInvite exibe o cartão nativo e cai para a janela do chat
// quando o cartão não está disponível. Centraliza o caminho usado pelo evento
// IPC, pelo handler standalone e pela retomada no startup.
func (a *App) presentSupportChatInvite(invite supportchat.Invite) {
	a.presentSupportChatInviteWithRetry(invite, false)
}

// presentSupportChatInviteWithRetry é o caminho com a opção de REENTREGA
// INTENCIONAL: intentional=true (novo comando do servidor, ex.: "Reenviar
// convite" do console) reapresenta o cartão mesmo que o usuário já tenha
// respondido este convite antes, porque é uma pergunta NOVA.
func (a *App) presentSupportChatInviteWithRetry(invite supportchat.Invite, intentional bool) {
	if strings.TrimSpace(invite.SessionID) == "" {
		return
	}
	// No processo de SERVIÇO (sem Wails) não existe janela nativa para o
	// usuário: o cartão nasceria invisível no desktop da sessão 0. A superfície
	// correta é a UI companion, que recebe o evento via IPC.
	//
	// O evento é emitido SEMPRE que há UI companion conectada — e não só no
	// ServiceMode: no SDK/construtor de testes e quando o serviço participa do
	// modo standalone sobra um processo sem Wails cuja única saída é o IPC.
	if a.app == nil {
		// Uma ÚNICA superfície de aceite. Quem cria o cartão nativo é a UI
		// companion (que recebe o evento abaixo); abrir aqui a janela fallback
		// duplicaria o pedido (cartão + modal na janela do chat).
		if a.RuntimeFlags.ServiceMode || a.hasSupportChatUI() {
			// expiresAt viaja no evento: a UI precisa saber até quando o convite
			// vale para não apresentar um cartão já vencido.
			a.EmitEvent("supportchat:invite",
				"sessionId", invite.SessionID,
				"ticketId", invite.TicketID,
				"ticketTitle", invite.TicketTitle,
				"supportName", invite.SupportName,
				"expiresAt", invite.ExpiresAt)
			return
		}
	}
	if a.showSupportChatInviteNotification(invite, intentional) {
		a.startSupportChatInviteVerification(invite.SessionID)
		return
	}
	if err := a.supportChatContext().Err(); err != nil {
		// Encerrando: não abre uma janela nova durante o shutdown.
		return
	}
	// Fallback (não-Windows ou cartão indisponível): fluxo antigo, com toast,
	// janela do chat e modal de convite. A janela chega a existir com o convite
	// pendente de propósito (ela É a superfície de aceite neste fluxo) — é a
	// exceção de supportChatWindowMayOpen.
	a.notifySupportChatInvite(invite)
	a.openSupportChatWindow()
	a.startSupportChatInviteVerification(invite.SessionID)
}
