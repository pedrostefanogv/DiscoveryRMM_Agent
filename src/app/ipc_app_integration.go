package app

// Integração IPC com o ciclo de vida da App (PLANO_AGENT_SERVICE_SYSTEM.md,
// Fase 2): handlers do lado do serviço (repassa notificações/eventos) e da
// UI (companion mode: handshake + fallback standalone).

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strings"
	"time"
)

// handleIPCMessage processa mensagens recebidas de UIs conectadas (lado do
// serviço). Responde hello_ack e encaminha notification:respond / command_result
// para os domínios correspondentes. A conn de origem é fornecida para replies
// diretos (status_snapshot vai só à UI que pediu, não em broadcast).
func (a *App) handleIPCMessage(conn net.Conn, msg IPCMessage) {
	if a == nil || msg.Type == "" {
		return
	}
	switch msg.Type {
	case IPCMsgHello:
		a.Logs.Append("[ipc] handshake recebido da UI")
		// hello_ack é respondido diretamente na conexão via Broadcast — o
		// cliente faz probe com IsServicePresent antes de conectar de fato.
	case IPCMsgStatus:
		// Snapshot de status solicitado pela UI companion (contrato Fase 2):
		// conectividade real do core que roda no serviço.
		// Revisão 2026-09-05: reply DIRETO na conn de origem (antes era
		// broadcast — com uma conn zumbi no map, o snapshot nunca chegava à UI
		// viva e o tray ficava offline).
		agent := a.GetAgentStatus()
		if a.ipcServer != nil {
			// reason/lastEvent acompanham o snapshot para a UI logar e
			// diagnosticar oscilações do indicador (ex.: "reconectando
			// (planejado): watchdog global pong ...").
			// Estado de onboarding calculado AQUI no serviço (loadInstallerConfig
			// + flag zero-touch local): a UI companion usa para a overlay de
			// "aguardando provisionamento/aprovação" refletir a verdade do core.
			onb := a.GetOnboardingStatus()
			// transportConnected é o estado CRU do transporte NATS: com a API HTTP
			// fora, `connected` (efetivo) é false mas o NATS segue de pé — a página
			// de Status precisa distinguir os dois casos.
			transportConnected := false
			if a.AgentConn != nil {
				transportConnected = a.AgentConn.GetStatus().Connected
			}
			// apiReachable permite ao chat/status diferenciar "API fora" de
			// "sem transporte" (banner específico na UI).
			apiReachable := true
			if a.ApiClientSvc != nil {
				apiReachable = a.ApiClientSvc.APIReachable()
			}
			a.ipcServer.RespondTo(conn, NewIPCMessage(IPCMsgEvent, map[string]any{
				"name":               "agent:status_snapshot",
				"connected":          agent.Connected,
				"transportConnected": transportConnected,
				"apiReachable":       apiReachable,
				"transport":          agent.Transport,
				"reason":             strings.TrimSpace(agent.OnlineReason),
				"lastEvent":          strings.TrimSpace(agent.LastEvent),
				// Último ping do servidor (página de Status na UI companion): o
				// agentConn roda AQUI no serviço — a UI não tem esses campos localmente.
				"lastGlobalPongAtUtc": strings.TrimSpace(agent.LastGlobalPongAtUTC),
				"globalPongStale":     agent.GlobalPongStale,
				// Estado de onboarding (overlay de provisionamento/aprovação).
				"onboardingMode":       onb["mode"],
				"onboardingConfigured": onb["configured"],
				"onboardingMessage":    onb["message"],
			}))
			// Estado do CHAT DE SUPORTE junto do snapshot de status.
			//
			// BUG CORRIGIDO (revisão): a UI só aprendia o estado do chat por eventos,
			// e eventos só viajam quando algo TRANSICIONA. Uma UI que (re)conecta no
			// meio de uma conversa — o log mostra reconexões de IPC a cada poucos
			// minutos — ficava com o estado de antes: convencida de que não havia
			// sessão, ou de que havia OUTRA, e então recusava convites como "máquina
			// ocupada" ou deixava de mostrar o cartão.
			//
			// O snapshot de status já vai só para a UI que pediu (RespondTo), então
			// não há risco de eco: a UI apenas ADOTA este estado.
			if a.supportChatSvc != nil {
				chatState := a.supportChatService().State()
				a.ipcServer.RespondTo(conn, NewIPCMessage(IPCMsgEvent, map[string]any{
					"name":        supportChatStateEventName,
					"sessionId":   chatState.SessionID,
					"ticketId":    chatState.TicketID,
					"ticketTitle": chatState.TicketTitle,
					"supportName": chatState.SupportName,
					"expiresAt":   chatState.ExpiresAt,
					"status":      chatState.Status,
					// Link REAL do canal (o NATS é deste processo): a UI
					// adota este valor em vez de inferir "conectado" do
					// próprio estado — era daí que vinha o aviso falso de
					// "Sem conexão em tempo real" na janela do chat.
					"supportChatLinkActive": a.supportChatService().LinkActive(),
				}))
			}
		}
	case IPCMsgRemoteSession:
		// S2: remote_session só é aceito de conexão ADMIN autenticada com token
		// válido cujo pid/sessão batem com o pedido. Sem isso, recusa e NUNCA
		// spawna worker (o payload também não recebe o authToken vivo).
		//
		// FAIL-CLOSED: o autorizador é nil-safe e DENEGA sem servidor IPC (o
		// guard antigo "ipcServer != nil" liberava o caminho inteiro quando o
		// servidor ainda não tinha sido atribuído à App — janela real entre
		// StartIPCServer (aceita e despacha) e a.ipcServer = ... no startup).
		if err := a.ipcServer.authorizeRemoteSessionMessage(conn, msg.Payload); err != nil {
			ipcAuthStats.RemoteSessionDenied.Add(1)
			a.Logs.Append("[ipc] remote session recusada (sem autenticação): " + err.Error())
			logIPCStructured("remote_session_denied", fmt.Sprintf("conn=%v reason=%q", conn != nil, err))
			return
		}
		// Remote session DEVE rodar na sessão interativa do usuário (a sessão 0
		// do serviço SYSTEM não tem desktop — captura falha e SendInput é
		// bloqueado por UIPI).
		// M-fix (decisão do dono — M5): a sessão roda NO WORKER spawnado pelo
		// serviço (SYSTEM na sessão interativa) — nunca na UI (Medium integrity:
		// UIPI bloqueia input em janelas elevadas e sem acesso ao desktop de
		// logon).
		a.Logs.Append("[ipc] remote session via IPC → spawn worker (sessão interativa)")
		if parsed := parseAnyMap(msg.Payload); parsed != nil {
			sid, _ := parsed["sessionId"].(string)
			act, _ := parsed["action"].(string)
			if act == "stop" {
				stopRemoteSessionWorker(sid)
			} else {
				// Injeta o token VIVO em memória no payload do worker (mesma
				// correção do caminho NATS — remote_debug_commands): o token do
				// debug_config.json fica velho após a rotação P2P e o NATS
				// rejeitava o worker com "Authorization Violation".
				if live := strings.TrimSpace(a.DebugSvc.GetConfig().AuthToken); live != "" {
					parsed["authToken"] = live
				}
				go func() {
					if err := spawnRemoteSessionWorker(context.Background(), parsed); err != nil {
						a.Logs.Append("[ipc] erro ao spawnar remote session worker: " + err.Error())
					}
				}()
			}
		}
	case IPCMsgNotificationRespond:
		// S3: a resposta só é aceita de conexão autenticada da sessão
		// destinatária do prompt (ou SYSTEM). A sessão alvo é persistida quando
		// o notification:new é emitido (ver broadcastIPCEvent).
		// FAIL-CLOSED: sem servidor IPC não há como provar o destinatário — nega.
		if !a.ipcServer.authorizeNotificationRespond(conn, msg.Payload) {
			a.Logs.Append("[ipc] resposta de notificação recusada: conexão não autenticada ou de outra sessão")
			return
		}
		if a.handleIPCNotificationRespond(msg.Payload) {
			return
		}
		a.Logs.Append("[ipc] resposta de notificação sem payload válido")
	case IPCMsgCommandResult:
		a.Logs.Append("[ipc] resultado de comando interativo recebido da UI (encaminhamento é extensão futura)")
	case IPCMsgRequest:
		// Request/response RPC (PLANO_SEPARACAO_SERVICO_UI.md, Fase C): a UI
		// companion consulta o core do serviço (status, config, inventário,
		// memory) sem abrir o SQLite do serviço (decisão D3).
		//
		// Gate de autenticação do dispatch: authorizeRPC revalida canal/token e
		// devolve o context com a identidade; o próprio handleIPCRequest recusa
		// mutação sem esse context. Respostas de erro de auth não passam pelo
		// handler (nada é executado).
		// authorizeRPC é nil-safe: sem servidor IPC o contexto sai SEM auth e o
		// gate de handleIPCRequest recusa qualquer mutação (fail-closed).
		// A.0.8: o audit de gate registra "allowed" ANTES de executar; aqui
		// capturamos método/início para logar o DESFECHO real (ok|code + duração)
		// quando a resposta for emitida.
		method, _ := msg.Payload["method"].(string)
		mutating := ipcRequestRequiresAdmin(method, msg.Payload)
		dispatchStart := time.Now()

		ctx := context.Background()
		authCtx, authErr := a.ipcServer.authorizeRPC(conn, msg)
		if authErr != nil {
			method, _ := msg.Payload["method"].(string)
			logIPCStructured("dispatch_denied", fmt.Sprintf("method=%q cid=%q reason=%q", method, msg.CorrelationID, authErr))
			if a.ipcServer != nil {
				reply := NewIPCMessage(IPCMsgResponse, map[string]any{
					"ok":    false,
					"error": "unauthorized",
					"code":  "ipc_unauthorized",
				})
				reply.CorrelationID = msg.CorrelationID
				a.ipcServer.RespondTo(conn, reply)
			}
			return
		}
		ctx = authCtx
		// Request em execução (ex.: instalação da loja, que pode levar
		// minutos): marca a conexão como ativa para o sweep de ociosidade não
		// derrubá-la no meio da operação. O touch no fim renova a janela.
		var inflightClient *ipcClientConn
		if a.ipcServer != nil {
			// clientFor já devolve o estado da conexão (nil-safe).
			if inflightClient = a.ipcServer.clientFor(conn); inflightClient != nil {
				inflightClient.inFlight.Add(1)
				defer func() {
					// touch ANTES de zerar o contador: o sweep decide pela
					// ociosidade, e zerar primeiro abriria uma janela em que a
					// conexão parece ociosa há minutos e seria fechada logo
					// depois da resposta.
					inflightClient.touch()
					inflightClient.inFlight.Add(-1)
				}()
			}
		}
		// O token nunca é repassado aos handlers/RPCs.
		delete(msg.Payload, "token")
		resp := a.handleIPCRequest(ctx, msg.Payload)
		if mutating {
			// Desfecho real do mutante (independente do gate): sucesso vira
			// result=ok; falha vira o code de domínio (ou "error") e incrementa
			// o contador de falhas.
			auth, _ := ipcAuthFromContext(ctx)
			outcomeOK, _ := resp["ok"].(bool)
			outcomeCode, _ := resp["code"].(string)
			ipcAuditMutatingOutcome(method, auth, outcomeOK, outcomeCode, time.Since(dispatchStart))
		}
		if a.ipcServer != nil {
			a.ipcServer.recordRequestResult(conn, msg.CorrelationID, resp)
			reply := NewIPCMessage(IPCMsgResponse, resp)
			reply.CorrelationID = msg.CorrelationID
			a.ipcServer.RespondTo(conn, reply)
		}
	case IPCMsgDiagnosticStacks:
		// Mensagem SERVIÇO→UI (pedido de stacks da UI zumbi). O serviço aqui é
		// o emissor, não o destinatário — um eco de cliente antigo é ignorado.
		log.Printf("[ipc] diagnostic:stacks recebida de cliente (eco ignorado)")
	default:
		log.Printf("[ipc] mensagem não tratada do tipo %s", msg.Type)
	}
}

