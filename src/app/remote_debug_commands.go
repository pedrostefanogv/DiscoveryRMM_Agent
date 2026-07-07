package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
		return fmt.Errorf("app indisponivel")
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
			return NotificationDispatchRequest{}, fmt.Errorf("falha ao serializar payload de notificacao: %w", err)
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

	if isPowerActionCommandType(cmdType) {
		pp := parsePowerCommandPayload(payload)
		action := "restart"
		if cmdType == "shutdown" {
			action = "shutdown"
		}
		if pp.DelaySeconds <= 0 {
			pp.DelaySeconds = 15
		}

		// Exibe prompt PSADT antes de executar a acao no sistema.
		//   force=true:  BalloonTip informativo (nao-bloqueante).
		//   force=false: Dialog Yes/No — usuario decide.
		decision, err := a.showPowerActionWarning(parent, action, pp.DelaySeconds, pp.Force, pp.Message)
		if err != nil {
			a.logs.append(fmt.Sprintf("[agent] %s-prompt erro ao exibir aviso: %v", action, err))
		}
		if decision != "proceed" {
			a.logs.append(fmt.Sprintf("[agent] %s-prompt usuario adiou — retornando sem agendar shutdown", action))
			return true, 0, fmt.Sprintf("%s adiado pelo usuario", action), ""
		}

		// No modo forcado com balloon, o shutdown.exe usa o delay completo
		// porque o balloon e nao-bloqueante (nao fez contagem regressiva).
		// No modo nao-forcado, o usuario ja decidiu via Dialog, entao
		// o shutdown pode usar o delay configurado normalmente.
		exitCode, output, errText := a.executeSystemPowerAction(parent, action, pp.DelaySeconds, pp.Force, pp.Message)
		return true, exitCode, output, errText
	}

	if a == nil || a.remoteDebug == nil {
		return false, 0, "", ""
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
