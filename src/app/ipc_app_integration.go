package app

// Integração IPC com o ciclo de vida da App (PLANO_AGENT_SERVICE_SYSTEM.md,
// Fase 2): handlers do lado do serviço (repassa notificações/eventos) e da
// UI (companion mode: handshake + fallback standalone).

import (
	"context"
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
			a.ipcServer.RespondTo(conn, NewIPCMessage(IPCMsgEvent, map[string]any{
				"name":      "agent:status_snapshot",
				"connected": agent.Connected,
				"transport": agent.Transport,
				"reason":    strings.TrimSpace(agent.OnlineReason),
				"lastEvent": strings.TrimSpace(agent.LastEvent),
				// Último ping do servidor (página de Status na UI companion): o
				// agentConn roda AQUI no serviço — a UI não tem esses campos localmente.
				"lastGlobalPongAtUtc": strings.TrimSpace(agent.LastGlobalPongAtUTC),
				"globalPongStale":     agent.GlobalPongStale,
				// Estado de onboarding (overlay de provisionamento/aprovação).
				"onboardingMode":       onb["mode"],
				"onboardingConfigured": onb["configured"],
				"onboardingMessage":    onb["message"],
			}))
		}
	case IPCMsgRemoteSession:
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
				go func() {
					if err := spawnRemoteSessionWorker(context.Background(), parsed); err != nil {
						a.Logs.Append("[ipc] erro ao spawnar remote session worker: " + err.Error())
					}
				}()
			}
		}
	case IPCMsgNotificationRespond:
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
		resp := a.handleIPCRequest(context.Background(), msg.Payload)
		if a.ipcServer != nil {
			reply := NewIPCMessage(IPCMsgResponse, resp)
			reply.CorrelationID = msg.CorrelationID
			a.ipcServer.RespondTo(conn, reply)
		}
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

// broadcastIPCEvent repassa um evento para as UIs conectadas via IPC
// (lado do serviço). É chamado pelos bridges que antes só faziam EmitEvent
// (Wails) — no modo serviço o Wails não existe e o evento vai pelo pipe.
func (a *App) broadcastIPCEvent(name string, data ...any) {
	if a == nil || a.ipcServer == nil || a.ipcServer.ClientCount() == 0 {
		return
	}
	payload := make(map[string]any, len(data)/2+1)
	payload["name"] = name
	for i := 0; i+1 < len(data); i += 2 {
		if key, ok := data[i].(string); ok {
			payload[key] = data[i+1]
		}
	}
	a.ipcServer.Broadcast(NewIPCMessage(IPCMsgEvent, payload))
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
func (a *App) storeCompanionStatus(connected bool, transport string, lastEvent string, lastPongAtUtc string, pongStale bool) {
	if a == nil {
		return
	}
	a.companionStatusMu.Lock()
	defer a.companionStatusMu.Unlock()
	st := a.companionStatus
	if st == nil {
		st = &AgentStatus{}
		a.companionStatus = st
	}
	st.Connected = connected
	st.TransportConnected = connected
	st.Transport = transport
	st.LastEvent = "snapshot do serviço via IPC"
	if connected {
		st.LastEvent = "conectado (via serviço)"
	}
	// Motivo detalhado enviado pelo serviço (reason/lastEvent do snapshot).
	if strings.TrimSpace(lastEvent) != "" {
		st.LastEvent = strings.TrimSpace(lastEvent)
	}
	// Último pong global do agentconn do serviço (formato RFC3339).
	st.LastGlobalPongAtUTC = strings.TrimSpace(lastPongAtUtc)
	st.GlobalPongStale = pongStale
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

			case IPCMsgEvent:
				// Repassa eventos do serviço (notification:new, agent:connectivity,
				// chat:question, ...) para o frontend da UI companion. O payload
				// IPC vem como {name, <campos do evento>}.
				name, _ := msg.Payload["name"].(string)
				if name == "" {
					return
				}
				// Guarda o snapshot de conectividade para o tray/status Go-side
				// (GetAgentStatus) refletirem o core que roda no serviço.
				if name == "agent:status_snapshot" || name == "agent:connectivity" {
					connected, _ := msg.Payload["connected"].(bool)
					transport, _ := msg.Payload["transport"].(string)
					reason, _ := msg.Payload["reason"].(string)
					lastEvent, _ := msg.Payload["lastEvent"].(string)
					lastPongAtUtc, _ := msg.Payload["lastGlobalPongAtUtc"].(string)
					pongStale, _ := msg.Payload["globalPongStale"].(bool)
					if strings.TrimSpace(reason) != "" {
						lastEvent = reason
					}
					a.storeCompanionStatus(connected, transport, lastEvent, lastPongAtUtc, pongStale)
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
				data := make([]any, 0, len(msg.Payload))
				for k, v := range msg.Payload {
					if k != "name" {
						data = append(data, k, v)
					}
				}
				a.EmitEvent(name, data...)
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