// handleIPCNotificationRespond processa a resposta do usuário recebida via
// IPC (UI companion) e injeta no notificationSvc (pendingNotifyResult) —
// fecha o ciclo serviço→UI→resposta→NATS do plano (Fase 2).
func (a *App) handleIPCNotificationRespond(payload map[string]any) bool {
	if a == nil || a.NotificationSvc == nil || payload == nil {
		return false
	}
	notificationID, _ := payload["notificationId"].(string)
	result, _ := payload["result"].(string)
	if strings.TrimSpace(notificationID) == "" || strings.TrimSpace(result) == "" {
		return false
	}
	ok := a.NotificationSvc.Respond(notificationID, result)
	a.Logs.Append(fmt.Sprintf("[ipc] resposta de notificação %s -> %s (injetada=%t)", notificationID, result, ok))
	return ok
}

// ipcEventRawKey é a chave reservada do payload IPC para eventos cujo dado é
// um valor único NÃO-mapa (string JSON, struct, número...). Ela é necessária
// porque o contrato do pipe espalha os campos do evento no próprio payload:
// um valor único precisaria de um nome de campo, que não existe.
const ipcEventRawKey = "__eventData"

// buildIPCEventPayload empacota um EmitEvent(name, data...) no payload IPC
// {name, <campos>}. As três formas de chamada do EmitEvent são preservadas:
//
//   - 0 argumentos .......... evento sem dados (só "name");
//   - 1 argumento mapa ...... campos do mapa viram campos do payload
//     (é o caso de agent:connectivity, store:catalog-updated, ...);
//   - 1 argumento escalar ... valor cru sob ipcEventRawKey (chat:question,
//     screenshot:request, p2p:transfer-progress, ...);
//   - N argumentos .......... pares chave/valor (agent:onboarding, updates:list).
//
// BUG CORRIGIDO: antes o loop de pares era a ÚNICA forma tratada, então
// EmitEvent(name, mapa) e EmitEvent(name, valor) eram enviados ao pipe sem
// nenhum dado — a UI companion recebia o evento vazio e tratava tudo como
// offline (chat indisponível/suporte somente-consulta) mesmo com o servidor
// no ar.
func buildIPCEventPayload(name string, data ...any) map[string]any {
	payload := make(map[string]any, len(data)/2+2)
	payload["name"] = name
	switch {
	case len(data) == 1:
		switch m := data[0].(type) {
		case map[string]any:
			for k, v := range m {
				if k == "name" || k == ipcEventRawKey {
					continue
				}
				payload[k] = v
			}
		case map[string]string:
			for k, v := range m {
				if k == "name" || k == ipcEventRawKey {
					continue
				}
				payload[k] = v
			}
		default:
			payload[ipcEventRawKey] = data[0]
		}
	case len(data) > 1:
		for i := 0; i+1 < len(data); i += 2 {
			if key, ok := data[i].(string); ok {
				payload[key] = data[i+1]
			}
		}
	}
	return payload
}

