package app

import (
	"context"
	"fmt"
	"strings"

	"discovery/app/agentcommands"
)

// ── Sensores (monitoramento) ──

// isRunSensorsCommandType informa se o cmdType pede coleta imediata de sensores.
func isRunSensorsCommandType(cmdType string) bool {
	return agentcommands.IsRunSensorsCommandType(cmdType)
}

// handleRunSensorsCommand trata o comando "runsensors":
//
//	payload { sensorIds?: [guid...], forceSync?: bool }
//
// forceSync=true sincroniza a policy de sensores ANTES de coletar (o operador
// clicou em "executar agora" logo depois de editar o sensor). O comando só
// AGENDA a coleta: o motor roda no loop de sensores, com semáforo e timeout —
// bloquear o canal de comandos por até 300s por sensor travaria os demais
// comandos do agente.
func (a *App) handleRunSensorsCommand(parent context.Context, payload any) (bool, int, string, string) {
	if a.AutomationSvc == nil {
		return true, 2, "", "servico de automacao indisponivel"
	}
	command := agentcommands.ParseRunSensorsPayload(payload)
	a.Logs.Append(fmt.Sprintf("[sensores] run-now recebido: sensores=%d forceSync=%t", len(command.SensorIDs), command.ForceSync))

	count, err := a.AutomationSvc.RunSensorsNow(parent, command.SensorIDs, command.ForceSync)
	if err != nil {
		a.Logs.Append(fmt.Sprintf("[sensores] run-now sem coleta: %v", err))
		return true, 1, "", err.Error()
	}
	message := fmt.Sprintf("coleta agendada para %d sensor(es)", count)
	if len(command.SensorIDs) > 0 && count < len(command.SensorIDs) {
		// Nem todo id pedido existe na policy local: reporta explicitamente para
		// o console não achar que todos rodaram.
		message = fmt.Sprintf("%s (%d de %d id(s) encontrados na policy local)", message, count, len(command.SensorIDs))
	}
	a.Logs.Append("[sensores] " + strings.TrimSpace(message))
	return true, 0, message, ""
}
