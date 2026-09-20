package automation

import (
	"testing"
	"time"

	"discovery/app/core/database"
)

func TestDispatchExecutionNotification_PackageTaskStart(t *testing.T) {
	svc := &Service{}
	var dispatched []AutomationNotificationRequest
	dispatcher := func(req AutomationNotificationRequest) AutomationNotificationResponse {
		dispatched = append(dispatched, req)
		return AutomationNotificationResponse{Accepted: true, Result: "approved"}
	}

	task := AutomationTask{
		TaskID:           "task-1",
		Name:             "Install TestApp",
		ActionType:       ActionInstallPackage,
		PackageID:        "Test.App",
		InstallationType: InstallationPSAppDeployToolkit,
		RequiresApproval: true,
	}
	entry := database.AutomationExecutionEntry{
		ExecutionID:      "exec-1",
		TaskID:           "task-1",
		TaskName:         "Install TestApp",
		ActionType:       string(ActionInstallPackage),
		InstallationType: string(InstallationPSAppDeployToolkit),
		Status:           string(ExecutionStatusDispatched),
		PackageID:        "Test.App",
		CorrelationID:    "corr-1",
	}

	svc.dispatchExecutionNotification(dispatcher, task, entry, nil, deferState{}, resolvePSADTWelcomeOptions(task))

	if len(dispatched) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(dispatched))
	}
	if dispatched[0].EventType != "install_start" {
		t.Fatalf("expected eventType install_start, got %q", dispatched[0].EventType)
	}
	if dispatched[0].Mode != "require_confirmation" {
		t.Fatalf("expected mode require_confirmation, got %q", dispatched[0].Mode)
	}
}

// C1: sem RequiresApproval, install_start não deve pedir confirmação.
func TestDispatchExecutionNotification_PackageTaskStartNoApproval(t *testing.T) {
	svc := &Service{}
	var dispatched []AutomationNotificationRequest
	dispatcher := func(req AutomationNotificationRequest) AutomationNotificationResponse {
		dispatched = append(dispatched, req)
		return AutomationNotificationResponse{Accepted: true, Result: "approved"}
	}

	task := AutomationTask{
		TaskID:     "task-noapproval",
		Name:       "Install TestApp",
		ActionType: ActionInstallPackage,
		PackageID:  "Test.App",
	}
	entry := database.AutomationExecutionEntry{
		ExecutionID:   "exec-noapproval",
		TaskID:        "task-noapproval",
		ActionType:    string(ActionInstallPackage),
		Status:        string(ExecutionStatusDispatched),
		CorrelationID: "corr-na",
	}

	svc.dispatchExecutionNotification(dispatcher, task, entry, nil, deferState{}, resolvePSADTWelcomeOptions(task))
	if len(dispatched) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(dispatched))
	}
	if dispatched[0].Mode != "notify_only" {
		t.Fatalf("expected mode notify_only, got %q", dispatched[0].Mode)
	}
}

// C1: resultados (install_end/failed/reboot) nunca pedem confirmação,
// mesmo quando RequiresApproval=true.
func TestDispatchExecutionNotification_ResultAlwaysNotifyOnly(t *testing.T) {
	svc := &Service{}
	var dispatched []AutomationNotificationRequest
	dispatcher := func(req AutomationNotificationRequest) AutomationNotificationResponse {
		dispatched = append(dispatched, req)
		return AutomationNotificationResponse{Accepted: true, Result: "approved"}
	}

	task := AutomationTask{
		TaskID:           "task-result",
		Name:             "Install TestApp",
		ActionType:       ActionInstallPackage,
		PackageID:        "Test.App",
		RequiresApproval: true,
	}
	entry := database.AutomationExecutionEntry{
		ExecutionID:   "exec-result",
		TaskID:        "task-result",
		ActionType:    string(ActionInstallPackage),
		Status:        string(ExecutionStatusCompleted),
		CorrelationID: "corr-r",
	}

	success := ExecutionResult{Success: true, ExitCodeSet: true, ExitCode: 0}
	svc.dispatchExecutionNotification(dispatcher, task, entry, &success, deferState{}, resolvePSADTWelcomeOptions(task))
	if len(dispatched) != 1 || dispatched[0].EventType != "install_end" {
		t.Fatalf("expected success notification install_end")
	}
	if dispatched[0].Mode != "notify_only" {
		t.Fatalf("expected result mode notify_only, got %q", dispatched[0].Mode)
	}
}