// broadcastIPCEvent repassa um evento para as UIs conectadas via IPC
// (lado do serviço). É chamado pelos bridges que antes só faziam EmitEvent
// (Wails) — no modo serviço o Wails não existe e o evento vai pelo pipe.
func (a *App) broadcastIPCEvent(name string, data ...any) {
	if a == nil || a.ipcServer == nil || a.ipcServer.ClientCount() == 0 {
		return
	}
	payload := buildIPCEventPayload(name, data...)
	// S3: persiste o destinatário do prompt no MESMO momento do broadcast. O
	// payload do evento também carrega sessionId/sid para a UI (auditoria e
	// prompt), sem quebrar o contrato antigo (campos extras são ignorados).
	//
	// A.3.5: quando o destinatário é CONHECIDO, entrega só às conexões
	// autenticadas daquela sessão. Se nenhuma casar (UI em outra sessão, canal
	// ainda não autenticado), cai para Broadcast — compatibilidade preservada.
	var target ipcAuthIdentity
	targetKnown := false
	if name == "notification:new" {
		if notificationID, _ := payload["id"].(string); strings.TrimSpace(notificationID) != "" {
			t, ok := a.ipcServer.currentNotificationTarget()
			ipcRememberNotificationTarget(notificationID, t, !ok)
			if ok {
				target, targetKnown = t, true
				payload["sessionId"] = t.SessionID
				if strings.TrimSpace(t.SID) != "" {
					payload["sid"] = t.SID
				}
			}
		}
	} else if name == "supportchat:invite" {
		if t, ok := a.ipcServer.currentNotificationTarget(); ok && t.Valid {
			target, targetKnown = t, true
		}
	}

	msg := NewIPCMessage(IPCMsgEvent, payload)
	sent := 0
	if targetKnown {
		sent = a.ipcServer.SendToSession(target.SessionID, msg)
	}
	if sent == 0 {
		a.ipcServer.Broadcast(msg)
	}
}

