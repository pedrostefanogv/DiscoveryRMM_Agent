package app

func (a *App) GetDebugConfig() DebugConfig {
	if a == nil || a.DebugSvc == nil {
		return DebugConfig{}
	}
	return a.DebugSvc.GetConfig()
}

func (a *App) SetDebugConfig(cfg DebugConfig) error {
	if err := a.requireDebugSvc(); err != nil {
		return err
	}
	return a.DebugSvc.SetConfig(cfg)
}

func (a *App) TestDebugConnection(cfg DebugConfig) (string, error) {
	if err := a.requireDebugSvc(); err != nil {
		return "", err
	}
	return a.DebugSvc.TestConnection(cfg)
}

func (a *App) GetRealtimeStatus() (RealtimeStatus, error) {
	if err := a.requireDebugSvc(); err != nil {
		return RealtimeStatus{}, err
	}
	return a.DebugSvc.GetRealtimeStatus()
}

func (a *App) GetAgentStatus() AgentStatus {
	if a == nil {
		return AgentStatus{}
	}
	// Companion mode: o core (agentConn) roda no serviço — o agentConn local
	// nunca conecta nesta UI, então GetAgentStatus() local retornaria sempre
	// offline (tray cinza/status offline mesmo com heartbeat no site). Usa o
	// último snapshot recebido via IPC (agent:status_snapshot) quando houver.
	if a.ipcClient != nil {
		a.companionStatusMu.Lock()
		snap := a.companionStatus
		a.companionStatusMu.Unlock()
		if snap != nil {
			return *snap
		}
	}
	if a.DebugSvc == nil {
		return AgentStatus{}
	}
	return a.resolveAgentConnectivity(a.DebugSvc.GetAgentStatus())
}
