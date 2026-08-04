package app

import "discovery/app/agentconfig"

func (a *App) commandResultOfflineMode() string {
	return agentconfig.NormalizeOfflineQueueMode(a.GetAgentConfiguration().Rollout.CommandResultOfflineMode)
}

func (a *App) p2pTelemetryOfflineMode() string {
	return agentconfig.NormalizeOfflineQueueMode(a.GetAgentConfiguration().Rollout.P2PTelemetryOfflineMode)
}

func (a *App) shouldEnqueueCommandResultOutbox() bool {
	return a.commandResultOfflineMode() != agentconfig.OfflineQueueModeLoggingOnly
}

func (a *App) shouldDrainCommandResultOutbox() bool {
	return a.commandResultOfflineMode() == agentconfig.OfflineQueueModeEnqueueAndDrain
}

func (a *App) shouldEnqueueP2PTelemetryOutbox() bool {
	return a.p2pTelemetryOfflineMode() != agentconfig.OfflineQueueModeLoggingOnly
}

func (a *App) shouldDrainP2PTelemetryOutbox() bool {
	return a.p2pTelemetryOfflineMode() == agentconfig.OfflineQueueModeEnqueueAndDrain
}