// ipcEventToFrontend desempacota o payload IPC {name, <campos>} no par
// (nome, dados) aceito por EmitEvent. Espelha buildIPCEventPayload: o mapa de
// campos volta como UM argumento (objeto para o frontend) e o valor cru sob
// ipcEventRawKey volta como o valor unico original.
//
// BUG CORRIGIDO: antes a UI reconstruia um slice plano [chave, valor, ...] e
// chamava EmitEvent(name, chave, valor, ...). No Wails v3 (EventManager.Emit),
// MAIS DE UM argumento faz com que Event.Data seja o SLICE - o frontend recebia
// um array e data.connected era sempre undefined/false. Resultado: chat com
// aviso de servidor offline, bolinha offline e suporte em somente-consulta
// mesmo com o servico/servidor no ar.
func ipcEventToFrontend(payload map[string]any) (string, []any) {
	name, _ := payload["name"].(string)
	if name == "" {
		return "", nil
	}
	if raw, ok := payload[ipcEventRawKey]; ok {
		return name, []any{raw}
	}
	data := make(map[string]any, len(payload))
	for k, v := range payload {
		if k == "name" || k == ipcEventRawKey {
			continue
		}
		data[k] = v
	}
	return name, []any{data}
}

// storeCompanionOnboarding guarda o último estado de onboarding recebido do
// serviço via snapshot IPC (companion mode). Nil-safe.
func (a *App) storeCompanionOnboarding(mode string, configured bool, message string) {
	if a == nil {
		return
	}
	st := map[string]interface{}{
		"mode":       strings.TrimSpace(mode),
		"configured": configured,
	}
	if msg := strings.TrimSpace(message); msg != "" {
		st["message"] = msg
	}
	a.companionOnboardingMu.Lock()
	a.companionOnboarding = st
	a.companionOnboardingMu.Unlock()
}

