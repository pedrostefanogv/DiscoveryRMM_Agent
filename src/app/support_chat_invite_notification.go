package app

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	// Limite de UMA reapresentação por sessão (ver ensureSupportChatInviteSurface):
	// o cap da decisão de reapresentar segue a CONTAGEM, não o chamador — assim
	// nenhum caminho (evento IPC, handler JS, reconciliação, verify, UI nova)
	// consegue entrar no ciclo de fechar/reapresentar o mesmo cartão.
	if !intentional && a.supportChatNotifyShown != nil && a.supportChatNotifyShown[sessionID] >= supportChatInviteMaxPresentations {
		a.supportChatNotifyMu.Unlock()
		a.Logs.Append("[supportchat] cartao do convite ja foi reapresentado " + strconv.Itoa(a.supportChatNotifyShown[sessionID]) +
			"x; nao reapresenta de novo (sessao " + sessionID + ")")
		return true
	}
	if a.supportChatNotifyShown == nil {
		a.supportChatNotifyShown = make(map[string]int)
	}
	// Poda: o mapa é por sessão e o agent fica ligado por semanas — sem esta
	// limpeza cada conversa deixaria uma entrada para sempre (vazamento lento de
	// memória num processo de bandeja). O convite só existe por minutos, então
	// entradas de outras sessões aqui já cumpriram o papel.
	for id := range a.supportChatNotifyShown {
		if id != sessionID {
			delete(a.supportChatNotifyShown, id)
		}
	}
	a.supportChatNotifyShown[sessionID]++
	if intentional {
		// Reenvio do suporte é uma pergunta NOVA: reinicia o orçamento de
		// reapresentação desta sessão (senão o "Reenviar convite" ficaria
		// bloqueado para sempre pelo limite).
		a.supportChatNotifyShown[sessionID] = 1
	}
	if !intentional && a.supportChatNotifyShown[sessionID] > 1 {
		a.Logs.Append("[supportchat] reapresentando o cartao do convite (apresentacao #" +
			strconv.Itoa(a.supportChatNotifyShown[sessionID]) + " da sessao " + sessionID + ")")
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

	// guardFired marca que a REDE DE SEGURANÇA fechou este cartão. Quem chama
	// usa isso para limitar a reapresentação a UMA por sessão: o ciclo
	// "fecha pela rede de segurança → estado waiting sem cartão → reapresenta"
	// já apareceu 4 vezes no log de um teste real, e cada volta era uma decisão
	// do usuário perdida.
	var (
		guardOnce  sync.Once
		guardFired atomic.Bool
		guardDone  = make(chan struct{})
	)
	// BUG CORRIGIDO (revisao): aqui existia um TIMER DE 4s armado na CRIACAO do
	// cartao. Passados 4 segundos com o cartao ainda na TELA e SEM clique, o cartao
	// era fechado e uma recusa automatica ("user_dismissed") era enviada ao
	// servidor: o convite desaparecia sozinho e o atendente via "recusado" mesmo
	// sem o usuario responder. Era tambem a causa do ciclo visto no log real (4x
	// "fechando pela rede de seguranca" + "reapresentando") e do sintoma "aparece
	// a notificacao mas a conversa nao abre".
	//
	// A rede de seguranca existe para o cartao JA RESPONDIDO que nao saiu da tela
	// (thread ocupada, hwnd invalido). Ela so age quando ha uma decisao
	// registrada: checa supportChatNotifyCardDecided a cada segundo e espera o
	// prazo a partir do CLIQUE. Sem clique o cartao fica na tela ate a
	// reconciliacao (expiracao/substituicao), que fecha sem reportar recusa.
	go func() {
		ticker := time.NewTicker(supportChatInviteCardGuardPoll)
		defer ticker.Stop()
		for {
			select {
			case <-guardDone:
				return
			case <-a.supportChatContext().Done():
				return
			case <-ticker.C:
			}
			if !a.supportChatNotifyCardDecided(sessionID) {
				continue
			}
			guardOnce.Do(func() {
				guardFired.Store(true)
				if !a.supportChatNotifyCardPending(sessionID) {
					a.Logs.Append("[supportchat] rede de seguranca: cartao da sessao " + sessionID + " ja nao esta registrado; nada a fechar")
					return
				}
				a.Logs.Append("[supportchat] cartao respondido ainda na tela apos o prazo; fechando pela rede de seguranca (sessao " + sessionID + ")")
				a.forgetSupportChatInviteNotification(sessionID)
				if cancel != nil {
					cancel()
				}
			})
			return
		}
	}()
	defer guardOnce.Do(func() { close(guardDone) })

	select {
	case <-a.supportChatContext().Done():
		a.forgetSupportChatInviteNotification(sessionID)
		a.closeSupportChatInviteNotification(sessionID)
		return
	case result, ok = <-resultCh:
	}

	// OK: o próprio cartão devolveu a decisão (e já reportou ao servidor). O
	// registro sai do mapa — o cartão já não existe na tela.
	a.forgetSupportChatInviteNotification(sessionID)

	// 3.4: e a REDE DE SEGURANÇA fechou o cartão antes da decisão chegar? Então
	// esta é uma decisão ÓRFÃ de um cartão que já saiu da tela (o usuário clicou
	// no último instante). A recusa já foi reportada pela rede de segurança com o
	// mesmo SessionID (POST idempotente por sessão); o aceite ainda pode ser
	// aplicado localmente (abrir a janela), mas não pode mais ser enviado como
	// se o cartão estivesse na tela.
	guardClosedCard := guardFired.Load()

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
	// em paralelo ao fechamento da janela nativa — ver
	// support_chat_invite_notification_windows.go. Em modo companion o cartão é
	// criado na UI e o serviço só publica por IPC: a decisão do cartão é a
	// AUTORIDADE; aqui só se aplica o que falta no processo de UI.
	if result.Error != nil {
		a.Logs.Append("[supportchat] falha ao reportar a resposta do cartao nativo (sessao " + sessionID +
			", decisao " + supportChatInviteDecisionLabel(result.Accepted) + "): " + result.Error.Error())
	}

	// A resposta é aplicada à sessão do CARTÃO. Um SessionID diferente do cartão
	// atual não existe em produção (o cartão carrega o próprio convite); quando
	// acontece é resultado corrompido e usar o do resultado é o mais seguro.
	if result.SessionID != "" && sessionID == "" {
		sessionID = strings.TrimSpace(result.SessionID)
	} else if result.SessionID != "" && result.SessionID != sessionID {
		a.Logs.Append("[supportchat] sessao do resultado difere do cartao; usando a sessao do cartao (cartao=" +
			sessionID + " resultado=" + result.SessionID + ")")
	}

	// 3.3: a RECUSA de um cartão cujo convite foi SUBSTITUÍDO continua válida —
	// o POST é por SessionID e é justamente assim que o convite antigo deixa de
	// ficar "aguardando". Só o ACEITE precisa da sessão corrente (ele abre a
	// janela do chat).
	if !result.Accepted {
		if result.Error != nil {
			a.retryDeniedSupportChatInvite(sessionID, result.Error)
			return
		}
		a.finishSupportChatInviteDenial(sessionID)
		return
	}
	if guardClosedCard {
		a.Logs.Append("[supportchat] aceite chegou depois de a rede de seguranca fechar o cartao (sessao " + sessionID +
			"); aplicando o estado local, sem novo POST")
	}

	state := a.supportChatService().State()
	if state.Status == supportchat.StatusActive && state.SessionID == sessionID {
		// ACEITE CONCLUÍDO: o POST já foi feito pelo cartão (reportSupportChatInviteDecision),
		// que também já pediu a abertura da janela. Aqui só garantimos a janela —
		// sem descartar a decisão. Antes este caminho tratava o convite já ativo
		// como "decisão descartada" e o cartão ficava na tela sem a janela abrir.
		a.Logs.Append("[supportchat] aceite confirmado apos o cartao; garantindo a janela do chat (sessao " + sessionID + ")")
		a.hideSupportChatInviteSurface()
		go a.openSupportChatWindow()
		return
	}
	if state.Status != supportchat.StatusWaiting || state.SessionID != sessionID {
		// O convite já não é o pendente: para a RECUSA nada se perde (ela foi
		// reportada por SessionID logo acima); para o ACEITE, a janela ainda é
		// garantida — era isso que deixava o cartão sumir sem o chat abrir.
		a.Logs.Append("[supportchat] decisao do cartao sem convite pendente; aplicando o minimo (sessao " + sessionID + ")")
		if a.app != nil {
			a.hideSupportChatInviteSurface()
			go a.openSupportChatWindow()
		}
		return
	}

	if result.Accepted {
		a.Logs.Append("[supportchat] convite aceito no cartao nativo (sessao " + sessionID + ")")
		// ACEITE: o POST ao servidor JÁ foi feito pelo cartão
		// (reportSupportChatInviteDecision → RespondInviteAcceptedForSession, que
		// também move o estado local e pede o snapshot). NÃO repetir aqui:
		// SupportChatRespondInvite faria um SEGUNDO POST de aceite na mesma
		// sessão (o servidor responderia "session_not_pending" e o log viraria
		// ruído), e ainda chamaria Refresh de novo.
		//
		// Se o cartão reportou erro (servidor fora), ainda vale abrir a janela:
		// ela mostra o estado/erro e o usuário não perde o contexto.
		a.hideSupportChatInviteSurface()
		if result.Error != nil {
			a.Logs.Append("[supportchat] aceite nao confirmado pelo servidor; abrindo a janela mesmo assim (sessao " + sessionID + ")")
		}
		if a.app != nil {
			a.openSupportChatWindow()
		}
		return
	}

	a.finishSupportChatInviteDenial(sessionID)
}

