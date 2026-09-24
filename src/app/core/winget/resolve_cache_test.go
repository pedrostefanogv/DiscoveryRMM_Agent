package winget

import (
	"sync"
	"testing"
	"time"
)

// ── Cache de resolução: TTL e concorrência ──

// TestResolveExecutable_ConcurrentWithInvalidate garante que invalidar o cache
// enquanto outra goroutine resolve não gera data race (o par de globais era
// escrito sem lock antes da correção).
func TestResolveExecutable_ConcurrentWithInvalidate(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = ResolveExecutable()
		}()
		go func() {
			defer wg.Done()
			InvalidateResolvedExecutable()
		}()
	}
	wg.Wait()
}

// TestResolveExecutable_ConcurrentFirstCall garante que a primeira resolução
// concorrente é feita uma única vez e todos recebem o mesmo resultado.
func TestResolveExecutable_ConcurrentFirstCall(t *testing.T) {
	InvalidateResolvedExecutable()

	var wg sync.WaitGroup
	results := make([]string, 16)
	origins := make([]string, 16)
	for i := 0; i < len(results); i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], origins[idx] = ResolveExecutable()
		}(i)
	}
	wg.Wait()

	for i := 1; i < len(results); i++ {
		if results[i] != results[0] || origins[i] != origins[0] {
			t.Fatalf("resultados divergentes: [0]=%q/%q [%d]=%q/%q",
				results[0], origins[0], i, results[i], origins[i])
		}
	}
}

// TestResolveExecutable_ReResolvesAfterTTL garante que o cache expira: sem TTL,
// um "não encontrado" no boot ficaria congelado e o agente nunca passaria a
// enxergar um winget instalado depois.
func TestResolveExecutable_ReResolvesAfterTTL(t *testing.T) {
	InvalidateResolvedExecutable()
	if _, _ = ResolveExecutable(); true {
		// Primeira resolução popula o cache.
	}

	// Envelhece o carimbo além do TTL.
	wingetMu.Lock()
	wingetAt = time.Now().Add(-(resolvedTTL + time.Minute))
	before := wingetAt
	wingetMu.Unlock()

	// Nova chamada deve re-resolver (atualizando o carimbo).
	_, _ = ResolveExecutable()

	wingetMu.RLock()
	after := wingetAt
	wingetMu.RUnlock()

	if !after.After(before) {
		t.Fatalf("o cache nao foi renovado apos o TTL (at=%v)", after)
	}
}

// TestResolvedTTL_IsPositive guarda contra um TTL zerado (que faria o resolver
// rodar toda a cadeia — incluindo subprocessos — em cada chamada).
func TestResolvedTTL_IsPositive(t *testing.T) {
	if resolvedTTL <= 0 {
		t.Fatalf("resolvedTTL deve ser positivo, veio %v", resolvedTTL)
	}
	if resolvedTTL > time.Hour {
		t.Fatalf("resolvedTTL alto demais (%v): um winget instalado demoraria demais a ser visto", resolvedTTL)
	}
}
