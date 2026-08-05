package app

import (
	"time"

	"discovery/app/core/agentconn"
	"discovery/app/sync"
)

// Bridges de backoff não-crítico e sinal de conectividade. A lógica foi
// movida para o pacote sync (sync.Backoff); estes métodos delegam para a
// instância do *App e são usados como callbacks do agentconn e por status.go.
func (a *App) handleGlobalPong(pong agentconn.GlobalPongMessage) {
	if a.syncBackoff == nil {
		return
	}
	a.syncBackoff.HandleGlobalPong(pong)
}

func (a *App) recordGlobalPong(receivedAt time.Time, serverTime string, overloaded *bool) {
	if a.syncBackoff == nil {
		return
	}
	a.syncBackoff.RecordGlobalPong(receivedAt, serverTime, overloaded)
}

func (a *App) extendNonCriticalBackoff(delay time.Duration, reason string) {
	if a.syncBackoff == nil {
		return
	}
	a.syncBackoff.ExtendNonCriticalBackoff(delay, reason)
}

func (a *App) clearNonCriticalBackoff() bool {
	if a.syncBackoff == nil {
		return false
	}
	return a.syncBackoff.ClearNonCriticalBackoff()
}

func (a *App) nonCriticalBackoffWindow() (time.Duration, bool, string) {
	if a.syncBackoff == nil {
		return 0, false, ""
	}
	return a.syncBackoff.NonCriticalBackoffWindow()
}

func (a *App) nonCriticalBackoffStatus() (time.Time, bool, string) {
	if a.syncBackoff == nil {
		return time.Time{}, false, ""
	}
	return a.syncBackoff.NonCriticalBackoffStatus()
}

func (a *App) globalPongStatus() (time.Time, string, bool, bool) {
	if a.syncBackoff == nil {
		return time.Time{}, "", false, false
	}
	return a.syncBackoff.GlobalPongStatus()
}

func (a *App) resolveAgentConnectivity(status AgentStatus) AgentStatus {
	if a.syncBackoff == nil {
		return status
	}
	return a.syncBackoff.ResolveAgentConnectivity(status)
}

// evaluateAgentOnlineSignal delega para o pacote sync.
func evaluateAgentOnlineSignal(transportConnected bool, lastGlobalPongAt time.Time) (bool, string, bool) {
	return sync.EvaluateAgentOnlineSignal(transportConnected, lastGlobalPongAt)
}

// parseRFC3339Time delega para o pacote sync.
func parseRFC3339Time(raw string) time.Time {
	return sync.ParseRFC3339Time(raw)
}