// finishSupportChatInviteDenial aplica o efeito LOCAL de uma recusa que JÁ foi
// entregue ao servidor: descarta o estado e fecha a janela do chat (caminho
// fallback).
//
// Só é chamada quando o servidor confirmou a recusa (ou já a tinha resolvido):
// limpar o estado com o POST pendente deixaria o convite invisível aqui e vivo
// no console (o defeito "recusei e o servidor nunca soube").
func (a *App) finishSupportChatInviteDenial(sessionID string) {
	if a == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	a.Logs.Append("[supportchat] convite recusado no cartao nativo (sessao " + sessionID + ")")
	a.clearSupportChatIfCurrent(sessionID)
	// Marca a sessão como respondida: uma reentrega tardia do comando (ou um
	// evento IPC atrasado) não pode reabrir o cartão de um convite já recusado.
	if sessionID != "" {
		a.markSupportChatInviteNotificationAnswered(sessionID)
	}
	a.closeSupportChatWindow()
}

// retryDeniedSupportChatInvite é o caminho da RECUSA quando o cartão não
// conseguiu entregá-la (3.2).
//
// RELATO (teste real): a UI logava "convite recusado ... e reportado ao
// servidor" e o SERVIÇO nunca soube — o erro do POST só ia para o log e o
// estado local era limpo na sequência. Agora o erro volta em result.Error, o
// POST é retentado (2s/5s/10s) e, se ele continuar falhando, o estado local NÃO
// é limpo: o convite segue pendente e pode ser reapresentado / reconciliado.
func (a *App) retryDeniedSupportChatInvite(sessionID string, firstErr error) {
	if a == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	a.Logs.Append("[supportchat] recusa do cartao nao chegou ao servidor; retentando (sessao " + sessionID + "): " + firstErr.Error())
	if err := a.retrySupportChatInviteDenial(sessionID, "user_denied"); err != nil {
		// Não limpa o estado: o convite continua pendente e uma reentrega/UI nova
		// pode reapresentá-lo. A marca de "respondido" vale 2 min; passada a
		// janela recente (clearStaleSupportChatInviteAnswer), a reentrega volta a
		// perguntar ao usuário.
		a.Logs.Append("[supportchat] recusa do cartao definitivamente nao entregue (sessao " + sessionID +
			"); convite mantido pendente: " + err.Error())
		return
	}
	a.finishSupportChatInviteDenial(sessionID)
}

