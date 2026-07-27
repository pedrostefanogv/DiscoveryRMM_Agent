package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ── Agent Update Commands ──

func isAgentUpdateCommandType(cmdType string) bool {
	switch strings.ToLower(strings.TrimSpace(cmdType)) {
	case "10", "update", "agentupdate", "selfupdate", "self-update":
		return true
	default:
		return false
	}
}

func parseAgentUpdatePayload(payload any) (string, string, string) {
	if payload == nil {
		return "check-update", "", ""
	}
	switch typed := payload.(type) {
	case string:
		action := strings.ToLower(strings.TrimSpace(typed))
		if action == "" {
			return "check-update", "", ""
		}
		return action, "", ""
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return "", "", fmt.Sprintf("falha ao serializar payload de update: %v", err)
		}
		var parsed struct {
			Action  string `json:"action"`
			URL     string `json:"url"`     // URL direta do instalador (opcional)
			Version string `json:"version"` // versão alvo (opcional)
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return "", "", fmt.Sprintf("payload de update invalido: %v", err)
		}
		action := strings.ToLower(strings.TrimSpace(parsed.Action))
		if action == "" {
			action = "check-update"
		}
		return action, strings.TrimSpace(parsed.URL), strings.TrimSpace(parsed.Version)
	}
}

// agentUpdateCommand representa o payload completo de um comando de update
// enviado pelo servidor. Usado para extrair url/version para install direto.
// Version e URL usam *string para tolerar null vindo do servidor.
type agentUpdateCommand struct {
	Action  string  `json:"action"`
	Version *string `json:"version"`
	URL     *string `json:"url"`
}

func (c agentUpdateCommand) version() string {
	if c.Version != nil {
		return strings.TrimSpace(*c.Version)
	}
	return ""
}

func (c agentUpdateCommand) downloadURL() string {
	if c.URL != nil {
		return strings.TrimSpace(*c.URL)
	}
	return ""
}

func parseAgentUpdateCommand(payload any) (agentUpdateCommand, error) {
	if payload == nil {
		return agentUpdateCommand{Action: "check-update"}, nil
	}
	switch typed := payload.(type) {
	case string:
		var cmd agentUpdateCommand
		if err := json.Unmarshal([]byte(typed), &cmd); err != nil {
			return agentUpdateCommand{Action: "check-update"}, nil
		}
		cmd.Action = strings.ToLower(strings.TrimSpace(cmd.Action))
		if cmd.Action == "" {
			cmd.Action = "check-update"
		}
		return cmd, nil
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return agentUpdateCommand{}, fmt.Errorf("falha ao serializar payload de update: %w", err)
		}
		var cmd agentUpdateCommand
		if err := json.Unmarshal(raw, &cmd); err != nil {
			return agentUpdateCommand{}, fmt.Errorf("payload de update invalido: %w", err)
		}
		cmd.Action = strings.ToLower(strings.TrimSpace(cmd.Action))
		if cmd.Action == "" {
			cmd.Action = "check-update"
		}
		return cmd, nil
	}
}

func (a *App) requestAgentUpdateCheck(_ context.Context, source string) error {
	if a == nil {
		return fmt.Errorf("app indisponível")
	}
	source = strings.TrimSpace(source)
	if source == "" {
		source = "manual"
	}

	if a.selfUpdater != nil {
		select {
		case a.selfUpdaterCh <- false:
			a.logs.append("[selfupdate] check disparado via InvalidateCh: source=" + source)
			return nil
		default:
			a.logs.append("[selfupdate] InvalidateCh cheio, ignorando duplicado: source=" + source)
			return nil
		}
	}

	// Fallback para o legacy updateTrigger
	select {
	case a.updateTrigger <- struct{}{}:
		a.logs.append("[update] force-check de update disparado localmente: source=" + source)
		return nil
	default:
		return fmt.Errorf("update check ja pendente")
	}
}

// ── Notification Dispatch Commands ──

func isNotificationDispatchCommandType(cmdType string) bool {
	switch strings.ToLower(strings.TrimSpace(cmdType)) {
	case "notification", "notify", "notification_dispatch", "notification-dispatch":
		return true
	default:
		return false
	}
}

func parseNotificationDispatchPayload(payload any) (NotificationDispatchRequest, error) {
	if payload == nil {
		return NotificationDispatchRequest{}, nil
	}
	switch typed := payload.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return NotificationDispatchRequest{}, nil
		}
		var req NotificationDispatchRequest
		if err := json.Unmarshal([]byte(typed), &req); err != nil {
			return NotificationDispatchRequest{}, fmt.Errorf("payload de notificacao invalido: %w", err)
		}
		return req, nil
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return NotificationDispatchRequest{}, fmt.Errorf("falha ao serializar payload de notificação: %w", err)
		}
		var req NotificationDispatchRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return NotificationDispatchRequest{}, fmt.Errorf("payload de notificacao invalido: %w", err)
		}
		return req, nil
	}
}

// ── Power Action Commands ──

type powerCommandPayload struct {
	DelaySeconds int    `json:"delaySeconds"`
	Force        bool   `json:"force"`
	Message      string `json:"message"`
	DeferMinutes int    `json:"deferMinutes"` // minutos para adiar (default 60)
	MaxDefers    int    `json:"maxDefers"`    // máximo de adiamentos permitidos (default 3)
}

