//go:build windows

package terminal

// Memória de saúde do backend ConPTY por PROCESSO.
//
// Problema de campo (2026-10-27, AORUSAXV2 com Kaspersky): o AV/EDR injeta DLLs
// nos filhos do ConPTY e TODAS as tentativas morrem com 0xC0000142. A escada de
// fallback (9 tentativas in-process + 4 no dispatcher) custava ~6,6 s em CADA
// abertura de terminal, porque cada worker era um processo novo e não lembrava
// da falha anterior.
//
// Este estado é local ao processo (o worker dura uma sessão). O SERVIÇO é quem
// transporta o hint entre workers: ele lê a linha [terminal-health] do stderr e
// injeta "conptyBlockedUntil" no payload do próximo worker.
//
// Regras:
//   - MarkConptyBlocked só deve ser chamado depois de a escada INTEIRA falhar
//     por injeção (in-process E dispatcher). Chamar dentro de tryConPTY
//     sozinho pularia um dispatcher que funcionaria.
//   - MarkConptyHealthy é chamado em QUALQUER sucesso (in-process, dispatcher
//     ou upgrade), limpando o hint.
//   - Logar somente em transição para não poluir o agent-service.log.

import (
	"log"
	"sync"
	"time"
)

// ConptyBlockedTTL é por quanto tempo a máquina "lembra" que o ConPTY acabou
// de falhar por injeção de DLL. Curto o suficiente para não mascarar uma
// correção (exclusão de AV); o upgrade em background também limpa o hint.
const ConptyBlockedTTL = 5 * time.Minute

var conptyHealth struct {
	mu           sync.RWMutex
	blockedUntil time.Time
	lastReason   string
}

// SetConptyBlockedUntil aplica um hint vindo do payload do worker (o serviço
// lembra da falha entre processos). Não loga: o serviço já logou a origem.
func SetConptyBlockedUntil(t time.Time) {
	conptyHealth.mu.Lock()
	conptyHealth.blockedUntil = t
	conptyHealth.mu.Unlock()
}

// MarkConptyBlocked registra a falha por injeção e loga a transição.
func MarkConptyBlocked(reason string) {
	until := time.Now().Add(ConptyBlockedTTL)

	conptyHealth.mu.Lock()
	wasBlocked := conptyHealth.blockedUntil.After(time.Now())
	conptyHealth.blockedUntil = until
	conptyHealth.lastReason = reason
	conptyHealth.mu.Unlock()

	if !wasBlocked {
		log.Printf("[terminal-health] conpty=blocked until=%s reason=%s", until.UTC().Format(time.RFC3339), reason)
	}
}

// MarkConptyHealthy limpa o hint e loga apenas se havia bloqueio ativo.
func MarkConptyHealthy() {
	conptyHealth.mu.Lock()
	wasBlocked := conptyHealth.blockedUntil.After(time.Now())
	conptyHealth.blockedUntil = time.Time{}
	conptyHealth.lastReason = ""
	conptyHealth.mu.Unlock()

	if wasBlocked {
		log.Printf("[terminal-health] conpty=ok")
	}
}

// ConptyBlocked informa se há hint de bloqueio ainda válido.
func ConptyBlocked() bool {
	return ConptyBlockedUntil().After(time.Now())
}

// ConptyBlockedUntil devolve o instante do hint (zero quando não há).
func ConptyBlockedUntil() time.Time {
	conptyHealth.mu.RLock()
	defer conptyHealth.mu.RUnlock()
	return conptyHealth.blockedUntil
}
