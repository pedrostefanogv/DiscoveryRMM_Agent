package app

import (
	"os"
	"runtime"
	"strings"
	"time"
)

// StatusOverview provides a simplified health snapshot for the default status page.
type StatusOverview struct {
	Connected               bool      `json:"connected"`
	ConnectionLabel         string    `json:"connectionLabel"`
	Hostname                string    `json:"hostname"`
	Server                  string    `json:"server"`
	ConnectionType          string    `json:"connectionType"`
	AppVersion              string    `json:"appVersion"`
	OSName                  string    `json:"osName"`
	OSVersion               string    `json:"osVersion"`
	LastInventoryCollected  string    `json:"lastInventoryCollected"`
	RealtimeAvailable       bool      `json:"realtimeAvailable"`
	RealtimeNATSConnected   bool      `json:"realtimeNatsConnected"`
	RealtimeConnectedAgents int       `json:"realtimeConnectedAgents"`
	RealtimeMessage         string    `json:"realtimeMessage"`
	CheckedAtUTC            time.Time `json:"checkedAtUtc"`
	// Outbox offline queue backlog counts
	PendingCommandResults int `json:"pendingCommandResults"`
	PendingP2PTelemetry   int `json:"pendingP2pTelemetry"`
}

// GetStatusOverview returns a user-friendly status summary for the Status tab.
func (a *App) GetStatusOverview() StatusOverview {
	agent := a.GetAgentStatus()
	cfg := a.GetDebugConfig()
	out := StatusOverview{
		Connected:       agent.Connected,
		ConnectionLabel: "Offline",
		Hostname:        "Computador local",
		Server:          strings.TrimSpace(agent.Server),
		ConnectionType:  "NATS",
		AppVersion:      strings.TrimSpace(Version),
		OSName:          runtime.GOOS,
		OSVersion:       runtime.GOARCH,
		CheckedAtUTC:    time.Now().UTC(),
	}

	if strings.EqualFold(strings.TrimSpace(cfg.Scheme), "nats") {
		out.ConnectionType = "NATS"
	}

	if transport := strings.TrimSpace(agent.Transport); transport != "" {
		switch strings.ToLower(transport) {
		case "nats":
			out.ConnectionType = "NATS"
		case "nats-wss", "nats-ws":
			out.ConnectionType = "NATS WS"
		default:
			out.ConnectionType = transport
		}
	}

	if host, err := os.Hostname(); err == nil {
		host = strings.TrimSpace(host)
		if host != "" {
			out.Hostname = host
		}
	}

	if out.Connected {
		out.ConnectionLabel = "Online"
	}
	if out.AppVersion == "" {
		out.AppVersion = "dev"
	}

	if inv, ok := a.invCache.get(); ok {
		if host := strings.TrimSpace(inv.Hardware.Hostname); host != "" {
			out.Hostname = host
		}
		if name := strings.TrimSpace(inv.OS.Name); name != "" {
			out.OSName = name
		}
		versionParts := []string{}
		if version := strings.TrimSpace(inv.OS.Version); version != "" {
			versionParts = append(versionParts, version)
		}
		if build := strings.TrimSpace(inv.OS.Build); build != "" {
			versionParts = append(versionParts, "build "+build)
		}
		if arch := strings.TrimSpace(inv.OS.Architecture); arch != "" {
			versionParts = append(versionParts, arch)
		}
		if len(versionParts) > 0 {
			out.OSVersion = strings.Join(versionParts, " | ")
		}
		out.LastInventoryCollected = strings.TrimSpace(inv.CollectedAt)
	}

	rt, err := a.GetRealtimeStatus()
	if err != nil {
		applyRealtimeFallbackFromAgentStatus(&out, agent, err)
	} else {
		applyRealtimeStatus(&out, rt)
	}

	if a.db != nil {
		agentID := strings.TrimSpace(a.GetDebugConfig().AgentID)
		if agentID != "" {
			if n, err := a.db.CountPendingCommandResultOutbox(agentID); err == nil {
				out.PendingCommandResults = n
			}
			if n, err := a.db.CountPendingP2PTelemetryOutbox(agentID); err == nil {
				out.PendingP2PTelemetry = n
			}
		}
	}

	return out
}

func applyRealtimeStatus(out *StatusOverview, rt RealtimeStatus) {
	if out == nil {
		return
	}
	out.RealtimeAvailable = true
	out.RealtimeNATSConnected = rt.NATSConnected
	out.RealtimeConnectedAgents = rt.RealtimeConnectedAgents
	if rt.NATSConnected {
		out.RealtimeMessage = "Realtime operacional"
	} else {
		out.RealtimeMessage = "Realtime indisponivel no momento"
	}
}

func applyRealtimeFallbackFromAgentStatus(out *StatusOverview, agent AgentStatus, err error) {
	if out == nil || err == nil {
		return
	}
	out.RealtimeMessage = err.Error()
	if !isRealtimeUnauthorizedError(err) {
		return
	}
	if !agent.Connected {
		out.RealtimeMessage = "endpoint /api/v1/agent-auth/me/realtime/status nao autorizado para o token do agent"
		return
	}

	transport := strings.ToLower(strings.TrimSpace(agent.Transport))
	out.RealtimeAvailable = true
	out.RealtimeConnectedAgents = 1
	switch transport {
	case "nats", "nats-ws", "nats-wss":
		out.RealtimeNATSConnected = true
		out.RealtimeMessage = "sessao remota ativa via NATS; endpoint /api/v1/agent-auth/me/realtime/status indisponivel ou token rejeitado"
	default:
		out.RealtimeMessage = "sessao remota ativa; endpoint /api/v1/agent-auth/me/realtime/status indisponivel ou token rejeitado"
	}
}

func isRealtimeUnauthorizedError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "401") ||
		strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "autenticação necessária") ||
		strings.Contains(msg, "autenticacao necessaria")
}
