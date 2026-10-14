// Package memlimit aplica um teto suave de memória ao runtime Go e devolve
// páginas ao sistema operacional em janelas de ociosidade.
//
// Motivação (análise de RAM do agente, ver plano de otimização): o agente é um
// processo de vida longa que executa picos de alocação (ex.: leitura/parse da
// política efetiva da app-store) e, sem teto, o runtime mantém o heap no pico
// histórico — o RSS nunca volta. Um limite suave (GOMEMLIMIT) faz o GC trabalhar
// um pouco mais apenas perto do teto, e FreeOSMemory devolve as páginas livres
// ao SO quando o agente está ocioso.
//
// Sem impacto funcional: nenhuma alocação falha; o runtime apenas antecipa o GC.
package memlimit

import (
	"math"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

// EnvLimit é a variável de ambiente que sobrepõe o teto. Valor em MiB; 0 (ou
// negativo) desabilita o teto.
const EnvLimit = "DISCOVERY_MEMORY_LIMIT_MB"

// relaxInterval é o intervalo mínimo entre duas devoluções de memória ao SO.
// FreeOSMemory é caro (força GC + scavenge síncrono); chamadas repetidas
// causariam engasgos sem ganho.
const relaxInterval = 10 * time.Minute

var (
	relaxMu   sync.Mutex
	lastRelax time.Time
)

// Apply aplica o teto de memória. defaultMiB é usado quando a variável de
// ambiente não está definida ou é inválida. Retorna o teto efetivo em MiB
// (0 = ilimitado).
func Apply(defaultMiB int) int {
	mib := defaultMiB
	if raw := strings.TrimSpace(os.Getenv(EnvLimit)); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil {
			mib = v
		}
	}
	if mib <= 0 {
		// math.MaxInt64 desabilita efetivamente o limite (documentado).
		debug.SetMemoryLimit(math.MaxInt64)
		return 0
	}
	debug.SetMemoryLimit(int64(mib) << 20)
	return mib
}

// Current devolve o teto efetivo atual em bytes. Entrada negativa em
// SetMemoryLimit apenas consulta o valor, não altera o limite.
func Current() int64 {
	return debug.SetMemoryLimit(-1)
}

// Relax força GC + devolução de páginas ao SO, respeitando relaxInterval.
// Retorna true quando a liberação realmente executou.
func Relax() bool {
	relaxMu.Lock()
	if !lastRelax.IsZero() && time.Since(lastRelax) < relaxInterval {
		relaxMu.Unlock()
		return false
	}
	lastRelax = time.Now()
	relaxMu.Unlock()

	debug.FreeOSMemory()
	return true
}

// ResetRelaxState zera o controle de intervalo (usado por testes).
func ResetRelaxState() {
	relaxMu.Lock()
	lastRelax = time.Time{}
	relaxMu.Unlock()
}
