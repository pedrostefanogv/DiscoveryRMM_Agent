package automation

import (
	"context"
	"testing"
	"time"

	"discovery/app/core/database"
)

// newWatchdogService cria um Service com DB real (SQLite em tempdir) e uma
// policy minima, necessaria para recalcular as chaves dos marcadores de trigger.
func newWatchdogService(t *testing.T, tasks []AutomationTask, fingerprint string) (*Service, *database.DB) {
	t.Helper()
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	svc := NewService(func() RuntimeConfig { return RuntimeConfig{AgentID: "agent-1"} }, nil)
	svc.SetDB(db)
	svc.mu.Lock()
	svc.state.Tasks = tasks
	svc.state.PolicyFingerprint = fingerprint
	svc.mu.Unlock()
	return svc, db
}

func braveTask() AutomationTask {
	return AutomationTask{
		TaskID:           "01a0e5dc-375d-7f9e-9663-af372667f3fa",
		Name:             "Brave",
		ActionType:       ActionInstallPackage,
		InstallationType: InstallationWinget,
		PackageID:        "brave.brave",
		TriggerImmediate: true,
		LastUpdatedAt:    "2026-09-28T15:38:56.170575Z",
	}
}

// Caso Brave: a unica execucao ficou em "Dispatched" (started_at preenchido,
// finished_at NULL) e o marcador immediate continuou gravado. O watchdog precisa
// encerrar a linha E liberar o gatilho — sem isso a task nunca mais e disparada.
func TestWatchdogReconcilesStaleExecutionAndReleasesTrigger(t *testing.T) {
	task := braveTask()
	svc, db := newWatchdogService(t, []AutomationTask{task}, "fp-brave")

	stale := database.AutomationExecutionEntry{
		ExecutionID: "8246f53e-0667-4c79-a71b-5853cd8a00e4",
		AgentID:     "agent-1",
		TaskID:      task.TaskID,
		TaskName:    task.Name,
		ActionType:  string(task.ActionType),
		Status:      string(ExecutionStatusDispatched),
		StartedAt:   time.Now().UTC().Add(-2 * time.Hour),
		PackageID:   task.PackageID,
	}
	if err := db.UpsertAutomationExecution(stale); err != nil {
		t.Fatalf("upsert stale: %v", err)
	}
	markerKey := "immediate:" + "fp-brave" + ":" + task.TaskID + ":" + task.LastUpdatedAt
	if err := db.SetAutomationMarker("agent-1", markerKey, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("set marker: %v", err)
	}

	svc.reconcileStaleExecutions("agent-1")

	entries, err := db.ListRecentAutomationExecutions("agent-1", 10)
	if err != nil {
		t.Fatalf("list executions: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("esperava 1 execucao, obtido %d", len(entries))
	}
	got := entries[0]
	if got.Status != string(ExecutionStatusFailed) {
		t.Fatalf("status terminal esperado Failed, obtido %q", got.Status)
	}
	if got.FinishedAt.IsZero() {
		t.Fatal("watchdog precisa preencher finished_at")
	}
	if got.Success {
		t.Fatal("execucao interrompida nao pode ser marcada como sucesso")
	}

	if _, found, err := db.GetAutomationMarker("agent-1", markerKey); err != nil || found {
		t.Fatalf("marcador de trigger deveria ter sido liberado (found=%v err=%v)", found, err)
	}
}

// Marcador de dedup dos gatilhos precisa bater EXATAMENTE com o formato gravado
// por triggerImmediate/triggerOnAgentCheckIn — senao a liberacao nao surte efeito.
func TestTriggerMarkerKeysMatchTriggerFormat(t *testing.T) {
	task := AutomationTask{
		TaskID:                "t1",
		LastUpdatedAt:         "2026-09-28T15:38:56.170575Z",
		TriggerImmediate:      true,
		TriggerOnAgentCheckIn: true,
	}
	keys := triggerMarkerKeysForTask("fp", task)
	want := []string{
		"immediate:fp:t1:2026-09-28T15:38:56.170575Z",
		"checkin:fp:t1:2026-09-28T15:38:56.170575Z",
	}
	if len(keys) != len(want) {
		t.Fatalf("esperava %d chaves, obtido %d (%v)", len(want), len(keys), keys)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("chave %d: esperado %q, obtido %q", i, want[i], keys[i])
		}
	}

	// Task sem gatilhos nao gera chave alguma (nao ha dedup a liberar).
	if got := triggerMarkerKeysForTask("fp", AutomationTask{TaskID: "t2"}); len(got) != 0 {
		t.Fatalf("task sem gatilhos nao deveria gerar chaves: %v", got)
	}
}