// getCompanionOnboarding devolve o último estado de onboarding do serviço
// (nil quando nada foi recebido ainda — ex.: serviço mais antigo sem os
// campos no snapshot). Nil-safe.
func (a *App) getCompanionOnboarding() map[string]interface{} {
	if a == nil {
		return nil
	}
	a.companionOnboardingMu.RLock()
	defer a.companionOnboardingMu.RUnlock()
	return a.companionOnboarding
}

// storeCompanionStatus guarda o último snapshot de conectividade recebido do
// serviço via IPC. É usado por GetAgentStatus() na UI companion (o agentConn
// local não roda lá), mantendo tray e página de status consistentes com o
// core que roda no serviço.
// lastPongAtUtc/pongStale alimentam o campo "Último ping do servidor" da
// página de Status — o pong global existe só no agentConn do serviço; sem
// esses campos no snapshot, a UI exibia "-" para sempre.
// Campos ausentes NÃO sobrescrevem o estado anterior: uma transição
// agent:connectivity carrega só connected/transport/reason e antes zerava o
// "Último ping do servidor" da página de Status até o próximo snapshot.
func (a *App) storeCompanionStatus(payload map[string]any) {
	if a == nil || payload == nil {
		return
	}
	connected, _ := payload["connected"].(bool)
	transportConnected, hasTransportConnected := payload["transportConnected"].(bool)
	if !hasTransportConnected {
		// Serviço antigo (sem o campo): o transporte era o próprio efetivo.
		transportConnected = connected
	}
	transport, _ := payload["transport"].(string)
	reason, _ := payload["reason"].(string)
	lastEvent, _ := payload["lastEvent"].(string)
	if strings.TrimSpace(reason) != "" {
		lastEvent = reason
	}

	a.companionStatusMu.Lock()
	defer a.companionStatusMu.Unlock()
	st := a.companionStatus
	if st == nil {
		st = &AgentStatus{}
		a.companionStatus = st
	}
	st.Connected = connected
	st.TransportConnected = transportConnected
	st.Transport = transport
	st.LastEvent = "snapshot do serviço via IPC"
	if connected {
		st.LastEvent = "conectado (via serviço)"
	}
	// Motivo detalhado enviado pelo serviço (reason/lastEvent do snapshot).
	if strings.TrimSpace(lastEvent) != "" {
		st.LastEvent = strings.TrimSpace(lastEvent)
	}
	// Último pong global do agentconn do serviço (formato RFC3339): só atualiza
	// quando o evento REALMENTE carrega o campo.
	if raw, ok := payload["lastGlobalPongAtUtc"]; ok {
		lastPongAtUtc, _ := raw.(string)
		st.LastGlobalPongAtUTC = strings.TrimSpace(lastPongAtUtc)
	}
	if raw, ok := payload["globalPongStale"]; ok {
		pongStale, _ := raw.(bool)
		st.GlobalPongStale = pongStale
	}
}

