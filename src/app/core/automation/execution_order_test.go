package automation

import (
	"reflect"
	"sync"
	"testing"
	"time"
)

// Ordem de execução: ExecutionOrder crescente e, no empate, LastUpdatedAt mais
// antiga primeiro (regra definida pelo operador: "da mais antiga para a mais
// recente").
func TestOrderedExecutionTasks_SortsByOrderThenOldestUpdate(t *testing.T) {
	tasks := []AutomationTask{
		{TaskID: "script", ExecutionOrder: 40, LastUpdatedAt: "2026-01-01T00:00:00Z"},
		{TaskID: "install-novo", ExecutionOrder: 10, LastUpdatedAt: "2026-06-01T00:00:00Z"},
		{TaskID: "install-antigo", ExecutionOrder: 10, LastUpdatedAt: "2026-01-01T00:00:00Z"},
		{TaskID: "update", ExecutionOrder: 20, LastUpdatedAt: "2026-03-01T00:00:00Z"},
		{TaskID: "remover", ExecutionOrder: 30, LastUpdatedAt: "2026-02-01T00:00:00Z"},
	}

	ordered := orderedExecutionTasks(tasks)

	got := make([]string, 0, len(ordered))
	for _, task := range ordered {
		got = append(got, task.TaskID)
	}
	want := []string{"install-antigo", "install-novo", "update", "remover", "script"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ordem de execução errada: got=%v want=%v", got, want)
	}

	// A lista original não pode ser reordenada no lugar (o state é compartilhado).
	if tasks[0].TaskID != "script" {
		t.Fatalf("orderedExecutionTasks mutou a lista original: %v", tasks)
	}
}

// Sem timestamps válidos o desempate cai no TaskID, mantendo a ordem estável.
func TestOrderedExecutionTasks_FallsBackToTaskID(t *testing.T) {
	tasks := []AutomationTask{
		{TaskID: "b", ExecutionOrder: 50},
		{TaskID: "a", ExecutionOrder: 50},
	}

	ordered := orderedExecutionTasks(tasks)

	if ordered[0].TaskID != "a" || ordered[1].TaskID != "b" {
		t.Fatalf("desempate por TaskID falhou: %v", ordered)
	}
}

// A fila garante que a ORDEM DE ENFILEIRAMENTO manda, mesmo que as goroutines
// chamem waitTurn fora de ordem (é o que aconteceria com spawn concorrente).
func TestExecutionOrderQueue_RunsInEnqueueOrder(t *testing.T) {
	var queue executionOrderQueue
	for _, ticket := range []string{"a", "b", "c"} {
		queue.enqueue(ticket)
	}

	var mu sync.Mutex
	var got []string
	var wg sync.WaitGroup
	for _, ticket := range []string{"c", "b", "a"} {
		wg.Add(1)
		go func(ticket string) {
			defer wg.Done()
			queue.waitTurn(ticket)
			mu.Lock()
			got = append(got, ticket)
			mu.Unlock()
			// Segura a vez para expor quem realmente entrou primeiro.
			time.Sleep(10 * time.Millisecond)
			queue.release(ticket)
		}(ticket)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("fila de ordem de execução travou")
	}

	if !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("ordem de execução da fila errada: got=%v want=[a b c]", got)
	}
}

func TestIsPolicyOrderedTrigger(t *testing.T) {
	ordered := []TriggerType{TriggerTypeImmediate, TriggerTypeRecurring, TriggerTypeUserLogin, TriggerTypeAgentCheckIn}
	for _, trigger := range ordered {
		if !isPolicyOrderedTrigger(trigger) {
			t.Fatalf("trigger %q deveria entrar na fila ordenada", trigger)
		}
	}
	if isPolicyOrderedTrigger(TriggerTypeManual) {
		t.Fatal("trigger manual (RunNow) não deve esperar na fila da policy")
	}
}