// isPowerActionCommandType checks whether cmdType is a restart/reboot/shutdown command.
func isPowerActionCommandType(cmdType string) bool {
	switch strings.ToLower(strings.TrimSpace(cmdType)) {
	case "restart", "reboot", "shutdown":
		return true
	default:
		return false
	}
}

// parsePowerCommandPayload extracts delaySeconds, force, and message from the raw payload.
// Aceita tanto map[string]any (JSON ja parseado) quanto string (JSON raw).
func parsePowerCommandPayload(payload any) powerCommandPayload {
	if payload == nil {
		return powerCommandPayload{}
	}

	toString := func(v any) string {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
		return ""
	}
	toBool := func(v any) bool {
		// Aceita bool nativo e string "true"/"false" (JSON strings comuns).
		if b, ok := v.(bool); ok {
			return b
		}
		if s, ok := v.(string); ok {
			s = strings.ToLower(strings.TrimSpace(s))
			return s == "true" || s == "1"
		}
		return false
	}
	toInt := func(v any) int {
		switch t := v.(type) {
		case float64:
			return int(t)
		case int:
			return t
		case int64:
			return int(t)
		case json.Number:
			n, err := t.Int64()
			if err != nil {
				return 0
			}
			return int(n)
		case string:
			// Cenario comum: numero serializado como string no JSON (ex: "30").
			trimmed := strings.TrimSpace(t)
			if trimmed == "" {
				return 0
			}
			var n int
			if _, err := fmt.Sscanf(trimmed, "%d", &n); err == nil {
				return n
			}
			return 0
		}
		return 0
	}

	// Primeiro, tenta payload como map[string]any (JSON ja parseado).
	m, ok := payload.(map[string]any)
	if !ok {
		// Fallback: payload pode ser uma string JSON (comum quando o servidor
		// envia o payload como JSON-encoded string dentro do envelope NATS).
		if s, ok := payload.(string); ok {
			s = strings.TrimSpace(s)
			if s != "" {
				var fallback map[string]any
				if err := json.Unmarshal([]byte(s), &fallback); err == nil {
					m = fallback
				}
			}
		}
		if m == nil {
			return powerCommandPayload{}
		}
	}

	return powerCommandPayload{
		DelaySeconds: toInt(m["delaySeconds"]),
		Force:        toBool(m["force"]),
		Message:      toString(m["message"]),
		DeferMinutes: toInt(m["deferMinutes"]),
		MaxDefers:    toInt(m["maxDefers"]),
	}
}

// ── Main Command Handler ──

func (a *App) handleAgentRuntimeCommand(parent context.Context, cmdType string, payload any) (bool, int, string, string) {
	cmdType = strings.ToLower(strings.TrimSpace(cmdType))
	a.logs.append(fmt.Sprintf("[cmd] recebido: cmdType=%q payload=%v", cmdType, truncatePayloadForLog(payload)))

	if cmdType == "update" || cmdType == "selfupdate" {
		if a.selfUpdater != nil {
			updateCmd, parseErr := parseAgentUpdateCommand(payload)
			if parseErr != nil {
				a.logs.append(fmt.Sprintf("[selfupdate] erro ao parsear payload: %v — fallback para check forcado", parseErr))
			}

			// Se o servidor enviou action=install com URL direta, faz download e instala imediatamente.
			if updateCmd.Action == "install" && updateCmd.downloadURL() != "" {
				a.logs.append(fmt.Sprintf("[selfupdate] install direto: version=%s url=%s", updateCmd.version(), updateCmd.downloadURL()))
				if err := a.selfUpdater.InstallFromURL(parent, updateCmd.version(), updateCmd.downloadURL()); err != nil {
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
			if updateCmd.downloadURL() == "" {
				// Sem URL: tenta via manifest
				a.logs.append("[selfupdate] install sem URL — usando manifest da API")
				if err := a.selfUpdater.CheckAndUpdate(parent, true); err != nil {
					return true, 1, "", fmt.Sprintf("self-update falhou: %v", err)
				}
				return true, 0, "self-update iniciado com sucesso (via manifest)", ""
			}
			a.logs.append(fmt.Sprintf("[selfupdate] install direto (alias): version=%s url=%s", updateCmd.version(), updateCmd.downloadURL()))
			if err := a.selfUpdater.InstallFromURL(parent, updateCmd.version(), updateCmd.downloadURL()); err != nil {
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
			return true, 1, "", "remote session manager nao inicializado"
		}
		handled, errMsg := a.remoteSessionMgr.HandleCommand(parent, parseAnyMap(payload))
		if handled {
			return true, 0, "ok", errMsg
		}
		return false, 0, "", errMsg
	}

	// Delega comandos de inventário sob demanda (SystemInfo)
	if cmdType == "systeminfo" {
		return a.handleSystemInfoCommand(parent, payload)
	}

	return a.remoteDebug.HandleCommand(parent, cmdType, payload)
// ── Remote Session Helpers ──

func isRemoteSessionCommandType(cmdType string) bool {
	switch strings.ToLower(strings.TrimSpace(cmdType)) {
	case "remotesessionstart", "remote_session_start", "remotesessionstop", "remote_session_stop",
		"remotesessionquality", "remote_session_quality",
		"recordingstart", "recording_start", "recordingstop", "recording_stop":
		return true
	default:
		return false
	}
}

func parseAnyMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func (a *App) onAgentCommandOutput(cmdType, output, errText string) {
	if a == nil || a.remoteDebug == nil {
		return
	}
	a.remoteDebug.OnCommandOutput(cmdType, output, errText)
}
