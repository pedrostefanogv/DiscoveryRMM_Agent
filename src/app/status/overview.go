package status

import (
	"strings"
	"time"
)

// Overview provides a simplified health snapshot for the default status page.
type Overview struct {
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
	AppCommit                 string    `json:"appCommit,omitempty"`
	BuildDateUTC              string    `json:"buildDateUtc,omitempty"`
	OSName                    string    `json:"osName"`
	OSVersion                 string    `json:"osVersion"`
	OSEdition                 string    `json:"osEdition,omitempty"`
	LastInventoryCollected    string    `json:"lastInventoryCollected"`
	RealtimeAvailable         bool      `json:"realtimeAvailable"`
	RealtimeNATSConnected     bool      `json:"realtimeNatsConnected"`
	RealtimeConnectedAgents   int       `json:"realtimeConnectedAgents"`
	RealtimeMessage           string    `json:"realtimeMessage"`
	CheckedAtUTC              time.Time `json:"checkedAtUtc"`
	// Outbox offline queue backlog counts
	PendingCommandResults int `json:"pendingCommandResults"`
	PendingP2PTelemetry   int `json:"pendingP2pTelemetry"`
	// Update do agente (self-update)
	UpdateCheckEnabled          bool   `json:"updateCheckEnabled"`
	UpdateCheckInProgress       bool   `json:"updateCheckInProgress"`
	LastUpdateCheckAtUTC        string `json:"lastUpdateCheckAtUtc,omitempty"`
	UpdateLastError             string `json:"updateLastError,omitempty"`
	UpdateLastInstallerExitCode int32  `json:"updateLastInstallerExitCode"`
	UpdatePendingTargetVersion  string `json:"updatePendingTargetVersion,omitempty"`
	// Estado de adiamento: update pronto mas aguardando janela minimizada/oculta.
	UpdateDeferred         bool   `json:"updateDeferred"`
	UpdateDeferredReason   string `json:"updateDeferredReason,omitempty"`
	UpdateDeferredSinceUTC string `json:"updateDeferredSinceUtc,omitempty"`
	// Contadores de telemetria do self-update
	UpdateDownloadOKCount      int64 `json:"updateDownloadOkCount"`
	UpdateLaunchOKCount        int64 `json:"updateLaunchOkCount"`
	UpdateLaunchFailCount      int64 `json:"updateLaunchFailCount"`
	UpdateInstallCompleteCount int64 `json:"updateInstallCompleteCount"`
}

// RealtimeStatus é uma visão mínima do status realtime.
type RealtimeStatus struct {
	NATSConnected           bool
	RealtimeConnectedAgents int
}

// AgentStatus é uma visão mínima do status do agente.
type AgentStatus struct {
	Connected                  bool
	TransportConnected         bool
	OnlineReason               string
	Server                     string
	Transport                  string
	LastGlobalPongAtUTC        string
	GlobalPongStale            bool
	NonCriticalBackoffUntilUTC string
	NonCriticalBackoffReason   string
}

// ApplyRealtimeStatus aplica o status realtime ao overview.
func ApplyRealtimeStatus(out *Overview, rt RealtimeStatus) {
	if out == nil {
		return
	}
	out.RealtimeAvailable = true
	out.RealtimeNATSConnected = rt.NATSConnected
	out.RealtimeConnectedAgents = rt.RealtimeConnectedAgents
	if rt.NATSConnected {
		out.RealtimeMessage = "Realtime operacional"
	} else {
		out.RealtimeMessage = "Realtime indisponível no momento"
	}
}

// ApplyRealtimeFallbackFromAgentStatus aplica o fallback de realtime a partir do status do agente.
func ApplyRealtimeFallbackFromAgentStatus(out *Overview, agent AgentStatus, err error) {
	if out == nil || err == nil {
		return
	}
	out.RealtimeMessage = err.Error()
	if !IsRealtimeUnauthorizedError(err) {
		return
	}
	transportConnected := agent.TransportConnected || agent.Connected
	if !transportConnected {
		out.RealtimeMessage = "endpoint /api/v1/agent-auth/me/realtime/status não autorizado para o token do agent"
		return
	}

	transport := strings.ToLower(strings.TrimSpace(agent.Transport))
	out.RealtimeAvailable = true
	out.RealtimeConnectedAgents = 1
	switch transport {
	case "nats", "nats-ws", "nats-wss":
		out.RealtimeNATSConnected = true
		out.RealtimeMessage = "sessão remota ativa via NATS; endpoint /api/v1/agent-auth/me/realtime/status indisponível ou token rejeitado"
	default:
		out.RealtimeMessage = "sessao remota ativa; endpoint /api/v1/agent-auth/me/realtime/status indisponivel ou token rejeitado"
	}
}

// IsRealtimeUnauthorizedError verifica se o erro é de não autorização.
func IsRealtimeUnauthorizedError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "401") ||
		strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "autenticação necessária") ||
		strings.Contains(msg, "autenticacao necessaria")
}