// Um defer do usuario NAO pode deixar a linha em "Dispatched": o retry ja e
// agendado, entao o estado terminal e Deferred (com finished_at).
func TestDeferredExecutionDoesNotStayDispatched(t *testing.T) {
	task := braveTask()
	task.RequiresApproval = true
	task.NotificationMode = NotificationModePrompt
	svc, db := newWatchdogService(t, []AutomationTask{task}, "fp-defer")

	svc.SetNotificationDispatcher(func(req AutomationNotificationRequest) AutomationNotificationResponse {
		return AutomationNotificationResponse{Accepted: true, Result: "deferred", AgentAction: "user_decision"}
	})

	svc.executeTaskAsync(context.Background(), "agent-1", task, ExecutionSourceForceSync, TriggerTypeImmediate, nil)

	deadline := time.Now().Add(5 * time.Second)
	var status string
	var finishedAt time.Time
	for time.Now().Before(deadline) {
		entries, err := db.ListRecentAutomationExecutions("agent-1", 5)
		if err != nil {
			t.Fatalf("list executions: %v", err)
		}
		for _, entry := range entries {
			if entry.TaskID != task.TaskID {
				continue
			}
			status = entry.Status
			finishedAt = entry.FinishedAt
		}
		if status == string(ExecutionStatusDeferred) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if status != string(ExecutionStatusDeferred) {
		t.Fatalf("execucao adiada deveria terminar como Deferred, obtido %q", status)
	}
	if finishedAt.IsZero() {
		t.Fatal("execucao adiada precisa de finished_at (nao pode ficar em Dispatched)")
	}
}

// O teto duro e derivado da policy, SEM limite superior fixo: truncar aqui
// cancelaria o ctx externo ANTES do timeout interno do executor PSADT (que
// aplica timeoutAction), suprimindo o tratamento de timeout do servidor.
func TestResolveExecutionHardTimeout(t *testing.T) {
	if got := resolveExecutionHardTimeout(PSADTPolicy{}); got != executionHardTimeoutMin {
		t.Fatalf("sem policy deveria usar o minimo, obtido %s", got)
	}
	if got := resolveExecutionHardTimeout(PSADTPolicy{ExecutionTimeoutSeconds: 1800}); got != 30*time.Minute+executionHardTimeoutMargin {
		t.Fatalf("1800s + margem, obtido %s", got)
	}
	// Policy ACIMA do antigo teto de 60 min nao pode ser truncada.
	want := 2*time.Hour + executionHardTimeoutMargin
	if got := resolveExecutionHardTimeout(PSADTPolicy{ExecutionTimeoutSeconds: 7200}); got != want {
		t.Fatalf("policy de 7200s deveria dar %s, obtido %s", want, got)
	}
	if got := resolveExecutionHardTimeout(PSADTPolicy{ExecutionTimeoutSeconds: 5}); got != 5*time.Second+executionHardTimeoutMargin {
		t.Fatalf("policy de 5s deveria dar 5s+margem, obtido %s", got)
	}
}

// O circuit breaker passou a valer tambem para tarefas Immediate: agora que uma
// falha libera o gatilho, sem este portao a task seria redisparada a cada sync.
func TestCircuitBreakerPaused(t *testing.T) {
	task := braveTask()
	svc, db := newWatchdogService(t, []AutomationTask{task}, "fp-cb")

	if svc.circuitBreakerPaused("agent-1", task.TaskID) {
		t.Fatal("sem marker a task nao pode estar pausada")
	}

	cb := circuitBreakerState{
		Failures:   failureThreshold,
		LastFailAt: time.Now().UTC(),
		OpenUntil:  time.Now().UTC().Add(time.Hour),
	}
	if err := db.SetAutomationMarker("agent-1", "circuit:fail:"+task.TaskID, encodeCircuitBreakerState(cb)); err != nil {
		t.Fatalf("set circuit marker: %v", err)
	}
	if !svc.circuitBreakerPaused("agent-1", task.TaskID) {
		t.Fatal("marker aberto deveria pausar a task")
	}

	// Circuit breaker fechado (OpenUntil no passado) nao pausa.
	cb.OpenUntil = time.Now().UTC().Add(-time.Minute)
	if err := db.SetAutomationMarker("agent-1", "circuit:fail:"+task.TaskID, encodeCircuitBreakerState(cb)); err != nil {
		t.Fatalf("set circuit marker: %v", err)
	}
	if svc.circuitBreakerPaused("agent-1", task.TaskID) {
		t.Fatal("circuit breaker fechado nao pode pausar a task")
	}
}

// Uma execucao EM VOO neste processo nunca pode ser reconciliada pelo
// watchdog: o pior caso legitimo (prompt de ate 60 min + execucao de ate
// 60 min) passa do cutoff e seria encerrado como falha, liberando o gatilho e
// disparando uma execucao duplicada concorrente.
func TestWatchdogSkipsInFlightExecution(t *testing.T) {
	task := braveTask()
	svc, db := newWatchdogService(t, []AutomationTask{task}, "fp-inflight")

	entry := database.AutomationExecutionEntry{
		ExecutionID: "exec-in-flight",
		AgentID:     "agent-1",
		TaskID:      task.TaskID,
		Status:      string(ExecutionStatusDispatched),
		StartedAt:   time.Now().UTC().Add(-2 * time.Hour),
	}
	if err := db.UpsertAutomationExecution(entry); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	svc.mu.Lock()
	svc.activeExecutions["exec-in-flight"] = true
	svc.mu.Unlock()

	svc.reconcileStaleExecutions("agent-1")

	entries, err := db.ListRecentAutomationExecutions("agent-1", 5)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("esperava 1 execucao, obtido %d", len(entries))
	}
	if entries[0].Status != string(ExecutionStatusDispatched) {
		t.Fatalf("execucao em voo nao pode ser reconciliada, status=%q", entries[0].Status)
	}
}

