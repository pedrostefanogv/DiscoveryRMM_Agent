package app

import (
	"context"
	"fmt"
	"strings"
)

// GetDebugConfig returns the current debug configuration.
func (a *App) GetDebugConfig() DebugConfig {
	if a == nil || a.debugSvc == nil {
		return DebugConfig{}
	}
	return a.debugSvc.GetConfig()
}

// SetDebugConfig validates, stores and persists the debug connection settings.
func (a *App) SetDebugConfig(cfg DebugConfig) error {
	if err := a.requireDebugSvc(); err != nil {
		return err
	}
	return a.debugSvc.SetConfig(cfg)
}

// TestDebugConnection tests connectivity to configured servers and returns diagnostic info.
func (a *App) TestDebugConnection(cfg DebugConfig) (string, error) {
	if err := a.requireDebugSvc(); err != nil {
		return "", err
	}
	return a.debugSvc.TestConnection(cfg)
}

// GetRealtimeStatus queries /api/v1/agent-auth/me/realtime/status from the configured HTTP server.
func (a *App) GetRealtimeStatus() (RealtimeStatus, error) {
	if err := a.requireDebugSvc(); err != nil {
		return RealtimeStatus{}, err
	}
	return a.debugSvc.GetRealtimeStatus()
}

// GetAgentStatus returns the current agent connectivity status.
func (a *App) GetAgentStatus() AgentStatus {
	if a == nil {
		return AgentStatus{}
	}
	if status, ok := a.getServiceAgentStatus(); ok {
		return a.resolveAgentConnectivity(status)
	}
	if a.debugSvc == nil {
		return AgentStatus{}
	}
	return a.resolveAgentConnectivity(a.debugSvc.GetAgentStatus())
}

func (a *App) getServiceAgentStatus() (AgentStatus, bool) {
	if a == nil || !a.serviceConnectedMode.Load() || a.serviceClient == nil {
		return AgentStatus{}, false
	}

	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if !a.serviceClient.IsConnected() {
		if err := a.serviceClient.Connect(ctx); err != nil {
			return AgentStatus{}, false
		}
	}

	resp, err := a.serviceClient.GetStatus(ctx)
	if err != nil {
		_ = a.serviceClient.Close()
		if reconnectErr := a.serviceClient.Connect(ctx); reconnectErr != nil {
			return AgentStatus{}, false
		}
		resp, err = a.serviceClient.GetStatus(ctx)
		if err != nil {
			return AgentStatus{}, false
		}
	}
	if resp == nil || resp.Data == nil {
		return AgentStatus{}, false
	}
	return agentStatusFromServiceStatusData(resp.Data), true
}

func agentStatusFromServiceStatusData(data map[string]interface{}) AgentStatus {
	if data == nil {
		return AgentStatus{}
	}
	status := AgentStatus{
		Connected:                  boolFromServiceStatusValue(data["agent_connected"]),
		TransportConnected:         boolFromServiceStatusValue(data["agent_transport_connected"]),
		AgentID:                    strings.TrimSpace(stringFromServiceStatusValue(data["agent_id"])),
		Server:                     strings.TrimSpace(stringFromServiceStatusValue(data["agent_server"])),
		LastEvent:                  strings.TrimSpace(stringFromServiceStatusValue(data["agent_last_event"])),
		Transport:                  strings.TrimSpace(stringFromServiceStatusValue(data["agent_transport"])),
		OnlineReason:               strings.TrimSpace(stringFromServiceStatusValue(data["agent_online_reason"])),
		LastGlobalPongAtUTC:        strings.TrimSpace(stringFromServiceStatusValue(data["agent_last_global_pong_at"])),
		GlobalPongStale:            boolFromServiceStatusValue(data["agent_global_pong_stale"]),
		NonCriticalBackoffUntilUTC: strings.TrimSpace(stringFromServiceStatusValue(data["agent_non_critical_backoff_until"])),
		NonCriticalBackoffReason:   strings.TrimSpace(stringFromServiceStatusValue(data["agent_non_critical_backoff_reason"])),
	}
	if !status.TransportConnected {
		status.TransportConnected = status.Connected
	}
	if status.Server == "" {
		status.Server = strings.TrimSpace(stringFromServiceStatusValue(data["server_url"]))
	}
	return status
}

func (a *App) requestServiceConfigReload(ctx context.Context, source string) {
	if a == nil || !a.serviceConnectedMode.Load() || a.serviceClient == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !a.serviceClient.IsConnected() {
		if err := a.serviceClient.Connect(ctx); err != nil {
			a.logs.append("[service] falha ao conectar para reload_config: " + err.Error())
			return
		}
	}
	payload := map[string]interface{}{}
	if source = strings.TrimSpace(source); source != "" {
		payload["source"] = source
	}
	if _, err := a.serviceClient.Execute(ctx, "reload_config", payload); err != nil {
		_ = a.serviceClient.Close()
		if reconnectErr := a.serviceClient.Connect(ctx); reconnectErr != nil {
			a.logs.append("[service] falha ao reconectar para reload_config: " + reconnectErr.Error())
			return
		}
		if _, err = a.serviceClient.Execute(ctx, "reload_config", payload); err != nil {
			a.logs.append("[service] reload_config falhou: " + err.Error())
			return
		}
	}
	a.logs.append("[service] reload_config enfileirado: source=" + strings.TrimSpace(source))
}

func stringFromServiceStatusValue(raw interface{}) string {
	if raw == nil {
		return ""
	}
	return fmt.Sprint(raw)
}

func boolFromServiceStatusValue(raw interface{}) bool {
	value, ok := raw.(bool)
	return ok && value
}
