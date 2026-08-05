package sync

import (
	"discovery/app/agentconfig"
)

// Rollout encapsula a lógica de modos offline (rollout) do agent.
// É uma estrutura sem estado próprio — lê a configuração via SyncDeps.
type Rollout struct {
	deps SyncDeps
}

// NewRollout cria um Rollout com as dependências injetadas.
func NewRollout(deps SyncDeps) *Rollout {
	return &Rollout{deps: deps}
}

// CommandResultOfflineMode retorna o modo offline normalizado para resultados
// de comando.
func (r *Rollout) CommandResultOfflineMode() string {
	return agentconfig.NormalizeOfflineQueueMode(r.deps.GetAgentConfiguration().Rollout.CommandResultOfflineMode)
}

// P2PTelemetryOfflineMode retorna o modo offline normalizado para telemetria P2P.
func (r *Rollout) P2PTelemetryOfflineMode() string {
	return agentconfig.NormalizeOfflineQueueMode(r.deps.GetAgentConfiguration().Rollout.P2PTelemetryOfflineMode)
}

// ShouldEnqueueCommandResultOutbox indica se resultados de comando devem ser
// enfileirados no outbox (qualquer modo exceto logging-only).
func (r *Rollout) ShouldEnqueueCommandResultOutbox() bool {
	return r.CommandResultOfflineMode() != agentconfig.OfflineQueueModeLoggingOnly
}

// ShouldDrainCommandResultOutbox indica se o outbox de resultados de comando
// deve ser drenado (modo enqueue-and-drain).
func (r *Rollout) ShouldDrainCommandResultOutbox() bool {
	return r.CommandResultOfflineMode() == agentconfig.OfflineQueueModeEnqueueAndDrain
}

// ShouldEnqueueP2PTelemetryOutbox indica se telemetria P2P deve ser
// enfileirada no outbox (qualquer modo exceto logging-only).
func (r *Rollout) ShouldEnqueueP2PTelemetryOutbox() bool {
	return r.P2PTelemetryOfflineMode() != agentconfig.OfflineQueueModeLoggingOnly
}

// ShouldDrainP2PTelemetryOutbox indica se o outbox de telemetria P2P deve ser
// drenado (modo enqueue-and-drain).
func (r *Rollout) ShouldDrainP2PTelemetryOutbox() bool {
	return r.P2PTelemetryOfflineMode() == agentconfig.OfflineQueueModeEnqueueAndDrain
}
