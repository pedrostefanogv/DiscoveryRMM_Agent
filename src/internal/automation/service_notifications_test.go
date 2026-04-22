package automation

import (
	"testing"

	"discovery/internal/database"
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