// supportChatInviteGuardDeniedReason foi o motivo usado quando a rede de
// seguranca fechava o cartao SEM decisao do usuario. Esse caminho foi removido:
// fechar e recusar um cartao que o usuario ainda esta vendo fazia o convite
// desaparecer sozinho. O motivo continua aceito pelo servidor e pelo console
// (capabilities ja publicadas) para builds anteriores nao gerarem dado orfao.
const supportChatInviteGuardDeniedReason = "user_dismissed"
// supportChatInvitePresentationCount informa quantas vezes o cartão desta
// sessão já foi apresentado (ver showSupportChatInviteNotification).
func (a *App) supportChatInvitePresentationCount(sessionID string) int {
	if a == nil || strings.TrimSpace(sessionID) == "" {
		return 0
	}
	a.supportChatNotifyMu.Lock()
	defer a.supportChatNotifyMu.Unlock()
	return a.supportChatNotifyShown[strings.TrimSpace(sessionID)]
}

// markSupportChatInvitePresentation conta uma apresentação do cartão (limite de
// 1 reapresentação por sessão — ver ensureSupportChatInviteSurface).
func (a *App) markSupportChatInvitePresentation(sessionID string) {
	if a == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	a.supportChatNotifyMu.Lock()
	if a.supportChatNotifyShown == nil {
		a.supportChatNotifyShown = make(map[string]int)
	}
	a.supportChatNotifyShown[sessionID]++
	a.supportChatNotifyMu.Unlock()
}

