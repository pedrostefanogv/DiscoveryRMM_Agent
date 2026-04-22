package app

const (
	OfflineQueueModeLoggingOnly     = "logging_only"
	OfflineQueueModeEnqueueOnly     = "enqueue_only"
	OfflineQueueModeEnqueueAndDrain = "enqueue_and_drain"
)

func (a *App) commandResultOfflineMode() string {
	return normalizeOfflineQueueMode(a.GetAgentConfiguration().Rollout.CommandResultOfflineMode)
}

func (a *App) p2pTelemetryOfflineMode() string {
	return normalizeOfflineQueueMode(a.GetAgentConfiguration().Rollout.P2PTelemetryOfflineMode)
}

func (a *App) shouldEnqueueCommandResultOutbox() bool {
	return a.commandResultOfflineMode() != OfflineQueueModeLoggingOnly
}

func (a *App) shouldDrainCommandResultOutbox() bool {
	return a.commandResultOfflineMode() == OfflineQueueModeEnqueueAndDrain
}

func (a *App) shouldEnqueueP2PTelemetryOutbox() bool {
	return a.p2pTelemetryOfflineMode() != OfflineQueueModeLoggingOnly
}

func (a *App) shouldDrainP2PTelemetryOutbox() bool {
	return a.p2pTelemetryOfflineMode() == OfflineQueueModeEnqueueAndDrain
}
