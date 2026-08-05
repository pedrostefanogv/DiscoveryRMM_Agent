package sync

import (
	cryptorand "crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"discovery/app/core/agentconn"
	debug "discovery/app/debug"
	"discovery/app/syncmeta"
)

// Backoff encapsula o estado de backoff de tráfego não essencial e o sinal de
// conectividade online baseado em tenant.global.pong. É uma estrutura com
// estado próprio (mutex + campos), isolada do *App para permitir testes
// unitários e migração a um Service Wails3.
type Backoff struct {
	deps SyncDeps

	mu                       sync.RWMutex
	nonCriticalBackoffUntil  time.Time
	nonCriticalBackoffReason string
	lastGlobalPongAt         time.Time
	lastGlobalPongServerTime string
	lastGlobalPongKnown      bool
	lastGlobalPongOverloaded bool
}

// NewBackoff cria um Backoff com as dependências injetadas.
func NewBackoff(deps SyncDeps) *Backoff {
	return &Backoff{deps: deps}
}

// HandleGlobalPong processa um tenant.global.pong recebido via transporte.
func (b *Backoff) HandleGlobalPong(pong agentconn.GlobalPongMessage) {
	if b == nil {
		return
	}

	receivedAt := pong.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = time.Now().UTC()
	}
	b.recordGlobalPong(receivedAt, strings.TrimSpace(pong.ServerTimeUTC), pong.ServerOverloaded)

	if pong.ServerOverloaded == nil {
		b.deps.Log("[agent-sync] global pong recebido sem estado de sobrecarga")
		return
	}

	if *pong.ServerOverloaded {
		delay := randomNonCriticalBackoffDuration()
		b.ExtendNonCriticalBackoff(delay, "tenant.global.pong:serverOverloaded=true")
		remaining, deferred, _ := b.NonCriticalBackoffWindow()
		if deferred {
			b.deps.Log(fmt.Sprintf("[agent-sync] servidor sobrecarregado; tráfego não essencial adiado por %s", remaining.Round(time.Second)))
		}
		return
	}

	if b.ClearNonCriticalBackoff() {
		b.deps.Log("[agent-sync] servidor normalizado; retomando trafego nao essencial")
	}
}

func (b *Backoff) recordGlobalPong(receivedAt time.Time, serverTime string, overloaded *bool) {
	if b == nil {
		return
	}
	if receivedAt.IsZero() {
		receivedAt = time.Now().UTC()
	}

	b.mu.Lock()
	b.lastGlobalPongAt = receivedAt.UTC()
	b.lastGlobalPongServerTime = strings.TrimSpace(serverTime)
	if overloaded != nil {
		b.lastGlobalPongKnown = true
		b.lastGlobalPongOverloaded = *overloaded
	} else {
		b.lastGlobalPongKnown = false
		b.lastGlobalPongOverloaded = false
	}
	b.mu.Unlock()
}

// ExtendNonCriticalBackoff estende a janela de backoff se o novo atraso for
// maior que o atual.
func (b *Backoff) ExtendNonCriticalBackoff(delay time.Duration, reason string) {
	if b == nil {
		return
	}
	if delay <= 0 {
		delay = syncmeta.NonCriticalBackoffMin
	}

	until := time.Now().UTC().Add(delay)
	b.mu.Lock()
	if until.After(b.nonCriticalBackoffUntil) {
		b.nonCriticalBackoffUntil = until
		b.nonCriticalBackoffReason = strings.TrimSpace(reason)
	}
	b.mu.Unlock()
}

// ClearNonCriticalBackoff limpa o backoff e retorna se havia tráfego adiado.
func (b *Backoff) ClearNonCriticalBackoff() bool {
	if b == nil {
		return false
	}
	now := time.Now().UTC()
	b.mu.Lock()
	deferred := !b.nonCriticalBackoffUntil.IsZero() && b.nonCriticalBackoffUntil.After(now)
	b.nonCriticalBackoffUntil = time.Time{}
	b.nonCriticalBackoffReason = ""
	b.mu.Unlock()
	return deferred
}

// NonCriticalBackoffWindow retorna o tempo restante de adiamento, se houver.
func (b *Backoff) NonCriticalBackoffWindow() (time.Duration, bool, string) {
	until, deferred, reason := b.NonCriticalBackoffStatus()
	if !deferred {
		return 0, false, ""
	}
	remaining := until.Sub(time.Now().UTC())
	if remaining <= 0 {
		return 0, false, ""
	}
	return remaining, true, reason
}