// supportChatInviteGuardClosed informa se a rede de segurança fechou o cartão
// desta sessão sem decisão do usuário (o estado "waiting" que sobrou não é um
// pedido que o usuário esteja vendo).
func (a *App) supportChatInviteGuardClosed(sessionID string) bool {
	if a == nil || strings.TrimSpace(sessionID) == "" {
		return false
	}
	sessionID = strings.TrimSpace(sessionID)
	a.supportChatNotifyMu.Lock()
	defer a.supportChatNotifyMu.Unlock()
	closedAt, ok := a.supportChatNotifyGuardClosed[sessionID]
	return ok && time.Since(closedAt) < supportChatInviteGuardCloseTTL
}

// markSupportChatInviteGuardClosed registra que a rede de segurança fechou o
// cartão da sessão.
func (a *App) markSupportChatInviteGuardClosed(sessionID string) {
	if a == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	now := time.Now()
	a.supportChatNotifyMu.Lock()
	if a.supportChatNotifyGuardClosed == nil {
		a.supportChatNotifyGuardClosed = make(map[string]time.Time)
	}
	for id, at := range a.supportChatNotifyGuardClosed {
		if now.Sub(at) > supportChatInviteGuardCloseTTL {
			delete(a.supportChatNotifyGuardClosed, id)
		}
	}
	a.supportChatNotifyGuardClosed[sessionID] = now
	a.supportChatNotifyMu.Unlock()
}

