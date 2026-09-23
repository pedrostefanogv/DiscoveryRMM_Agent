package app

import (
	"context"
	"fmt"
	"strings"

	"discovery/app/agentcommands"
)

// handleSystemInfoCommand processes SystemInfo command payloads.
// Supported operations:
//   - "force-sync": full inventory + software sync (existing)
//   - "refresh-on-demand": selective collection per flags
func (a *App) handleSystemInfoCommand(ctx context.Context, payload any) (bool, int, string, string) {
	payloadJSON, err := agentcommands.NormalizePayloadJSON(payload)
	if err != nil {
		return true, 1, "", "payload systeminfo invalido: " + err.Error()
	}

	operation := agentcommands.GetStringField(payloadJSON, "Operation")
	operation = strings.ToLower(strings.TrimSpace(operation))

	a.Logs.Append(fmt.Sprintf("[agent] processando systeminfo: operation=%s", operation))

	switch operation {
	case "refresh-on-demand":
		return a.handleRefreshOnDemand(ctx, payloadJSON)
	case "force-sync":
		return a.handleForceSync(ctx, payloadJSON)
	default:
		// Legacy: treat as full force-sync
		return a.handleForceSync(ctx, payloadJSON)
	}
}

// handleRefreshOnDemand collects only the data requested by the dashboard refresh buttons.
func (a *App) handleRefreshOnDemand(ctx context.Context, payloadJSON map[string]any) (bool, int, string, string) {
	flags := refreshOnDemandFlags{
		Ports:          agentcommands.GetBoolField(payloadJSON, "Ports"),
		Connections:    agentcommands.GetBoolField(payloadJSON, "Connections"),
		Software:       agentcommands.GetBoolField(payloadJSON, "Software"),
		Printers:       agentcommands.GetBoolField(payloadJSON, "Printers"),
		Hardware:       agentcommands.GetBoolField(payloadJSON, "Hardware"),
		StartupItems:   agentcommands.GetBoolField(payloadJSON, "StartupItems"),
		ScheduledTasks: agentcommands.GetBoolField(payloadJSON, "ScheduledTasks"),
	}

	// If nothing specific requested, default to ports + connections only
	hasAny := flags.Ports || flags.Connections || flags.Software || flags.Printers || flags.Hardware || flags.StartupItems || flags.ScheduledTasks
	if !hasAny {
		flags.Ports = true
		flags.Connections = true
	}

	var results []string

	if flags.Ports || flags.Connections {
		if err := a.requireInventorySvc(); err != nil {
			a.Logs.Append("[agent] refresh-on-demand: inventory não provisionado: " + err.Error())
			return true, 1, "", err.Error()
		}

		// Coleta e faz upload das conexões de rede para a API
		if err := a.SyncNetworkConnections(); err != nil {
			a.Logs.Append("[agent] refresh-on-demand: falha ao coletar/enviar conexoes de rede: " + err.Error())
			return true, 1, "", err.Error()
		}
		// Obtém os dados do cache para o log
		if cached, ok := a.InvCache.Get(); ok {
			results = append(results, fmt.Sprintf("ports=%d", len(cached.ListeningPorts)))
			results = append(results, fmt.Sprintf("connections=%d", len(cached.OpenSockets)))
		} else {
			results = append(results, "network=synced")
		}
	}

	if flags.StartupItems || flags.ScheduledTasks {
		// Itens de inicialização + tarefas agendadas: coleta e upload parcial
		// (somente as duas listas — o merge server-side preserva o restante).
		if err := a.SyncStartupAndScheduledTasks(); err != nil {
			a.Logs.Append("[agent] refresh-on-demand: falha ao sincronizar startup/tarefas: " + err.Error())
		} else {
			if flags.StartupItems {
				results = append(results, "startupItems=synced")
			}
			if flags.ScheduledTasks {
				results = append(results, "scheduledTasks=synced")
			}
		}
	}

	if flags.Software || flags.Printers || flags.Hardware {
		report, err := a.InventorySvc.RefreshInventory()
		if err != nil {
			a.Logs.Append("[agent] refresh-on-demand: falha ao coletar inventario: " + err.Error())
		} else {
			// Upload imediato ao servidor: o refresh manual do dashboard deve
			// refletir sem esperar o sync periódico. Inclui o inventário de
			// software com updates e Ids de winget/chocolatey.
			a.InventorySvc.SyncInventoryOnStartup(ctx, report)
			if flags.Software {
				results = append(results, fmt.Sprintf("software=%d", len(report.Software)))
			}
			if flags.Printers {
				results = append(results, fmt.Sprintf("printers=%d", len(report.Printers)))
			}
			if flags.Hardware {
				results = append(results, "hardware=ok")
			}
		}
	}

	a.Logs.Append("[agent] refresh-on-demand concluido: " + strings.Join(results, ", "))
	return true, 0, "refresh-on-demand: " + strings.Join(results, ", "), ""
}

// handleForceSync triggers a full inventory and software sync.
func (a *App) handleForceSync(ctx context.Context, payloadJSON map[string]any) (bool, int, string, string) {
	policies := agentcommands.GetBoolField(payloadJSON, "Policies")
	inventory := agentcommands.GetBoolField(payloadJSON, "Inventory")
	software := agentcommands.GetBoolField(payloadJSON, "Software")

	hasAny := policies || inventory || software
	if !hasAny {
		policies = true
		inventory = true
	}

	var results []string

	if inventory || software {
		report, err := a.InventorySvc.RefreshInventory()
		if err != nil {
			a.Logs.Append("[agent] force-sync: falha ao coletar inventario: " + err.Error())
			results = append(results, "inventory=failed")
		} else {
			// force-sync de fato envia ao servidor (inclui software com updates
			// e Ids de winget/chocolatey).
			a.InventorySvc.SyncInventoryOnStartup(ctx, report)
			if inventory {
				results = append(results, fmt.Sprintf("inventory=ok(ports=%d,conn=%d)", len(report.ListeningPorts), len(report.OpenSockets)))
			}
			if software {
				results = append(results, fmt.Sprintf("software=%d", len(report.Software)))
			}
		}
	}

	if policies {
		a.Logs.Append("[agent] force-sync: policies sync triggered")
		results = append(results, "policies=triggered")
	}

	a.Logs.Append("[agent] force-sync concluido: " + strings.Join(results, ", "))
	return true, 0, "force-sync: " + strings.Join(results, ", "), ""
}

// ── helpers ────────────────────────────────────────────────────────────────

type refreshOnDemandFlags struct {
	Ports          bool
	Connections    bool
	Software       bool
	Printers       bool
	Hardware       bool
	StartupItems   bool
	ScheduledTasks bool
}