// startIPCClient inicia o cliente IPC da UI (companion mode) e envia o hello.
// Chamado no startup da UI quando IsServicePresent detectou o serviço.
func (a *App) startIPCClient() {
	a.ipcClient = NewIPCClient(
		func(msg IPCMessage) {
			switch msg.Type {
			case IPCMsgRemoteSession:
				// Comando de remote session encaminhado pelo serviço: executa NESTA
				// UI (sessão interativa do usuário). A captura de tela e o input
				// exigem um desktop; a sessão 0 do serviço não o tem (captura sem
				// frames e SendInput bloqueado por UIPI).
				a.handleCompanionRemoteSession(msg.Payload)

			case IPCMsgDiagnosticStacks:
				// FIX forense (20/10 20:37): o serviço detectou esta UI como zumbi
				// (viva, sem IPC). O read loop desta goroutine está VIVO mesmo com
				// a main thread presa — gravar as stacks aqui revela ONDE a main
				// thread travou (ex.: criação do WebView2, AttachThreadInput).
				// Em goroutine para nunca bloquear o read loop.
				go a.writeRuntimeStacksDump("pedido-servico")

			case IPCMsgEvent:
				// Repassa eventos do serviço (notification:new, agent:connectivity,
				// chat:question, ...) para o frontend da UI companion. O payload
				// IPC vem como {name, <campos do evento>}.
				name, eventData := ipcEventToFrontend(msg.Payload)
				if name == "" {
					return
				}
				// Toast nativo do app: no modo companion quem tem o servico nativo
				// de notificacoes do Wails e esta UI, entao o envio acontece aqui
				// (o frontend nao repete o toast quando a superficie e nativa).
				if name == "notification:new" {
					a.maybeSendCompanionNativeToast(eventData)
				}
				// Guarda o snapshot de conectividade para o tray/status Go-side
				// (GetAgentStatus) refletirem o core que roda no serviço.
				if name == "agent:status_snapshot" || name == "agent:connectivity" {
					a.storeCompanionStatus(msg.Payload)
					// Estado de onboarding do serviço (overlay de provisionamento/
					// aprovação). Campos ausentes (serviço antigo) → mantém o último.
					if rawMode, ok := msg.Payload["onboardingMode"]; ok {
						mode, _ := rawMode.(string)
						cfgd, _ := msg.Payload["onboardingConfigured"].(bool)
						msg2, _ := msg.Payload["onboardingMessage"].(string)
						a.storeCompanionOnboarding(mode, cfgd, msg2)
					}
					a.syncTrayVisualState()
					a.updateTrayMenu()
					a.updateTrayTooltip()
				}
				// Config de debug atualizada no serviço (SetConfig, segurança
				// remota, bootstrap): adota a config na cópia em memória da UI
				// para que serviços locais (ex.: loja de apps, suporte) e a
				// própria página de Debug deixem de estar stale até reiniciar.
				if name == "debug:config_updated" {
					// O serviço emite EmitEvent("debug:config_updated", "config", cfg):
					// os dados são o VALOR da chave "config", não o payload inteiro
					// (com "name"/"config" o unmarshal em debug.Config resultaria em
					// config ZERO e a UI perderia endpoint/token em memória).
					source := any(msg.Payload)
					if inner, ok := msg.Payload["config"]; ok {
						source = inner
					}
					if raw, err := json.Marshal(source); err == nil {
						var cfg DebugConfig
						if err := json.Unmarshal(raw, &cfg); err == nil && a.DebugSvc != nil {
							a.DebugSvc.AdoptExternalConfig(cfg)
							a.Logs.Append("[ipc] config de debug adotada do serviço (debug:config_updated)")
						}
					}
				}
				// Convite do chat de suporte: a janela é aberta AQUI, no Go, e não
				// só pelo handler de JS da janela principal. Antes, se a janela
				// principal do agent nunca tivesse sido criada (agent no tray,
				// criação preguiçosa) ou estivesse sem webview carregado, o evento
				// não tinha quem o escutasse e o convite simplesmente não aparecia —
				// só o ícone no tray. O handler de JS continua existindo (caminho
				// idempotente: a segunda abertura só traz a janela para a frente).
				if name == "supportchat:invite" {
					a.openSupportChatInviteFromIPC(msg.Payload)
				}
				// Sessão VIVA que o serviço conhece e esta UI não (aceite feito
				// enquanto a UI estava desconectada): adota na hora, sem esperar
				// a reconciliação (ver adoptSupportChatSession).
				if name == "supportchat:replay" {
					a.adoptSupportChatSession(supportChatField(msg.Payload, "sessionId"))
				}
				// Estado do chat de suporte vindo do SERVIÇO: o modo da janela
				// ("sempre no topo" só com convite pendente) é decidido aqui na
				// UI, que é quem tem a janela. O serviço não sabe do Wails, então
				// sem isto a janela do convite ficaria flutuando até o usuário
				// responder — ou perderia o topo no meio do atendimento.
				// BUG CORRIGIDO: o estado vindo do SERVIÇO era usado só para decidir o
				// modo da janela — o serviço local da UI (companion) continuava com o
				// SEU próprio estado. Quando a conversa era encerrada pelo servidor
				// (session-ended) só o SERVIÇO recebia o evento: a UI ficava em
				// "active" para sempre e o convite seguinte era recusado com
				// "já existe um chat de suporte ativo nesta máquina" — o cartão nem
				// aparecia (relato: "mando abrir o chat e a notificação não vem").
				//
				// O serviço é a AUTORIDADE do estado do chat (carimbo/expiração/
				// aceite passam por ele). A UI adota o status e a sessão ANTES de
				// decidir o modo da janela; o transcript local é preservado.
				if name == supportChatStateEventName {
					a.adoptSupportChatStateFromIPC(msg.Payload)
					a.syncSupportChatWindowFromIPCPayload(msg.Payload)
				}
				// Link REAL do canal publicado pelo SERVIÇO (o NATS não é
				// desta processo). A UI guarda o valor e o repassa à janela
				// do chat no evento supportchat:state — antes ela dizia "sem
				// conexão" só porque o próprio Connected era false.
				if active, known := supportChatLinkFromIPC(msg.Payload); known {
					a.setSupportChatLink(active)
				}
				a.EmitEvent(name, eventData...)
			default:
			}
		},
		func(connected bool) {
			state := "desconectado"
			if connected {
				state = "conectado"
			}
			a.Logs.Append("[ipc] " + state + " ao serviço")
			a.EmitEvent("service:ipc_state", map[string]any{"connected": connected})
			if connected {
				// Convite pendente + UI que acabou de (re)conectar: o evento IPC
				// original pode ter caído no intervalo da conexão (pipe ainda
				// subindo). Reapresenta o cartão e confirma no servidor se a
				// sessão continua pendente — ver
				// replaySupportChatInviteAfterUIReconnect.
				go a.replaySupportChatInviteAfterUIReconnect()
				// Adota a configuração do agente resolvida no serviço (clientId/
				// siteId): a UI companion nasce com AgentConfig zero e sem DB.
				go a.hydrateAgentConfigurationCompanion()
				// Reconexão: pede um snapshot imediato de conectividade para o
				// tray/status refletirem o estado real sem esperar o próximo
				// tick de 5s do CompanionController (evita indicador defasado
				// após cada reconexão do pipe).
				go func() {
					if err := a.ipcClient.Send(NewIPCMessage(IPCMsgStatus, nil)); err != nil {
						a.Logs.Append("[ipc] falha ao pedir snapshot pós-reconexão: " + err.Error())
					}
				}()
			}
		},
	)
	go a.ipcClient.RunConnectLoop()

	// Polling de status + fallback standalone via CompanionController
	// (PLANO_SEPARACAO_SERVICO_UI.md §0.4 — máquina de estados extraída para
	// struct testável). Pede snapshot de conectividade ao serviço a cada 5s;
	// falha contínua por 5min → assume core standalone.
	ticker := time.NewTicker(DefaultCompanionConfig().PollInterval)
	controller := NewDefaultCompanionController(companionTunerAdapter{a: a})
	go func() {
		defer ticker.Stop()
		controller.Run(a.ctx, ticker.C)
	}()
}

// M5: decideCompanionMode removida — a UI é SEMPRE interface (sem fallback
// standalone). O cliente IPC (RunConnectLoop) aguarda o serviço aparecer e o
// CompanionController cuida do estado "serviço ausente/voltou".