func TestDispatchExecutionNotification_PackageTaskResult(t *testing.T) {
	svc := &Service{}
	var dispatched []AutomationNotificationRequest
	dispatcher := func(req AutomationNotificationRequest) AutomationNotificationResponse {
		dispatched = append(dispatched, req)
		return AutomationNotificationResponse{Accepted: true, Result: "approved"}
	}

	task := AutomationTask{
		TaskID:     "task-2",
		Name:       "Install TestApp",
		ActionType: ActionInstallPackage,
		PackageID:  "Test.App",
	}
	entry := database.AutomationExecutionEntry{
		ExecutionID:   "exec-2",
		TaskID:        "task-2",
		TaskName:      "Install TestApp",
		ActionType:    string(ActionInstallPackage),
		Status:        string(ExecutionStatusCompleted),
		PackageID:     "Test.App",
		CorrelationID: "corr-2",
	}

	success := ExecutionResult{Success: true, ExitCodeSet: true, ExitCode: 0}
	svc.dispatchExecutionNotification(dispatcher, task, entry, &success, deferState{}, resolvePSADTWelcomeOptions(task))
	if len(dispatched) != 1 || dispatched[0].EventType != "install_end" {
		t.Fatalf("expected success notification install_end")
	}

	dispatched = nil
	reboot := ExecutionResult{Success: true, ExitCodeSet: true, ExitCode: 3010}
	svc.dispatchExecutionNotification(dispatcher, task, entry, &reboot, deferState{}, resolvePSADTWelcomeOptions(task))
	if len(dispatched) != 1 || dispatched[0].EventType != "reboot_required" {
		t.Fatalf("expected reboot notification reboot_required")
	}

	dispatched = nil
	failed := ExecutionResult{Success: false, ExitCodeSet: true, ExitCode: 1603, ErrorMessage: "falha"}
	svc.dispatchExecutionNotification(dispatcher, task, entry, &failed, deferState{}, resolvePSADTWelcomeOptions(task))
	if len(dispatched) != 1 || dispatched[0].EventType != "install_failed" {
		t.Fatalf("expected failure notification install_failed")
	}
}

func TestDispatchExecutionNotification_NonPackageActionIgnored(t *testing.T) {
	svc := &Service{}
	called := false
	dispatcher := func(req AutomationNotificationRequest) AutomationNotificationResponse {
		called = true
		return AutomationNotificationResponse{Accepted: true, Result: "approved"}
	}

	task := AutomationTask{
		TaskID:     "task-3",
		Name:       "Run script",
		ActionType: ActionRunScript,
	}
	entry := database.AutomationExecutionEntry{ExecutionID: "exec-3"}

	svc.dispatchExecutionNotification(dispatcher, task, entry, nil, deferState{}, resolvePSADTWelcomeOptions(task))
	if called {
		t.Fatalf("expected no notification for non-package action")
	}
}

func TestResolvePSADTWelcomeOptions_FromPayload(t *testing.T) {
	task := AutomationTask{
		TaskID:         "task-welcome",
		ActionType:     ActionInstallPackage,
		CommandPayload: `{"psadtWelcome":{"allowDefer":true,"deferTimes":5,"deferDays":2,"deferRunIntervalSeconds":1200,"deferDeadline":"2026-01-01T10:00:00Z","closeProcesses":["winword","excel"],"blockExecution":true,"checkDiskSpace":true,"requiredDiskSpaceMb":2048}}`,
	}

	options := resolvePSADTWelcomeOptions(task)
	if options.DeferTimes != 5 {
		t.Fatalf("expected deferTimes=5, got %d", options.DeferTimes)
	}
	if options.DeferDays != 2 {
		t.Fatalf("expected deferDays=2, got %v", options.DeferDays)
	}
	if int(options.DeferRunInterval.Seconds()) != 1200 {
		t.Fatalf("expected interval 1200s, got %v", options.DeferRunInterval)
	}
	if options.DeferDeadline.IsZero() {
		t.Fatalf("expected deferDeadline to be parsed")
	}
	if len(options.CloseProcesses) != 2 {
		t.Fatalf("expected closeProcesses with 2 items")
	}
	if !options.BlockExecution || !options.CheckDiskSpace || options.RequiredDiskSpaceMB != 2048 {
		t.Fatalf("expected blockExecution/checkDiskSpace/requiredDiskSpaceMb from payload")
	}
}

func TestShouldDeferExecution_WhenDeferredResult(t *testing.T) {
	svc := &Service{}
	task := AutomationTask{ActionType: ActionInstallPackage}

	if !svc.shouldDeferExecution(task, AutomationNotificationResponse{Accepted: true, Result: "deferred"}) {
		t.Fatalf("expected defer decision to be honored")
	}
	if svc.shouldDeferExecution(task, AutomationNotificationResponse{Accepted: true, Result: "approved"}) {
		t.Fatalf("expected approved to not defer")
	}
}

// ── Dedup de notificações por ciclo de check-in (spam laranja na UI) ──────
// Regressão: uma tarefa de instalação que sempre falha (ex.: winget ausente
// no contexto do serviço) era re-executada a cada ciclo de check-in e gerava
// install_start + install_failed a cada ~3 min, repetidamente.

