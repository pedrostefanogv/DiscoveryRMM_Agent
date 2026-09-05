package app

// Integração IPC com o ciclo de vida da App (PLANO_AGENT_SERVICE_SYSTEM.md,
// Fase 2): handlers do lado do serviço (repassa notificações/eventos) e da
// UI (companion mode: handshake + fallback standalone).

import (
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
		a.logs.append("[ipc] handshake recebido da UI")
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
			a.ipcServer.RespondTo(conn, NewIPCMessage(IPCMsgEvent, map[string]any{
				"name":      "agent:status_snapshot",
				"connected": agent.Connected,
				"transport": agent.Transport,
			}))
		}
	case IPCMsgRemoteSession:
		// Remote session DEVE rodar na sessão interativa do usuário (a sessão 0
		// do serviço SYSTEM não tem desktop — captura falha e SendInput é
		// bloqueado por UIPI). Encaminha o comando à UI companion conectada.
		a.logs.append("[ipc] encaminhando remote session à UI (sessão interativa)")
		a.ipcServer.Broadcast(NewIPCMessage(IPCMsgRemoteSession, msg.Payload))
	case IPCMsgNotificationRespond:
		if a.handleIPCNotificationRespond(msg.Payload) {
			return
		}
		a.logs.append("[ipc] resposta de notificação sem payload válido")
	case IPCMsgCommandResult:
		a.logs.append("[ipc] resultado de comando interativo recebido da UI (encaminhamento é extensão futura)")
	default:
		log.Printf("[ipc] mensagem não tratada do tipo %s", msg.Type)
	}
}

// handleIPCNotificationRespond processa a resposta do usuário recebida via
// IPC (UI companion) e injeta no notificationSvc (pendingNotifyResult) —
// fecha o ciclo serviço→UI→resposta→NATS do plano (Fase 2).
func (a *App) handleIPCNotificationRespond(payload map[string]any) bool {
	if a == nil || a.notificationSvc == nil || payload == nil {
		return false
	}
	notificationID, _ := payload["notificationId"].(string)
	result, _ := payload["result"].(string)
	if strings.TrimSpace(notificationID) == "" || strings.TrimSpace(result) == "" {
		return false
	}
	ok := a.notificationSvc.Respond(notificationID, result)
	a.logs.append(fmt.Sprintf("[ipc] resposta de notificação %s -> %s (injetada=%t)", notificationID, result, ok))
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

// storeCompanionStatus guarda o último snapshot de conectividade recebido do
// serviço via IPC. É usado por GetAgentStatus() na UI companion (o agentConn
// local não roda lá), mantendo tray e página de status consistentes com o
// core que roda no serviço.
func (a *App) storeCompanionStatus(connected bool, transport string) {
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
					a.storeCompanionStatus(connected, transport)
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
			a.logs.append("[ipc] " + state + " ao serviço")
			a.EmitEvent("service:ipc_state", map[string]any{"connected": connected})
		},
	)
	go a.ipcClient.RunConnectLoop()

	// Polling de status: pede snapshot de conectividade ao serviço a cada 5s
	// (o core roda lá; o GetAgentStatus local da UI companion não o conhece).
	// O snapshot chega como evento "agent:status_snapshot" e é repassado ao
	// frontend pelo handler de eventos acima.
	//
	// Fallback de segurança: se o serviço sumir por tempo prolongado
	// (desinstalado/corrompido), a UI assume o core standalone após 5 min
	// de falhas contínuas — evita máquina sem agente por falha do serviço.
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		const maxFailures = 60 // 60 × 5s = 5 min
		failures := 0
		for {
			select {
			case <-a.ctx.Done():
				return
			case <-ticker.C:
				if err := a.ipcClient.Send(NewIPCMessage(IPCMsgStatus, nil)); err != nil {
					failures++
					if failures >= maxFailures {
						a.logs.append("[ipc] serviço ausente por 5min — assumindo core standalone (fallback)")
						a.EmitEvent("service:companion_lost", map[string]any{"reason": "service_unreachable"})
						a.runStagedStartup(a.ctx)
						return
					}
					continue
				}
				failures = 0
			}
		}
	}()
}

// decideCompanionMode determina se a UI deve rodar em modo companion
// (serviço ativo) ou standalone (fallback). Probe com timeout curto —
// pipe inexistente falha imediatamente no Windows; 500ms cobre o caso
// do serviço em startup (listener já criado antes do core).
func (a *App) decideCompanionMode() bool {
	if IsServicePresent(500 * time.Millisecond) {
		a.logs.append("[startup] serviço DiscoveryAgent ativo — modo companion (UI sem core)")
		return true
	}
	a.logs.append("[startup] serviço ausente — modo standalone (core completo na UI)")
	return false
}
