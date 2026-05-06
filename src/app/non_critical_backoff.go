package app

import (
	cryptorand "crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"time"

	"discovery/internal/agentconn"
)

const (
	nonCriticalBackoffMin = 10 * time.Minute
	nonCriticalBackoffMax = 15 * time.Minute
	globalPongStaleAfter  = 3 * time.Minute
)

func (a *App) handleGlobalPong(pong agentconn.GlobalPongMessage) {
	if a == nil {
		return
	}

	receivedAt := pong.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = time.Now().UTC()
	}
	a.recordGlobalPong(receivedAt, strings.TrimSpace(pong.ServerTimeUTC), pong.ServerOverloaded)

	if pong.ServerOverloaded == nil {
		a.logs.append("[agent-sync] global pong recebido sem estado de sobrecarga")
		return
	}

	if *pong.ServerOverloaded {
		delay := randomNonCriticalBackoffDuration()
		a.extendNonCriticalBackoff(delay, "tenant.global.pong:serverOverloaded=true")
		remaining, deferred, _ := a.nonCriticalBackoffWindow()
		if deferred {
			a.logs.append(fmt.Sprintf("[agent-sync] servidor sobrecarregado; trafego nao essencial adiado por %s", remaining.Round(time.Second)))
		}
		return
	}

	if a.clearNonCriticalBackoff() {
		a.logs.append("[agent-sync] servidor normalizado; retomando trafego nao essencial")
	}
}

func (a *App) recordGlobalPong(receivedAt time.Time, serverTime string, overloaded *bool) {
	if a == nil {
		return
	}
	if receivedAt.IsZero() {
		receivedAt = time.Now().UTC()
	}

	a.nonCriticalMu.Lock()
	a.lastGlobalPongAt = receivedAt.UTC()
	a.lastGlobalPongServerTime = strings.TrimSpace(serverTime)
	if overloaded != nil {
		a.lastGlobalPongKnown = true
		a.lastGlobalPongOverloaded = *overloaded
	} else {
		a.lastGlobalPongKnown = false
		a.lastGlobalPongOverloaded = false
	}
	a.nonCriticalMu.Unlock()
}

func (a *App) extendNonCriticalBackoff(delay time.Duration, reason string) {
	if a == nil {
		return
	}
	if delay <= 0 {
		delay = nonCriticalBackoffMin
	}

	until := time.Now().UTC().Add(delay)
	a.nonCriticalMu.Lock()
	if until.After(a.nonCriticalBackoffUntil) {
		a.nonCriticalBackoffUntil = until
		a.nonCriticalBackoffReason = strings.TrimSpace(reason)
	}
	a.nonCriticalMu.Unlock()
}

func (a *App) clearNonCriticalBackoff() bool {
	if a == nil {
		return false
	}
	now := time.Now().UTC()
	a.nonCriticalMu.Lock()
	deferred := !a.nonCriticalBackoffUntil.IsZero() && a.nonCriticalBackoffUntil.After(now)
	a.nonCriticalBackoffUntil = time.Time{}
	a.nonCriticalBackoffReason = ""
	a.nonCriticalMu.Unlock()
	return deferred
}

func (a *App) nonCriticalBackoffWindow() (time.Duration, bool, string) {
	until, deferred, reason := a.nonCriticalBackoffStatus()
	if !deferred {
		return 0, false, ""
	}
	remaining := until.Sub(time.Now().UTC())
	if remaining <= 0 {
		return 0, false, ""
	}
	return remaining, true, reason
}

func (a *App) nonCriticalBackoffStatus() (time.Time, bool, string) {
	if a == nil {
		return time.Time{}, false, ""
	}

	now := time.Now().UTC()
	a.nonCriticalMu.RLock()
	until := a.nonCriticalBackoffUntil
	reason := strings.TrimSpace(a.nonCriticalBackoffReason)
	a.nonCriticalMu.RUnlock()

	if until.IsZero() || !until.After(now) {
		if !until.IsZero() {
			a.clearNonCriticalBackoff()
		}
		return time.Time{}, false, ""
	}

	return until, true, reason
}

func (a *App) globalPongStatus() (time.Time, string, bool, bool) {
	if a == nil {
		return time.Time{}, "", false, false
	}
	a.nonCriticalMu.RLock()
	lastPongAt := a.lastGlobalPongAt
	serverTime := strings.TrimSpace(a.lastGlobalPongServerTime)
	overloadedKnown := a.lastGlobalPongKnown
	overloaded := a.lastGlobalPongOverloaded
	a.nonCriticalMu.RUnlock()
	return lastPongAt, serverTime, overloadedKnown, overloaded
}

func (a *App) resolveAgentConnectivity(status AgentStatus) AgentStatus {
	if a == nil {
		return status
	}

	transportConnected := status.TransportConnected || status.Connected
	status.TransportConnected = transportConnected

	if status.LastGlobalPongAtUTC == "" {
		if localPongAt, _, _, _ := a.globalPongStatus(); !localPongAt.IsZero() {
			status.LastGlobalPongAtUTC = localPongAt.UTC().Format(time.RFC3339)
		}
	}
	if status.NonCriticalBackoffUntilUTC == "" {
		if until, deferred, reason := a.nonCriticalBackoffStatus(); deferred {
			status.NonCriticalBackoffUntilUTC = until.UTC().Format(time.RFC3339)
			if status.NonCriticalBackoffReason == "" {
				status.NonCriticalBackoffReason = reason
			}
		}
	}

	lastPongAt := parseRFC3339Time(strings.TrimSpace(status.LastGlobalPongAtUTC))
	online, reason, stale := evaluateAgentOnlineSignal(transportConnected, lastPongAt)
	status.Connected = online
	status.GlobalPongStale = stale
	if strings.TrimSpace(status.OnlineReason) == "" {
		status.OnlineReason = reason
	}

	return status
}

func evaluateAgentOnlineSignal(transportConnected bool, lastGlobalPongAt time.Time) (bool, string, bool) {
	if !transportConnected {
		return false, "transporte desconectado", false
	}
	if lastGlobalPongAt.IsZero() {
		return true, "transporte conectado; aguardando primeiro tenant.global.pong", false
	}
	age := time.Since(lastGlobalPongAt)
	if age <= globalPongStaleAfter {
		return true, fmt.Sprintf("pong global recebido ha %s", age.Round(time.Second)), false
	}
	return false, fmt.Sprintf("sem pong global recente (ultimo ha %s)", age.Round(time.Second)), true
}

func parseRFC3339Time(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func randomNonCriticalBackoffDuration() time.Duration {
	rangeMinutes := int64((nonCriticalBackoffMax - nonCriticalBackoffMin) / time.Minute)
	if rangeMinutes <= 0 {
		return nonCriticalBackoffMin
	}

	n, err := cryptorand.Int(cryptorand.Reader, big.NewInt(rangeMinutes+1))
	if err != nil {
		return nonCriticalBackoffMin
	}
	return nonCriticalBackoffMin + time.Duration(n.Int64())*time.Minute
}
