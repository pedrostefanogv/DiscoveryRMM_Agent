package app

import (
	"time"

	"discovery/app/core/agentconn"
	"discovery/app/sync"
)

// Bridges de backoff não-crítico e sinal de conectividade. A lógica foi
// movida para o pacote sync (sync.Service → sync.Backoff); estes métodos
// delegam para a instância do *App e são usados como callbacks do agentconn
// e por status.go.
func (a *App) handleGlobalPong(pong agentconn.GlobalPongMessage) {
	if a.syncSvc == nil {
		return
	}
	a.syncSvc.HandleGlobalPong(pong)
}

func (a *App) nonCriticalBackoffWindow() (time.Duration, bool, string) {
	if a.syncSvc == nil {
		return 0, false, ""
	}
	return a.syncSvc.NonCriticalBackoffWindow()
}

func (a *App) nonCriticalBackoffStatus() (time.Time, bool, string) {
	if a.syncSvc == nil {
		return time.Time{}, false, ""
	}
	return a.syncSvc.NonCriticalBackoffStatus()
}

func (a *App) resolveAgentConnectivity(status AgentStatus) AgentStatus {
	if a.syncSvc == nil {
		return status
	}
	return a.syncSvc.ResolveAgentConnectivity(status)
}

// parseRFC3339Time delega para o pacote sync.
func parseRFC3339Time(raw string) time.Time {
	return sync.ParseRFC3339Time(raw)
}
