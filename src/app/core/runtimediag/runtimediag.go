// Package runtimediag expõe uma fotografia da memória do runtime Go usada para
// diagnóstico e para validar as otimizações de RAM do agente.
//
// Antes deste pacote não havia nenhuma instrumentação de heap no agente (grep por
// runtime.MemStats/HeapAlloc/pprof = 0 ocorrências): não era possível medir nem
// acompanhar o consumo. O Snapshot é barato (runtime.ReadMemStats) e o Log roda
// somente em modo debug.
package runtimediag

import (
	"context"
	"fmt"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"time"
)

// Snapshot é uma fotografia do heap/runtime em MiB (exceto onde indicado).
type Snapshot struct {
	HeapAllocMiB    float64 `json:"heapAllocMiB"`
	HeapInuseMiB    float64 `json:"heapInuseMiB"`
	HeapSysMiB      float64 `json:"heapSysMiB"`
	HeapReleasedMiB float64 `json:"heapReleasedMiB"`
	StackInuseMiB   float64 `json:"stackInuseMiB"`
	OtherSysMiB     float64 `json:"otherSysMiB"`
	SysMiB          float64 `json:"sysMiB"`
	TotalAllocMiB   float64 `json:"totalAllocMiB"`
	PauseTotalMs    float64 `json:"pauseTotalMs"`
	NumGC           uint32  `json:"numGC"`
	Goroutines      int     `json:"goroutines"`
	GOGCPercent     int64   `json:"gogcPercent"`
	MemoryLimitMiB  int64   `json:"memoryLimitMiB"`
}

// Take monta o Snapshot atual.
func Take() Snapshot {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	return Snapshot{
		HeapAllocMiB:    toMiB(ms.HeapAlloc),
		HeapInuseMiB:    toMiB(ms.HeapInuse),
		HeapSysMiB:      toMiB(ms.HeapSys),
		HeapReleasedMiB: toMiB(ms.HeapReleased),
		StackInuseMiB:   toMiB(ms.StackInuse),
		OtherSysMiB:     toMiB(ms.OtherSys),
		SysMiB:          toMiB(ms.Sys),
		TotalAllocMiB:   toMiB(ms.TotalAlloc),
		PauseTotalMs:    float64(ms.PauseTotalNs) / float64(time.Millisecond),
		NumGC:           ms.NumGC,
		Goroutines:      runtime.NumGoroutine(),
		GOGCPercent:     gogcPercent(),
		MemoryLimitMiB:  debug.SetMemoryLimit(-1) >> 20,
	}
}

// String devolve a linha de log compacta usada pelo agente.
func (s Snapshot) String() string {
	return fmt.Sprintf(
		"heap=%.1fMiB inuse=%.1fMiB sys=%.1fMiB released=%.1fMiB stack=%.1fMiB totalAlloc=%.1fMiB gc=%d pause=%.0fms goroutines=%d gogc=%d memlimit=%dMiB",
		s.HeapAllocMiB, s.HeapInuseMiB, s.SysMiB, s.HeapReleasedMiB, s.StackInuseMiB,
		s.TotalAllocMiB, s.NumGC, s.PauseTotalMs, s.Goroutines, s.GOGCPercent, s.MemoryLimitMiB,
	)
}

// Log emite uma linha "[mem] ..." a cada interval até o contexto ser cancelado.
// emit pode ser nil (usa apenas o pacote padrão de log via fmt não é feito aqui
// para manter o pacote livre de dependências do agente).
func Log(ctx context.Context, interval time.Duration, emit func(string)) {
	if interval <= 0 || emit == nil {
		return
	}
	emit("[mem] " + Take().String())

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			emit("[mem] " + Take().String())
		}
	}
}

func toMiB(v uint64) float64 { return float64(v) / (1 << 20) }

// gogcPercent lê o GOGC efetivo via runtime/metrics (debug.SetGCPercent(-1)
// desabilitaria o GC, então não pode ser usado para consulta).
func gogcPercent() int64 {
	samples := []metrics.Sample{{Name: "/gc/gogc:percent"}}
	metrics.Read(samples)
	if samples[0].Value.Kind() == metrics.KindUint64 {
		return int64(samples[0].Value.Uint64())
	}
	return -1
}
