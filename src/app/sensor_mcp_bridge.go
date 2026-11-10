package app

// ─────────────────────────────────────────────────────────────────────────────
// Ponte MCP dos sensores (implementação do AppBridge.SensorDebugSnapshotForMCP).
//
// A tool MCP get_sensors (app/core/mcp/sensor_tool.go) precisa do RETRATO do
// motor de sensores deste agente para responder "quais sensores eu tenho, como
// estão e qual foi o histórico recente". Este arquivo entrega esse retrato.
//
// Por que aqui e não dentro do handler da tool: o motor de sensores é um
// detalhe da camada App (AutomationSvc) e o pacote mcp não pode alcançá-lo
// diretamente (não há App lá). O AppBridge é a fronteira, exatamente como nas
// demais tools.
//
// Reaproveita a MESMA cadeia do painel de debug (sensor_debug_commands.go):
// primeiro o RPC companion (em companion a UI não roda o loop de sensores — M5
// — e o motor de verdade está no serviço) e, se ele não responder, o motor
// local via SensorDebugSnapshotCore. Nada é reimplementado aqui: a coleta
// continua sendo do núcleo.
// ─────────────────────────────────────────────────────────────────────────────

import (
	"discovery/app/core/automation"
)

// SensorDebugSnapshotForMCP devolve o snapshot do motor de sensores para a tool
// MCP de LEITURA get_sensors.
//
// Contrato:
//   - NUNCA devolve erro: leitura de sensores é resposta legítima mesmo quando
//     não há sensores ou o motor ainda está subindo. O snapshot de fallback vem
//     com Sensors vazio e o motivo em Engine.LastError, que a tool usa para
//     compor a mensagem amigável (hasSensors=false).
//   - companion PRIMEIRO: em companion é o serviço que coleta (M5); ler só o
//     AutomationSvc local devolveria "0 sensores" para uma máquina que tem
//     sensores configurados — a mentira exata que o painel de debug já corrigiu.
//   - standalone (ou RPC mudo): motor local, via SensorDebugSnapshotCore.
//
// NÃO muda nada: é somente leitura, não exige canal admin e não entra em
// ipcKnownMutatingMethods. Em companion ela usa o canal de LEITURA do IPC
// (sensors:debug_snapshot), que já é servido sem token.
func (a *App) SensorDebugSnapshotForMCP() automation.SensorDebugSnapshot {
	if a == nil {
		return sensorDebugEmptySnapshot(false, sensorDebugCoreUnavailable)
	}
	if snapshot, ok := a.getSensorDebugSnapshotCompanion(); ok {
		// Mesma regra do painel: não sobrescrevemos Engine.LastError — o serviço
		// pode tê-lo preenchido por um motivo legítimo (backoff de sync).
		return snapshot
	}
	return a.SensorDebugSnapshotCore()
}