// supportChatInviteGuardCloseTTL é por quanto tempo a marca "a rede de
// segurança fechou este cartão" vale. Curta de propósito: se o convite
// reaparecer legitimamente (reenvio do suporte) depois disso, a pergunta deve
// poder ser feita de novo.
const supportChatInviteGuardCloseTTL = 2 * time.Minute

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
		// 409 session_declined/session_ended/session_expired: o servidor JÁ
		// resolveu esta sessão (outra máquina, expiração, ou este mesmo POST que
		// teve sucesso e cujo retorno se perdeu). A decisão não está perdida —
		// retentar reescreveria o estado de uma sessão terminal.
		if isSupportChatInviteAlreadyResolved(err) {
			a.Logs.Append("[supportchat] resposta do convite ja resolvida no servidor (sessao " +
				result.SessionID + ", decisao " + supportChatInviteDecisionLabel(result.Accepted) + "): " + err.Error())
			return nil
		}
		// Falha do POST: loga aqui (o cartão já saiu da tela; este é o único
		// ponto que garante o registro, independentemente do caminho que
		// consumir a decisão). O erro TAMBÉM segue em result.Error para o
		// consumidor da decisão (ver runSupportChatInviteNotificationDecision).
		a.Logs.Append("[supportchat] falha ao reportar a resposta do convite ao servidor (sessao " +
			result.SessionID + ", decisao " + supportChatInviteDecisionLabel(result.Accepted) + "): " + err.Error())
		return err
	}

	a.Logs.Append("[supportchat] convite " + supportChatInviteDecisionLabel(result.Accepted) +
		" no cartao nativo e reportado ao servidor (sessao " + result.SessionID + ")")
	return nil
}

// supportChatInviteDecisionLabel devolve o rótulo da decisão usado nos logs.
func supportChatInviteDecisionLabel(accepted bool) string {
	if accepted {
		return "aceito"
	}
	return "recusado"
}

// ── Desfecho da recusa do cartão (fase 3) ───────────────────────────────────
//
// RELATO (teste real): o cartão fechou e reapareceu 4 vezes e, quando o usuário
// clicou em "Agora não", o log da UI dizia "recusado ... e reportado ao
// servidor" enquanto o SERVIÇO nunca soube — o console ficou "Aguardando
// confirmação" e cada fechamento pela rede de segurança era uma DECISÃO
// PERDIDA.
//
// Regras novas (3.2 / 3.3):
//   - o erro do envio deixa de ser só log: o consumidor recebe result.Error e,
//     para RECUSA, retenta o POST (2s/5s/10s) — nunca limpa o estado local se a
//     recusa não foi entregue;
//   - a recusa é reportada por SessionID, então a decisão de um cartão cujo
//     convite foi SUBSTITUÍDO continua válida (não é descartada);
//   - 409 (sessão já recusada/encerrada/expirada) é sucesso, não retentativa.

// supportChatInviteDenyRetryDelays é a agenda de novas tentativas do POST de
// recusa quando ele falha no cartão. O total (17s) cabe no TTL de 2 min da marca
// de "já respondido" e no timeout de resposta do servidor (120s).
var supportChatInviteDenyRetryDelays = []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second}

// supportChatInviteDenyMaxAttempts é o teto de tentativas para o consumidor da
// decisão. Variável (e não const) para os testes não dependerem do relógio.
var supportChatInviteDenyMaxAttempts = 3

// supportChatInviteDenyBackoff devolve o intervalo antes da tentativa
// seguinte de uma série começada em delayIndex (ver
// supportChatInviteDenyRetryDelays).
func supportChatInviteDenyBackoff(delayIndex int) time.Duration {
	if len(supportChatInviteDenyRetryDelays) == 0 {
		return 0
	}
	if delayIndex < 0 {
		delayIndex = 0
	}
	if delayIndex >= len(supportChatInviteDenyRetryDelays) {
		delayIndex = len(supportChatInviteDenyRetryDelays) - 1
	}
	return supportChatInviteDenyRetryDelays[delayIndex]
}

