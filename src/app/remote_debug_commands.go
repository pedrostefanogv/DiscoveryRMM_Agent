package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"discovery/app/core/remotedebug"
)

// handleAgentRuntimeCommand é o router central de comandos recebidos do servidor
// (via NATS/agentconn). Cada domínio é delegado para seu handler específico:
//   - update/selfupdate        → agent_commands_update.go
//   - notification/notify      → agent_commands_notification.go
//   - restart/reboot/shutdown  → agent_commands_power.go
//   - remote session           → agent_commands_remotesession.go
//   - remote debug             → core/remotedebug
//   - nats.reconnect           → agent_commands_natsreconnect.go (transferência de site)
//
// Retorna (handled, exitCode, output, errText) no contrato do agentconn.
func (a *App) handleAgentRuntimeCommand(parent context.Context, cmdType string, payload any) (bool, int, string, string) {
	cmdType = strings.ToLower(strings.TrimSpace(cmdType))
	a.logs.append(fmt.Sprintf("[cmd] recebido: cmdType=%q payload=%v", cmdType, remotedebug.TruncatePayloadForLog(payload)))

	if cmdType == "nats.reconnect" {
		return a.handleNatsReconnectCommand(parent, payload)
	}

	if cmdType == "update" || cmdType == "selfupdate" {
		if a.selfUpdater != nil {
			updateCmd, parseErr := parseAgentUpdateCommand(payload)
			if parseErr != nil {
				a.logs.append(fmt.Sprintf("[selfupdate] erro ao parsear payload: %v — fallback para check forcado", parseErr))
			}

			// Se o servidor enviou action=install com URL direta, faz download e instala imediatamente.
			if updateCmd.Action == "install" && updateCmd.DownloadURL() != "" {
				a.logs.append(fmt.Sprintf("[selfupdate] install direto: version=%s url=%s", updateCmd.VersionValue(), updateCmd.DownloadURL()))
				if err := a.selfUpdater.InstallFromURL(parent, updateCmd.VersionValue(), updateCmd.DownloadURL()); err != nil {
					return true, 1, "", fmt.Sprintf("self-update install direto falhou: %v", err)
				}
				return true, 0, "self-update install direto iniciado com sucesso", ""
			}

			// Fallback: check-update ou install sem URL → usa manifest da API.
			a.logs.append("[selfupdate] comando update recebido via NATS — iniciando check forcado")
			if err := a.selfUpdater.CheckAndUpdate(parent, true); err != nil {
				return true, 1, "", fmt.Sprintf("self-update falhou: %v", err)
			}
			return true, 0, "self-update iniciado com sucesso", ""
		}
		// selfUpdater não inicializado: cai no bloco legacy abaixo
	}

	if isPsadtAlertCommandType(cmdType) {
		p, err := parsePsadtAlertPayload(payload)
		if err != nil {
			return true, 2, "", err.Error()
		}
		exitCode, output, errText := a.handlePsadtAlert(parent, p)
		return true, exitCode, output, errText
	}

	if isNotificationDispatchCommandType(cmdType) {
		req, err := parseNotificationDispatchPayload(payload)
		if err != nil {
			return true, 2, "", err.Error()
		}
		resp := a.DispatchNotification(req)
		body, _ := json.Marshal(resp)
		switch resp.Result {
		case "approved":
			return true, 0, string(body), ""
		case "denied":
			return true, 10, string(body), "usuario negou a notificacao"
		case "timeout_policy_applied":
			return true, 124, string(body), "timeout de confirmacao"
		default:
			if resp.Accepted {
				return true, 0, string(body), ""
			}
			return true, 1, string(body), strings.TrimSpace(resp.Message)
		}
	}

	if isAgentUpdateCommandType(cmdType) {
		updateCmd, parseErr := parseAgentUpdateCommand(payload)
		if parseErr != nil {
			action, _, _ := parseAgentUpdatePayload(payload)
			updateCmd = agentUpdateCommand{Action: action}
		}

		switch updateCmd.Action {
		case "install":
			if a.selfUpdater == nil {
				return true, 2, "", "self-updater nao inicializado"
			}
			if updateCmd.DownloadURL() == "" {
				// Sem URL: tenta via manifest
				a.logs.append("[selfupdate] install sem URL — usando manifest da API")
				if err := a.selfUpdater.CheckAndUpdate(parent, true); err != nil {
					return true, 1, "", fmt.Sprintf("self-update falhou: %v", err)
				}
				return true, 0, "self-update iniciado com sucesso (via manifest)", ""
			}
			a.logs.append(fmt.Sprintf("[selfupdate] install direto (alias): version=%s url=%s", updateCmd.VersionValue(), updateCmd.DownloadURL()))
			if err := a.selfUpdater.InstallFromURL(parent, updateCmd.VersionValue(), updateCmd.DownloadURL()); err != nil {
				return true, 1, "", fmt.Sprintf("self-update install direto falhou: %v", err)
			}
			return true, 0, "self-update install direto iniciado com sucesso", ""
		case "", "check-update", "force-check":
			if err := a.requestAgentUpdateCheck(parent, "command:"+updateCmd.Action); err != nil {
				return true, 1, "", err.Error()
			}
			return true, 0, "self-update check solicitado", ""
		default:
			return true, 2, "", "acao de update nao suportada"
		}
	}

	// ── Power Actions: restart / shutdown ──
	// Fluxo revisado (2026-07-17):
	//
	//   FORCE (force=true):
	//     1. showForceRestartBalloon → PSADT BalloonTip Warning (não-bloqueante)
	//     2. Aguarda delaySeconds no agent (permite abort com shutdown /a)
	//     3. executeSystemPowerAction → shutdown.exe /r /t 0 /f (imediato)
	//
	//   NORMAL (force=false, com adiamento):
	//     1. showDeferrableRestartPrompt → PSADT ShowDialogBox YesNo
	//        - Yes / timeout → restart agora
	//        - No → adiar (scheduleDeferredRestart)
	//     2. Se restart_now: executeSystemPowerAction
	//     3. Se defer: agenda timer para re-exibição após deferMinutes
	//     4. Se PSADT indisponível: fallback para DispatchNotification
	//
	if isPowerActionCommandType(cmdType) {
		pp := parsePowerCommandPayload(payload)
		action := "restart"
		if cmdType == "shutdown" {
			action = "shutdown"
		}
		if pp.DelaySeconds <= 0 {
			if pp.Force {
				pp.DelaySeconds = 60
			} else {
				pp.DelaySeconds = 300 // normal: 5 min para usuário reagir
			}
		}
		// Defaults de defer
		if pp.DeferMinutes <= 0 {
			pp.DeferMinutes = 60
		}
		if pp.MaxDefers <= 0 {
			pp.MaxDefers = 3
		}

		// Cancela qualquer deferred restart pendente se receber novo comando
		a.cancelDeferredRestart()

		if pp.Force {
			// ── FORCE: balloon informativo + delay + shutdown imediato ──
			a.logs.append(fmt.Sprintf("[agent] %s-action [FORCE] delay=%ds force=true — modo balloon", action, pp.DelaySeconds))
			a.showForceRestartBalloon(action, pp.DelaySeconds, pp.Message)

			// Aguarda o delay no agent para dar tempo do usuário ver o balloon
			// e potencialmente salvar trabalho.
			select {
			case <-time.After(time.Duration(pp.DelaySeconds) * time.Second):
			case <-parent.Done():
				return true, 1, "", "contexto cancelado durante delay de restart forçado"
			}

			exitCode, output, errText := a.executeSystemPowerAction(parent, action, 0, true, pp.Message)
			return true, exitCode, output, errText
		}

		// ── NORMAL: diálogo com opção de adiar ──
		result := a.showDeferrableRestartPrompt(action, pp.DelaySeconds, pp.Message, pp.DeferMinutes)
		a.logs.append(fmt.Sprintf("[agent] %s-action [NORMAL] psadt-result=%s", action, result))

		switch result {
		case "restart_now":
			exitCode, output, errText := a.executeSystemPowerAction(parent, action, pp.DelaySeconds, false, pp.Message)
			return true, exitCode, output, errText

		case "defer":
			a.scheduleDeferredRestart(action, pp)
			return true, 0, fmt.Sprintf("%s adiado pelo usuário (defer=%d/%d, %dmin)", action, 1, pp.MaxDefers, pp.DeferMinutes), ""

		default:
			// "fallback" — PSADT indisponível, usar DispatchNotification
			a.logs.append(fmt.Sprintf("[agent] %s-action [FALLBACK] PSADT indisponível — usando DispatchNotification", action))
			// Timeout mínimo de 60s para o fallback de confirmação,
			// independente do delaySeconds recebido do servidor.
			notifTimeout := pp.DelaySeconds
			if notifTimeout < 60 {
				notifTimeout = 60
			}
			notifResp := a.DispatchNotification(NotificationDispatchRequest{
				NotificationID: fmt.Sprintf("restart-%d", time.Now().UnixNano()),
				Title:          "Reinicialização Necessária",
				Message:        pp.Message,
				Mode:           "require_confirmation",
				Severity:       "high",
				EventType:      "system_restart",
				Layout:         "modal",
				TimeoutSeconds: notifTimeout,
			})

			if notifResp.Result == "approved" {
				exitCode, output, errText := a.executeSystemPowerAction(parent, action, pp.DelaySeconds, false, pp.Message)
				return true, exitCode, output, errText
			}
			return true, 10, notifResp.Message, "usuário negou a reinicialização"
		}
	}

	if a == nil || a.remoteDebug == nil {
		return false, 0, "", ""
	}

	// ── Remote Session Commands (acesso remoto nativo) ──
	if isRemoteSessionCommandType(cmdType) {
		if a.remoteSessionMgr == nil {
			log.Printf("[remote-session] ERRO: remoteSessionMgr nil — nao inicializado\n")
			return true, 1, "", "remote session manager nao inicializado"
		}
		parsedPayload := parseAnyMap(payload)
		if parsedPayload == nil {
			log.Printf("[remote-session] ERRO: parseAnyMap retornou nil para cmdType=%s\n", cmdType)
			return true, 1, "", "remotesession: payload invalido (nao eh JSON object)"
		}
		sid, _ := parsedPayload["sessionId"].(string)
		act, _ := parsedPayload["action"].(string)
		log.Printf("[remote-session] dispatch: cmdType=%s sessionId=%s action=%s\n",
			cmdType, sid, act)
		handled, errMsg := a.remoteSessionMgr.HandleCommand(parent, parsedPayload)
		if handled {
			if errMsg != "" {
				log.Printf("[remote-session] comando processado com mensagem: %s\n", errMsg)
			}
			return true, 0, "ok", errMsg
		}
		// Retorna handled=true mesmo em caso de erro (ex: payload string em vez de map,
		// action ausente ou desconhecida) para evitar que o payload JSON seja executado
		// como comando shell via cmd /C no fallback de executeCommand.
		return true, 1, "", "remotesession: " + errMsg
	}

	// Delega comandos de inventário sob demanda (SystemInfo)
	if cmdType == "systeminfo" {
		return a.handleSystemInfoCommand(parent, payload)
	}

	return a.remoteDebug.HandleCommand(parent, cmdType, payload)
}

func (a *App) onAgentCommandOutput(cmdType, output, errText string) {
	if a == nil || a.remoteDebug == nil {
		return
	}
	a.remoteDebug.OnCommandOutput(cmdType, output, errText)
}
