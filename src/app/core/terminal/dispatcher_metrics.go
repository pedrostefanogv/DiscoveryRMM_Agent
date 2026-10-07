//go:build windows

package terminal

import (
	"strconv"
	"strings"
	"sync/atomic"
)

// dispatcherDroppedBytes acumula os bytes descartados pelo backpressure da
// saída do dispatcher (processo filho). O dispatcher emite periodicamente a
// linha "[dispatcher-metrics] dropped=<N>" no stderr; o agente a consome em
// teeDispatcherLog e publica o valor em .stats (R8).
var dispatcherDroppedBytes atomic.Int64

// DispatcherDroppedBytes reporta os bytes descartados pelo dispatcher de saída.
func DispatcherDroppedBytes() int64 { return dispatcherDroppedBytes.Load() }

func setDispatcherDroppedBytes(v int64) { dispatcherDroppedBytes.Store(v) }

// resetDispatcherDroppedBytes zera o contador no início de um novo dispatcher,
// para que .stats não mostre o descarte da sessão anterior (R8).
func resetDispatcherDroppedBytes() { dispatcherDroppedBytes.Store(0) }

// parseDispatcherMetric extrai um inteiro precedido por key na linha.
func parseDispatcherMetric(line, key string) (int64, bool) {
	i := strings.Index(line, key)
	if i < 0 {
		return 0, false
	}
	rest := line[i+len(key):]
	j := 0
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	if j == 0 {
		return 0, false
	}
	v, err := strconv.ParseInt(rest[:j], 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
