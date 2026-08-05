package app

// Bridges de rollout offline. A lógica foi movida para o pacote sync
// (sync.Rollout); estes métodos delegam para a instância do *App.
func (a *App) commandResultOfflineMode() string {
	if a.syncRollout == nil {
		return ""
	}
	return a.syncRollout.CommandResultOfflineMode()
}

func (a *App) p2pTelemetryOfflineMode() string {
	if a.syncRollout == nil {
		return ""
	}
	return a.syncRollout.P2PTelemetryOfflineMode()
}

func (a *App) shouldEnqueueCommandResultOutbox() bool {
	if a.syncRollout == nil {
		return false
	}
	return a.syncRollout.ShouldEnqueueCommandResultOutbox()
}

func (a *App) shouldDrainCommandResultOutbox() bool {
	if a.syncRollout == nil {
		return false
	}
	return a.syncRollout.ShouldDrainCommandResultOutbox()
}

func (a *App) shouldEnqueueP2PTelemetryOutbox() bool {
	if a.syncRollout == nil {
		return false
	}
	return a.syncRollout.ShouldEnqueueP2PTelemetryOutbox()
}

func (a *App) shouldDrainP2PTelemetryOutbox() bool {
	if a.syncRollout == nil {
		return false
	}
	return a.syncRollout.ShouldDrainP2PTelemetryOutbox()
}