// NonCriticalBackoffStatus retorna o estado bruto do backoff.
func (b *Backoff) NonCriticalBackoffStatus() (time.Time, bool, string) {
	if b == nil {
		return time.Time{}, false, ""
	}

	now := time.Now().UTC()
	b.mu.RLock()
	until := b.nonCriticalBackoffUntil
	reason := strings.TrimSpace(b.nonCriticalBackoffReason)
	b.mu.RUnlock()

	if until.IsZero() || !until.After(now) {
		if !until.IsZero() {
			b.ClearNonCriticalBackoff()
		}
		return time.Time{}, false, ""
	}

	return until, true, reason
}

// GlobalPongStatus retorna o estado do último pong global.
func (b *Backoff) GlobalPongStatus() (time.Time, string, bool, bool) {
	if b == nil {
		return time.Time{}, "", false, false
	}
	b.mu.RLock()
	lastPongAt := b.lastGlobalPongAt
	serverTime := strings.TrimSpace(b.lastGlobalPongServerTime)
	overloadedKnown := b.lastGlobalPongKnown
	overloaded := b.lastGlobalPongOverloaded
	b.mu.RUnlock()
	return lastPongAt, serverTime, overloadedKnown, overloaded
}

// ResolveAgentConnectivity enriquece o status do agent com o sinal de
// conectividade online baseado em pong global e backoff.
func (b *Backoff) ResolveAgentConnectivity(status debug.AgentStatus) debug.AgentStatus {
	if b == nil {
		return status
	}

	transportConnected := status.TransportConnected || status.Connected
	status.TransportConnected = transportConnected

	if status.LastGlobalPongAtUTC == "" {
		if localPongAt, _, _, _ := b.GlobalPongStatus(); !localPongAt.IsZero() {
			status.LastGlobalPongAtUTC = localPongAt.UTC().Format(time.RFC3339)
		}
	}
	if status.NonCriticalBackoffUntilUTC == "" {
		if until, deferred, reason := b.NonCriticalBackoffStatus(); deferred {
			status.NonCriticalBackoffUntilUTC = until.UTC().Format(time.RFC3339)
			if status.NonCriticalBackoffReason == "" {
				status.NonCriticalBackoffReason = reason
			}
		}
	}

	lastPongAt := ParseRFC3339Time(strings.TrimSpace(status.LastGlobalPongAtUTC))
	online, reason, stale := EvaluateAgentOnlineSignal(transportConnected, lastPongAt)
	status.Connected = online
	status.GlobalPongStale = stale
	if strings.TrimSpace(status.OnlineReason) == "" {
		status.OnlineReason = reason
	}

	return status
}

// EvaluateAgentOnlineSignal determina o sinal online a partir do transporte e
// do último pong global.
func EvaluateAgentOnlineSignal(transportConnected bool, lastGlobalPongAt time.Time) (bool, string, bool) {
	if !transportConnected {
		return false, "transporte desconectado", false
	}
	if lastGlobalPongAt.IsZero() {
		return true, "transporte conectado; aguardando primeiro tenant.global.pong", false
	}
	age := time.Since(lastGlobalPongAt)
	if age <= syncmeta.GlobalPongStaleAfter {
		return true, fmt.Sprintf("pong global recebido ha %s", age.Round(time.Second)), false
	}
	return false, fmt.Sprintf("sem pong global recente (ultimo ha %s)", age.Round(time.Second)), true
}

// ParseRFC3339Time faz parse de uma string RFC3339, retornando zero se vazia
// ou inválida.
func ParseRFC3339Time(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// randomNonCriticalBackoffDuration gera um atraso aleatório dentro do range.
func randomNonCriticalBackoffDuration() time.Duration {
	min := int64(syncmeta.NonCriticalBackoffMin)
	max := int64(syncmeta.NonCriticalBackoffMax)
	if max <= min {
		return syncmeta.NonCriticalBackoffMin
	}
	n, err := cryptorand.Int(cryptorand.Reader, big.NewInt(max-min))
	if err != nil {
		return syncmeta.NonCriticalBackoffMin
	}
	return time.Duration(min + n.Int64())
}