func TestAutomationExecutionNotificationAllowed_DedupRules(t *testing.T) {
	now := time.Now().UTC()
	fresh := automationNotifDedupState{lastEventType: "install_start", lastAt: now.Add(-2 * time.Minute)}

	if !automationExecutionNotificationAllowed(automationNotifDedupState{}, false, "install_start", now) {
		t.Fatalf("primeira execução deve notificar")
	}
	if !automationExecutionNotificationAllowed(fresh, true, "install_failed", now) {
		t.Fatalf("falha após start deve notificar (transição)")
	}

	failed := automationNotifDedupState{lastEventType: "install_failed", lastAt: now.Add(-2 * time.Minute)}
	if automationExecutionNotificationAllowed(failed, true, "install_failed", now) {
		t.Fatalf("falha repetida dentro de 24h deve ser suprimida")
	}
	if automationExecutionNotificationAllowed(failed, true, "install_start", now) {
		t.Fatalf("start de tarefa em loop de falha deve ser suprimido dentro de 6h")
	}
	if !automationExecutionNotificationAllowed(failed, true, "install_end", now) {
		t.Fatalf("install_end nunca deve ser suprimido")
	}
	if !automationExecutionNotificationAllowed(failed, true, "reboot_required", now) {
		t.Fatalf("reboot_required nunca deve ser suprimido")
	}

	oldFailed := automationNotifDedupState{lastEventType: "install_failed", lastAt: now.Add(-25 * time.Hour)}
	if !automationExecutionNotificationAllowed(oldFailed, true, "install_failed", now) {
		t.Fatalf("falha após 24h deve notificar novamente")
	}
	oldStart := automationNotifDedupState{lastEventType: "install_start", lastAt: now.Add(-7 * time.Hour)}
	if !automationExecutionNotificationAllowed(oldStart, true, "install_start", now) {
		t.Fatalf("start após 6h deve notificar novamente")
	}

	ended := automationNotifDedupState{lastEventType: "install_end", lastAt: now.Add(-2 * time.Minute)}
	if !automationExecutionNotificationAllowed(ended, true, "install_start", now) {
		t.Fatalf("start após conclusão (nova rodada) deve notificar")
	}
}

func TestDispatchExecutionNotification_SuppressesRepeatedCycles(t *testing.T) {
	svc := &Service{}
	var dispatched []AutomationNotificationRequest
	dispatcher := func(req AutomationNotificationRequest) AutomationNotificationResponse {
		dispatched = append(dispatched, req)
		return AutomationNotificationResponse{Accepted: true, Result: "approved"}
	}

	task := AutomationTask{
		TaskID:     "task-loop",
		Name:       "Install Thunderbird",
		ActionType: ActionInstallPackage,
		PackageID:  "mozilla.thunderbird",
	}
	newEntry := func(execID string) database.AutomationExecutionEntry {
		return database.AutomationExecutionEntry{
			ExecutionID:   execID,
			TaskID:        "task-loop",
			TaskName:      task.Name,
			ActionType:    string(ActionInstallPackage),
			Status:        string(ExecutionStatusDispatched),
			PackageID:     task.PackageID,
			CorrelationID: "corr-" + execID,
		}
	}

	// Ciclo 1: start + falha → 2 notificações
	failed := ExecutionResult{Success: false, ExitCodeSet: true, ExitCode: 1603}
	svc.dispatchExecutionNotification(dispatcher, task, newEntry("exec-1"), nil, deferState{}, resolvePSADTWelcomeOptions(task))
	svc.dispatchExecutionNotification(dispatcher, task, newEntry("exec-1"), &failed, deferState{}, resolvePSADTWelcomeOptions(task))
	if len(dispatched) != 2 {
		t.Fatalf("ciclo 1: esperado 2 notificações (start+failed), got %d", len(dispatched))
	}

	// Ciclo 2 (check-in repetido): start e falha suprimidos
	svc.dispatchExecutionNotification(dispatcher, task, newEntry("exec-2"), nil, deferState{}, resolvePSADTWelcomeOptions(task))
	svc.dispatchExecutionNotification(dispatcher, task, newEntry("exec-2"), &failed, deferState{}, resolvePSADTWelcomeOptions(task))
	if len(dispatched) != 2 {
		t.Fatalf("ciclo 2: esperado 0 notificações novas (dedup), got %d", len(dispatched)-2)
	}

	// Resultado positivo sempre passa
	success := ExecutionResult{Success: true, ExitCodeSet: true, ExitCode: 0}
	svc.dispatchExecutionNotification(dispatcher, task, newEntry("exec-3"), &success, deferState{}, resolvePSADTWelcomeOptions(task))
	if len(dispatched) != 3 {
		t.Fatalf("install_end nunca deve ser suprimido")
	}
}
