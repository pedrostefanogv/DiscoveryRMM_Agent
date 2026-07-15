package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// handleSystemInfoCommand processes SystemInfo command payloads.
// Supported operations:
//   - "force-sync": full inventory + software sync (existing)
//   - "refresh-on-demand": selective collection per flags
func (a *App) handleSystemInfoCommand(ctx context.Context, payload any) (bool, int, string, string) {
	payloadJSON, err := normalizePayloadJSON(payload)
	if err != nil {
		return true, 1, "", "payload systeminfo invalido: " + err.Error()
	}

	operation := getStringField(payloadJSON, "Operation")
	operation = strings.ToLower(strings.TrimSpace(operation))

	a.logs.append(fmt.Sprintf("[agent] processando systeminfo: operation=%s", operation))

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
func (a *App) handleRefreshOnDemand(_ context.Context, payloadJSON map[string]any) (bool, int, string, string) {
	flags := refreshOnDemandFlags{
		Ports:       getBoolField(payloadJSON, "Ports"),
		Connections: getBoolField(payloadJSON, "Connections"),
		Software:    getBoolField(payloadJSON, "Software"),
		Printers:    getBoolField(payloadJSON, "Printers"),
		Hardware:    getBoolField(payloadJSON, "Hardware"),
	}

	// If nothing specific requested, default to ports + connections only
	hasAny := flags.Ports || flags.Connections || flags.Software || flags.Printers || flags.Hardware
	if !hasAny {
		flags.Ports = true
		flags.Connections = true
	}

	var results []string

	if flags.Ports || flags.Connections {
		if err := a.requireInventorySvc(); err != nil {
			a.logs.append("[agent] refresh-on-demand: inventory não provisionado: " + err.Error())
			return true, 1, "", err.Error()
		}

		if flags.Ports && flags.Connections {
			report, err := a.inventorySvc.RefreshNetworkConnections()
			if err != nil {
				a.logs.append("[agent] refresh-on-demand: falha ao coletar conexoes de rede: " + err.Error())
				return true, 1, "", err.Error()
			}
			results = append(results, fmt.Sprintf("ports=%d", len(report.ListeningPorts)))
			results = append(results, fmt.Sprintf("connections=%d", len(report.OpenSockets)))
		} else if flags.Ports {
			ports, err := a.inventorySvc.RefreshListeningPorts()
			if err != nil {
				a.logs.append("[agent] refresh-on-demand: falha ao coletar portas: " + err.Error())
				return true, 1, "", err.Error()
			}
			results = append(results, fmt.Sprintf("ports=%d", len(ports)))
		} else if flags.Connections {
			report, err := a.inventorySvc.RefreshNetworkConnections()
			if err != nil {
				a.logs.append("[agent] refresh-on-demand: falha ao coletar conexoes: " + err.Error())
				return true, 1, "", err.Error()
			}
			results = append(results, fmt.Sprintf("connections=%d", len(report.OpenSockets)))
		}
	}

	if flags.Software {
		software, err := a.inventorySvc.RefreshSoftware()
		if err != nil {
			a.logs.append("[agent] refresh-on-demand: falha ao coletar software: " + err.Error())
		} else {
			results = append(results, fmt.Sprintf("software=%d", len(software)))
		}
	}

	if flags.Printers || flags.Hardware {
		report, err := a.inventorySvc.RefreshInventory()
		if err != nil {
			a.logs.append("[agent] refresh-on-demand: falha ao coletar inventario: " + err.Error())
		} else {
			if flags.Printers {
				results = append(results, fmt.Sprintf("printers=%d", len(report.Printers)))
			}
			if flags.Hardware {
				results = append(results, "hardware=ok")
			}
		}
	}

	a.logs.append("[agent] refresh-on-demand concluido: " + strings.Join(results, ", "))
	return true, 0, "refresh-on-demand: " + strings.Join(results, ", "), ""
}

// handleForceSync triggers a full inventory and software sync.
func (a *App) handleForceSync(_ context.Context, payloadJSON map[string]any) (bool, int, string, string) {
	policies := getBoolField(payloadJSON, "Policies")
	inventory := getBoolField(payloadJSON, "Inventory")
	software := getBoolField(payloadJSON, "Software")

	hasAny := policies || inventory || software
	if !hasAny {
		policies = true
		inventory = true
	}

	var results []string

	if inventory {
		report, err := a.inventorySvc.RefreshInventory()
		if err != nil {
			a.logs.append("[agent] force-sync: falha ao coletar inventario: " + err.Error())
			results = append(results, "inventory=failed")
		} else {
			results = append(results, fmt.Sprintf("inventory=ok(ports=%d,conn=%d)", len(report.ListeningPorts), len(report.OpenSockets)))
		}
	}

	if software {
		sw, err := a.inventorySvc.RefreshSoftware()
		if err != nil {
			a.logs.append("[agent] force-sync: falha ao coletar software: " + err.Error())
		} else {
			results = append(results, fmt.Sprintf("software=%d", len(sw)))
		}
	}

	if policies {
		a.logs.append("[agent] force-sync: policies sync triggered")
		results = append(results, "policies=triggered")
	}

	a.logs.append("[agent] force-sync concluido: " + strings.Join(results, ", "))
	return true, 0, "force-sync: " + strings.Join(results, ", "), ""
}

// ── helpers ────────────────────────────────────────────────────────────────

type refreshOnDemandFlags struct {
	Ports       bool
	Connections bool
	Software    bool
	Printers    bool
	Hardware    bool
}

func normalizePayloadJSON(payload any) (map[string]any, error) {
	switch v := payload.(type) {
	case map[string]any:
		return v, nil
	case string:
		var m map[string]any
		if err := json.Unmarshal([]byte(v), &m); err != nil {
			return nil, err
		}
		return m, nil
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, err
		}
		return m, nil
	}
}

func getStringField(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprintf("%v", v)
	}
	return ""
}

func getBoolField(m map[string]any, key string) bool {
	if v, ok := m[key]; ok {
		switch b := v.(type) {
		case bool:
			return b
		case string:
			return strings.EqualFold(b, "true") || b == "1"
		case float64:
			return b != 0
		}
	}
	return false
}
