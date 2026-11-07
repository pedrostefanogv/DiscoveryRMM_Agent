package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"discovery/app/agentcommands"
	"discovery/app/core/sysctrl"
)

// ─────────────────────────────────────────────────────────────────────────────
// F7 — Controle remoto de serviços pelo console (start/stop/restart).
//
// Diferente de startupitem/scheduledtask, este comando pode DERRUBAR a máquina
// do cliente ou a autenticação de um domínio. Por isso a execução NÃO herda o
// gate de confirmação do MCP local (que exige o usuário na máquina): o guard de
// sysctrl é a única barreira, e ele é fail-closed.
//
// Ver core/sysctrl/service_guard_windows.go para as listas e a justificativa de
// cada serviço bloqueado.
// ─────────────────────────────────────────────────────────────────────────────

// cmdTypeServico é o nome do comando aceito no canal servidor→agente.
//
// Aliases seguem a convenção dos outros comandos do agente (sem separador,
// com underscore e com hífen) para tolerar o mapeamento do wire.
const (
	cmdTypeServiceControl = "servicecontrol"
)

// IsServiceControlCommandType verifica se o cmdType é o de controle de serviço.
//
// Cuidado deliberado: aceita SOMENTE "servicecontrol" e variantes com
// underscore/hífen. NÃO aceita "servicestart"/"servicestop" como comandos
// separados — isso duplicaria a superfície de ataque e permitiria que a ação
// fosse inferida do nome do comando em vez do campo "action" validado.
func IsServiceControlCommandType(cmdType string) bool {
	switch strings.ToLower(strings.TrimSpace(cmdType)) {
	case "servicecontrol", "service_control", "service-control":
		return true
	default:
		return false
	}
}

// handleServiceControlCommand processa o comando de controle de serviço.
//
// Payload esperado:
//
//	{"action":"start|stop|restart","name":"Spooler","requestedBy":"usuario@dominio"}
//
// Campos:
//   - action      (obrigatório) start | stop | restart
//   - name        (obrigatório) nome NT do serviço (ex.: Spooler, wuauserv)
//   - requestedBy (opcional)    quem pediu, para o log de auditoria
//   - reason      (opcional)    justificativa textual, para auditoria
//
// Códigos de saída devolvidos ao servidor:
//   - 0  execução autorizada e aplicada com sucesso
//   - 1  autorizado, mas o SCM recusou/falhou
//   - 2  payload inválido (campo obrigatório ausente/ação desconhecida)
//   - 13 recusado pela POLÍTICA (guard) — não é erro de execução
//
// O código 13 espelha o usado por executePackageAction para negação de
// política (core/automation/executor.go), mantendo o vocabulário do agente.
func (a *App) handleServiceControlCommand(ctx context.Context, payload any) (bool, int, string, string) {
	_ = ctx

	payloadJSON, err := agentcommands.NormalizePayloadJSON(payload)
	if err != nil {
		return true, 2, "", "payload servicecontrol inválido: " + err.Error()
	}

	rawAction := agentcommands.GetStringField(payloadJSON, "action")
	action, ok := sysctrl.ParseServiceAction(rawAction)
	if !ok {
		return true, 2, "", fmt.Sprintf("ação servicecontrol inválida (%q); use start, stop ou restart", strings.TrimSpace(rawAction))
	}

	name := strings.TrimSpace(agentcommands.GetStringField(payloadJSON, "name"))
	if name == "" {
		return true, 2, "", "campo 'name' é obrigatório"
	}

	// Quem pediu e por quê: entra no log de auditoria local do agente. Sem
	// isso, uma recusa (ou uma execução) não teria rastro de autoria.
	requestedBy := strings.TrimSpace(agentcommands.GetStringField(payloadJSON, "requestedBy"))
	if requestedBy == "" {
		requestedBy = "desconhecido"
	}
	reason := strings.TrimSpace(agentcommands.GetStringField(payloadJSON, "reason"))

	// ProductType VIVO, lido agora — não de um inventário possivelmente antigo.
	// Um DC promovido depois do último sync precisa ser tratado como DC já
	// neste comando (ver comentário em sysctrl.DetectProductType).
	productType := sysctrl.DetectProductType()

	a.Logs.Append(fmt.Sprintf(
		"[agent] servicecontrol AUDITORIA: solicitado por=%q servico=%q acao=%q productType=%q motivo=%q",
		requestedBy, name, string(action), productType, reason))

	result := sysctrl.ApplyServiceAction(name, action, productType)
	decision := result.Decision

	// ── RECUSADO PELA POLÍTICA ──────────────────────────────────────────────
	if !decision.Allowed {
		a.Logs.Append(fmt.Sprintf(
			"[agent] servicecontrol RECUSADO (auditoria): por=%q servico=%q acao=%q lista=%q motivo=%q",
			requestedBy, decision.ServiceName, string(action), decision.MatchedList, decision.Reason))

		body, _ := json.Marshal(map[string]any{
			"success":     false,
			"denied":      true,
			"service":     decision.ServiceName,
			"action":      string(action),
			"matchedList": decision.MatchedList,
			"reason":      decision.Reason,
			"requestedBy": requestedBy,
		})
		return true, 13, string(body), decision.Reason
	}

	// ── AUTORIZADO, MAS O SCM FALHOU ────────────────────────────────────────
	if result.Err != nil {
		a.Logs.Append(fmt.Sprintf(
			"[agent] servicecontrol FALHA (auditoria): por=%q servico=%q acao=%q erro=%v",
			requestedBy, decision.ServiceName, string(action), result.Err))
		return true, 1, "", result.Err.Error()
	}

	// ── EXECUTADO ───────────────────────────────────────────────────────────
	a.Logs.Append(fmt.Sprintf(
		"[agent] servicecontrol OK (auditoria): por=%q servico=%q acao=%q",
		requestedBy, decision.ServiceName, string(action)))

	body, _ := json.Marshal(map[string]any{
		"success":     true,
		"denied":      false,
		"service":     decision.ServiceName,
		"action":      string(action),
		"matchedList": decision.MatchedList,
		"requestedBy": requestedBy,
	})
	return true, 0, string(body), ""
}