// Linha Deferred HISTORICA nao pode liberar o gatilho. Depois de um adiamento
// que concluiu com sucesso, o marcador segue armado (correto) e o registro
// Deferred permanece no historico para sempre; liberar o gatilho a cada
// restart re-executaria uma task ja concluida (para RunScript, rodaria o
// script de novo). A retomada de adiamento e responsabilidade de
// resumePendingDefers, que usa o estado de defer PERSISTIDO.
func TestWatchdogIgnoresHistoricalDeferredRow(t *testing.T) {
	task := braveTask()
	svc, db := newWatchdogService(t, []AutomationTask{task}, "fp-deferred")

	entry := database.AutomationExecutionEntry{
		ExecutionID: "exec-deferred",
		AgentID:     "agent-1",
		TaskID:      task.TaskID,
		Status:      string(ExecutionStatusDeferred),
		StartedAt:   time.Now().UTC().Add(-2 * time.Hour),
		FinishedAt:  time.Now().UTC().Add(-9 * time.Minute),
	}
	if err := db.UpsertAutomationExecution(entry); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	markerKey := "immediate:" + "fp-deferred" + ":" + task.TaskID + ":" + task.LastUpdatedAt
	if err := db.SetAutomationMarker("agent-1", markerKey, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("set marker: %v", err)
	}

	svc.reconcileStaleExecutions("agent-1")

	entries, err := db.ListRecentAutomationExecutions("agent-1", 5)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("esperava 1 execucao, obtido %d", len(entries))
	}
	if entries[0].Status != string(ExecutionStatusDeferred) {
		t.Fatalf("status do adiamento nao deveria ser reescrito, obtido %q", entries[0].Status)
	}
	if _, found, err := db.GetAutomationMarker("agent-1", markerKey); err != nil || !found {
		t.Fatalf("marcador de task concluida NAO pode ser liberado (found=%v err=%v)", found, err)
	}
}

// Sem policy carregada nao da para recalcular a chave do marcador de trigger.
// O watchdog deve ESPERAR: marcar a linha como terminal a tiraria do conjunto
// e o marcador ficaria armado para sempre, sem retry.
func TestWatchdogWaitsWhenPolicyNotLoaded(t *testing.T) {
	svc, db := newWatchdogService(t, nil, "")

	entry := database.AutomationExecutionEntry{
		ExecutionID: "exec-no-policy",
		AgentID:     "agent-1",
		TaskID:      "task-sem-policy",
		Status:      string(ExecutionStatusDispatched),
		StartedAt:   time.Now().UTC().Add(-2 * time.Hour),
	}
	if err := db.UpsertAutomationExecution(entry); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	svc.reconcileStaleExecutions("agent-1")

	entries, err := db.ListRecentAutomationExecutions("agent-1", 5)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("esperava 1 execucao, obtido %d", len(entries))
	}
	if entries[0].Status != string(ExecutionStatusDispatched) {
		t.Fatalf("sem policy o watchdog nao pode encerrar a linha, status=%q", entries[0].Status)
	}
}