// retrySupportChatInviteDenial reenvia a RECUSA do convite até
// supportChatInviteDenyMaxAttempts vezes, com backoff, e devolve o último erro.
//
// A sessão do POST é SEMPRE a sessão do cartão (nunca a "corrente"): outra
// conversa pode ter assumido no meio do caminho e não pode ser recusada por
// engano.
func (a *App) retrySupportChatInviteDenial(sessionID, reason string) error {
	if a == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if strings.TrimSpace(reason) == "" {
		reason = "user_denied"
	}

	// Conta as tentativas numa variável própria: o nome não pode se chamar "err"
	// por engano na lógica abaixo (bug pego por teste: a tentativa nº 1 "voltava"
	// como sucesso).
	var lastErr error
	for attempt := 1; attempt <= supportChatInviteDenyMaxAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(a.supportChatContext(), supportChatInviteReportWait)
		err := a.supportChatService().RespondInviteForSession(ctx, sessionID, false, reason)
		cancel()
		if err == nil {
			if attempt > 1 {
				a.Logs.Append("[supportchat] recusa do convite entregue na tentativa " + strconv.Itoa(attempt) + " (sessao " + sessionID + ")")
			}
			return nil
		}
		lastErr = err
		if isSupportChatInviteAlreadyResolved(err) {
			a.Logs.Append("[supportchat] recusa do convite ja resolvida no servidor (sessao " + sessionID + "): " + err.Error())
			return nil
		}
		a.Logs.Append("[supportchat] tentativa " + strconv.Itoa(attempt) + "/" + strconv.Itoa(supportChatInviteDenyMaxAttempts) +
			" de reportar a recusa falhou (sessao " + sessionID + ", motivo " + reason + "): " + err.Error())
		if attempt == supportChatInviteDenyMaxAttempts {
			break
		}
		select {
		case <-a.supportChatContext().Done():
			return lastErr
		case <-time.After(supportChatInviteDenyBackoff(attempt - 1)):
		}
	}
	return lastErr
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
		// clearSupportChatIfCurrent: só limpa se o estado AINDA é deste convite
		// (outro convite pode ter assumido entre a leitura e aqui).
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

// supportChatInviteMaxPresentations é o número máximo de vezes que o cartão do
// MESMO convite aparece para o usuário (1 apresentação + 1 reapresentação).
//
// RELATO (teste real): o cartão foi fechado e reapresentado 4 vezes seguidas
// ("cartao do convite ainda na tela apos o prazo; fechando pela rede de
// seguranca" x4). Cada volta é uma chance de o usuário responder um cartão que
// já não existe e de a decisão se perder. Com o limite, a 2ª apresentação é a
// última: dali em diante quem decide é a rede de segurança (recusa reportada) e
// a reconciliação.
const supportChatInviteMaxPresentations = 2

// supportChatInviteCardCloseGuard é o prazo após o qual o processo de UI força
// o fechamento de um cartão de convite que já foi respondido e continua na tela.
//
// RELATO: "cliquei em Agora não e o banner ficou travado". O POST pode demorar
// (ou o fechamento nativo falhar) e o usuário não pode ficar com o cartão
// preso: passado o prazo, o cancelador do cartão o fecha de qualquer forma.
const supportChatInviteCardCloseGuard = 4 * time.Second

// supportChatInviteCardGuardPoll é o intervalo com que a rede de segurança
// confere se o cartão já foi RESPONDIDO (ver runSupportChatInviteNotificationDecision).
// Ela NÃO pode fechar um cartão sem decisão: um cartão na tela é uma pergunta
// legítima esperando o usuário, e fechá-lo enviava "recusa" ao servidor sem
// ninguém ter clicado.
const supportChatInviteCardGuardPoll = time.Second



// supportChatNotifyCardPending informa se o cartão da sessão ainda está
// registrado como aberto (o cancelamento o remove do mapa).
// supportChatNotifyCardDecided informa se o cartão da sessão já recebeu uma
// decisão do usuário e por isso pode ser fechado à força pela rede de segurança.
//
// Usa a marca de "respondido" (supportChatNotifyAnswered), que é gravada no
// CLIQUE (markSupportChatInviteNotificationAnswered), antes do POST. Um cartão
// na tela SEM essa marca é uma pergunta legítima esperando o usuário e não pode
// ser fechado nem recusado automaticamente.
func (a *App) supportChatNotifyCardDecided(sessionID string) bool {
	if a == nil || strings.TrimSpace(sessionID) == "" {
		return false
	}
	a.supportChatNotifyMu.Lock()
	defer a.supportChatNotifyMu.Unlock()
	_, ok := a.supportChatNotifyAnswered[strings.TrimSpace(sessionID)]
	return ok
}
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
