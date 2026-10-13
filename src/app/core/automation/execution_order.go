package automation

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// Ordem de execução das tarefas na policy (0–99), definida no servidor.
//
// O agent executa em ordem CRESCENTE e, quando duas tarefas têm a mesma ordem,
// roda primeiro a atualizada há mais tempo (LastUpdatedAt crescente) — "da mais
// antiga para a mais recente". O TaskID fecha o empate para a ordem ser estável
// entre ciclos de policy-sync.
//
// A execução é SERIALIZADA por executionOrderQueue: disparar tudo em goroutines
// concorrentes faria uma instalação lenta atropelar um script de ordem maior.
// Gatilho manual (RunNow) continua paralelo — quem pediu espera o resultado e
// não deve ficar preso atrás da fila da policy.

// orderedExecutionTasks devolve uma cópia das tarefas já na ordem de execução.
func orderedExecutionTasks(tasks []AutomationTask) []AutomationTask {
	ordered := make([]AutomationTask, len(tasks))
	copy(ordered, tasks)

	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.ExecutionOrder != b.ExecutionOrder {
			return a.ExecutionOrder < b.ExecutionOrder
		}

		at := parseTaskTimestamp(a.LastUpdatedAt)
		bt := parseTaskTimestamp(b.LastUpdatedAt)
		if !at.Equal(bt) {
			return at.Before(bt)
		}

		return strings.TrimSpace(a.TaskID) < strings.TrimSpace(b.TaskID)
	})

	return ordered
}

// parseTaskTimestamp interpreta o LastUpdatedAt do policy-sync (RFC3339).
// Valor ausente/inválido vira zero (a tarefa mais "antiga"), o que a mantém
// próxima do início da fila em vez de reordenar de forma aleatória.
func parseTaskTimestamp(raw string) time.Time {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// isPolicyOrderedTrigger diz se o gatilho entra na fila sequencial ordenada.
// TriggerTypeManual (RunNow) fica de fora: execução avulsa não pode esperar
// atrás de uma policy longa.
func isPolicyOrderedTrigger(trigger TriggerType) bool {
	switch trigger {
	case TriggerTypeImmediate, TriggerTypeRecurring, TriggerTypeUserLogin, TriggerTypeAgentCheckIn:
		return true
	default:
		return false
	}
}

// executionOrderQueue é uma fila FIFO de tickets que serializa as execuções da
// policy. O ticket é enfileirado SINCRONAMENTE pelo dispatcher (na ordem já
// ordenada das tarefas); a goroutine de execução espera a vez e só então roda,
// garantindo que a ordem de enfileiramento — e não o agendamento do Go — decida
// quem executa primeiro.
//
// Zero value é utilizável (o sync.Cond é criado no primeiro enqueue), então
// Services construídos em testes não precisam de inicialização.
type executionOrderQueue struct {
	mu    sync.Mutex
	cond  *sync.Cond
	queue []string
}

// enqueue registra o ticket no fim da fila e devolve imediatamente.
func (q *executionOrderQueue) enqueue(ticket string) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.cond == nil {
		q.cond = sync.NewCond(&q.mu)
	}
	q.queue = append(q.queue, ticket)
}

// waitTurn bloqueia até o ticket ser o primeiro da fila.
func (q *executionOrderQueue) waitTurn(ticket string) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.cond == nil {
		q.cond = sync.NewCond(&q.mu)
	}
	for len(q.queue) == 0 || q.queue[0] != ticket {
		q.cond.Wait()
	}
}

// release remove o ticket da frente e acorda o próximo da fila.
func (q *executionOrderQueue) release(ticket string) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.queue) > 0 && q.queue[0] == ticket {
		q.queue = q.queue[1:]
	} else {
		// Defensivo: só acontece se o chamador liberar fora da vez (bug).
		for i, queued := range q.queue {
			if queued == ticket {
				q.queue = append(q.queue[:i], q.queue[i+1:]...)
				break
			}
		}
	}

	if q.cond != nil {
		q.cond.Broadcast()
	}
}
