package app

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

// GetRealtimeStatus queries /api/realtime/status from the configured HTTP server.
func (a *App) GetRealtimeStatus() (RealtimeStatus, error) {
	if err := a.requireDebugSvc(); err != nil {
		return RealtimeStatus{}, err
	}
	return a.debugSvc.GetRealtimeStatus()
}

// GetAgentStatus returns the current agent connectivity status.
func (a *App) GetAgentStatus() AgentStatus {
	if a == nil || a.debugSvc == nil {
		return AgentStatus{}
	}
	return a.debugSvc.GetAgentStatus()
}