// resumePendingDefers: agenda UM timer por task adiada (sem multiplicacao) e
// libera o gatilho quando a janela de adiamento ja se esgotou sem retry.
func TestResumePendingDefers(t *testing.T) {
	task := braveTask()
	svc, db := newWatchdogService(t, []AutomationTask{task}, "fp-resume")
	markerKey := "immediate:fp-resume:" + task.TaskID + ":" + task.LastUpdatedAt
	if err := db.SetAutomationMarker("agent-1", markerKey, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("set marker: %v", err)
	}

	// (a) adiamento ainda pendente: agenda o retry e NAO toca no gatilho.
	svc.mu.Lock()
	svc.deferByTask[task.TaskID] = deferState{Count: 1, NextAttempt: time.Now().UTC().Add(30 * time.Minute)}
	svc.mu.Unlock()
	svc.resumePendingDefers("agent-1")

	svc.mu.RLock()
	timer := svc.deferTimers[task.TaskID]
	svc.mu.RUnlock()
	if timer == nil {
		t.Fatal("esperava timer de retry agendado para o adiamento pendente")
	}
	if _, found, err := db.GetAutomationMarker("agent-1", markerKey); err != nil || !found {
		t.Fatalf("gatilho nao pode ser liberado com adiamento pendente (found=%v err=%v)", found, err)
	}

	// (b) janela esgotada (sem NextAttempt): a task deve executar.
	svc.mu.Lock()
	svc.deferByTask[task.TaskID] = deferState{Count: 3, Exhausted: true}
	svc.mu.Unlock()
	svc.resumePendingDefers("agent-1")
	if _, found, err := db.GetAutomationMarker("agent-1", markerKey); err != nil || found {
		t.Fatalf("adiamento esgotado deveria liberar o gatilho (found=%v err=%v)", found, err)
	}

	svc.mu.Lock()
	if tm := svc.deferTimers[task.TaskID]; tm != nil {
		tm.Stop()
	}
	svc.mu.Unlock()
}

// O corte do watchdog é SEMPRE por tempo: no startup NÃO se reconcilia "tudo o
// que existe". Com duas instâncias de core no mesmo agentID/DB (ex.: serviço
// rodado manualmente fora do SCM, ou sobreposição durante restart/self-update),
// reconciliar tudo no boot encerraria a execução EM VOO do processo antigo e
// liberaria o gatilho — disparando uma execução duplicada concorrente. Uma
// linha recente fica intacta e é recuperada quando envelhecer.
func TestWatchdogIgnoresRecentExecution(t *testing.T) {
	task := braveTask()
	svc, db := newWatchdogService(t, []AutomationTask{task}, "fp-recent")

	entry := database.AutomationExecutionEntry{
		ExecutionID: "exec-recent",
		AgentID:     "agent-1",
		TaskID:      task.TaskID,
		Status:      string(ExecutionStatusDispatched),
		StartedAt:   time.Now().UTC().Add(-10 * time.Minute),
	}
	if err := db.UpsertAutomationExecution(entry); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	markerKey := "immediate:fp-recent:" + task.TaskID + ":" + task.LastUpdatedAt
	if err := db.SetAutomationMarker("agent-1", markerKey, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("set marker: %v", err)
	}

	svc.reconcileStaleExecutions("agent-1")

	entries, err := db.ListRecentAutomationExecutions("agent-1", 5)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("esperava 1 execucao, obtido %d", len(entries))
	}
	if entries[0].Status != string(ExecutionStatusDispatched) {
		t.Fatalf("execucao recente nao pode ser reconciliada, status=%q", entries[0].Status)
	}
	if _, found, err := db.GetAutomationMarker("agent-1", markerKey); err != nil || !found {
		t.Fatalf("marcador de execucao recente nao pode ser liberado (found=%v err=%v)", found, err)
	}
}
