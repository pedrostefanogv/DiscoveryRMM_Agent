package app

import (
	"net"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// StatusOverview provides a simplified health snapshot for the default status page.
type StatusOverview struct {
	Connected                 bool      `json:"connected"`
	TransportConnected        bool      `json:"transportConnected"`
	ConnectionLabel           string    `json:"connectionLabel"`
	OnlineReason              string    `json:"onlineReason,omitempty"`
	Hostname                  string    `json:"hostname"`
	Server                    string    `json:"server"`
	ConnectionType            string    `json:"connectionType"`
	LastGlobalPongAtUTC       string    `json:"lastGlobalPongAtUtc,omitempty"`
	GlobalPongStale           bool      `json:"globalPongStale"`
	NonCriticalDeferred       bool      `json:"nonCriticalDeferred"`
	NonCriticalDeferredUntil  string    `json:"nonCriticalDeferredUntilUtc,omitempty"`
	NonCriticalDeferredReason string    `json:"nonCriticalDeferredReason,omitempty"`
	AppVersion                string    `json:"appVersion"`
	OSName                    string    `json:"osName"`
	OSVersion                 string    `json:"osVersion"`
	LastInventoryCollected    string    `json:"lastInventoryCollected"`
	RealtimeAvailable         bool      `json:"realtimeAvailable"`
	RealtimeNATSConnected     bool      `json:"realtimeNatsConnected"`
	RealtimeConnectedAgents   int       `json:"realtimeConnectedAgents"`
	RealtimeMessage           string    `json:"realtimeMessage"`
	CheckedAtUTC              time.Time `json:"checkedAtUtc"`
	// Outbox offline queue backlog counts
	PendingCommandResults int `json:"pendingCommandResults"`
	PendingP2PTelemetry   int `json:"pendingP2pTelemetry"`
}

// GetStatusOverview returns a user-friendly status summary for the Status tab.
func (a *App) GetStatusOverview() StatusOverview {
	agent := a.GetAgentStatus()
	cfg := a.GetDebugConfig()
	out := StatusOverview{
		Connected:           agent.Connected,
		TransportConnected:  agent.TransportConnected,
		ConnectionLabel:     "Offline",
		OnlineReason:        strings.TrimSpace(agent.OnlineReason),
		Hostname:            "Computador local",
		Server:              normalizeStatusServer(firstStatusServerCandidate(agent.Server, cfg)),
		ConnectionType:      resolveStatusConnectionType(agent.Transport, cfg),
		LastGlobalPongAtUTC: strings.TrimSpace(agent.LastGlobalPongAtUTC),
		GlobalPongStale:     agent.GlobalPongStale,
		AppVersion:          strings.TrimSpace(Version),
		OSName:              runtime.GOOS,
		OSVersion:           runtime.GOARCH,
		CheckedAtUTC:        time.Now().UTC(),
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

	if until := parseRFC3339Time(strings.TrimSpace(agent.NonCriticalBackoffUntilUTC)); !until.IsZero() && until.After(time.Now().UTC()) {
		out.NonCriticalDeferred = true
		out.NonCriticalDeferredUntil = until.UTC().Format(time.RFC3339)
		out.NonCriticalDeferredReason = strings.TrimSpace(agent.NonCriticalBackoffReason)
	}
	if !out.NonCriticalDeferred {
		if until, deferred, reason := a.nonCriticalBackoffStatus(); deferred {
			out.NonCriticalDeferred = true
			out.NonCriticalDeferredUntil = until.UTC().Format(time.RFC3339)
			out.NonCriticalDeferredReason = reason
		}
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

func firstStatusServerCandidate(agentServer string, cfg DebugConfig) string {
	if server := strings.TrimSpace(agentServer); server != "" {
		return server
	}
	for _, candidate := range []string{cfg.Server, cfg.NatsWsServer, cfg.NatsServer, cfg.ApiServer} {
		if server := strings.TrimSpace(candidate); server != "" {
			return server
		}
	}
	return ""
}

func resolveStatusConnectionType(transport string, cfg DebugConfig) string {
	if mapped := mapStatusTransportConnectionType(transport); mapped != "" {
		return mapped
	}

	if strings.TrimSpace(cfg.NatsWsServer) != "" && strings.TrimSpace(cfg.NatsServer) == "" {
		return "wss"
	}
	if strings.EqualFold(strings.TrimSpace(cfg.Scheme), "nats") || strings.TrimSpace(cfg.NatsServer) != "" {
		return "nats"
	}
	if strings.TrimSpace(cfg.NatsWsServer) != "" {
		return "wss"
	}

	return "-"
}

func mapStatusTransportConnectionType(transport string) string {
	normalized := strings.ToLower(strings.TrimSpace(transport))
	switch normalized {
	case "":
		return ""
	case "nats":
		return "nats"
	case "nats-ws", "nats-wss", "ws", "wss":
		return "wss"
	default:
		if strings.Contains(normalized, "ws") {
			return "wss"
		}
		if strings.Contains(normalized, "nats") {
			return "nats"
		}
		return normalized
	}
}

func normalizeStatusServer(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}

	if strings.Contains(value, "://") {
		if parsed, err := url.Parse(value); err == nil && strings.TrimSpace(parsed.Host) != "" {
			value = strings.TrimSpace(parsed.Host)
		}
	} else if strings.ContainsAny(value, "/?") {
		if parsed, err := url.Parse("//" + value); err == nil && strings.TrimSpace(parsed.Host) != "" {
			value = strings.TrimSpace(parsed.Host)
		}
	}

	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	} else if host, ok := splitHostAndNumericPort(value); ok {
		value = host
	}

	value = strings.TrimSpace(strings.Trim(value, "[]"))
	if value == "" {
		return strings.TrimSpace(raw)
	}
	return value
}

func splitHostAndNumericPort(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	idx := strings.LastIndex(trimmed, ":")
	if idx <= 0 || idx >= len(trimmed)-1 {
		return "", false
	}

	hostPart := strings.TrimSpace(trimmed[:idx])
	portPart := strings.TrimSpace(trimmed[idx+1:])
	if hostPart == "" || portPart == "" {
		return "", false
	}
	if strings.Contains(hostPart, ":") {
		return "", false
	}
	if _, err := strconv.Atoi(portPart); err != nil {
		return "", false
	}

	return hostPart, true
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
	transportConnected := agent.TransportConnected || agent.Connected
	if !transportConnected {
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
