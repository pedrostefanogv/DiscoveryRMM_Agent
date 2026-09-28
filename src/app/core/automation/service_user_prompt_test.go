package automation

import (
	"testing"

	"discovery/app/core/database"
)

func TestResolveUserPromptTimeoutSeconds_Defaults(t *testing.T) {
	cases := []struct {
		name  string
		task  AutomationTask
		want  int
	}{
		{name: "default quando ausente", task: AutomationTask{}, want: 60},
		{name: "default quando zero", task: AutomationTask{PromptTimeoutSeconds: 0}, want: 60},
		{name: "default quando negativo", task: AutomationTask{PromptTimeoutSeconds: -5}, want: 60},
		{name: "clamp minimo", task: AutomationTask{PromptTimeoutSeconds: 1}, want: 5},
		{name: "valor valido", task: AutomationTask{PromptTimeoutSeconds: 120}, want: 120},
		{name: "clamp maximo", task: AutomationTask{PromptTimeoutSeconds: 99999}, want: 3600},
	}
	for _, tc := range cases {
		if got := resolveUserPromptTimeoutSeconds(tc.task); got != tc.want {
			t.Fatalf("%s: expected %d, got %d", tc.name, tc.want, got)
		}
	}
}

func TestResolvePSADTWelcomeOptions_FromTaskFields(t *testing.T) {
	allowDefer := false
	task := AutomationTask{
		TaskID:               "task-fields",
		ActionType:           ActionInstallPackage,
		AllowDefer:           &allowDefer,
		CloseProcesses:       []string{" winword ", "WINWORD", "excel", ""},
		PromptTimeoutSeconds: 90,
	}

	options := resolvePSADTWelcomeOptions(task)
	if options.AllowDefer {
		t.Fatalf("expected AllowDefer=false vindo da task")
	}
	if options.CloseProcessesCountdownSeconds != 90 {
		t.Fatalf("expected countdown 90s vindo do prompt, got %d", options.CloseProcessesCountdownSeconds)
	}
	if len(options.CloseProcesses) != 2 || options.CloseProcesses[0] != "winword" || options.CloseProcesses[1] != "excel" {
		t.Fatalf("expected processos normalizados (winword, excel), got %#v", options.CloseProcesses)
	}
}

func TestResolvePSADTWelcomeOptions_NilAllowDeferPreservaDefault(t *testing.T) {
	options := resolvePSADTWelcomeOptions(AutomationTask{ActionType: ActionInstallPackage})
	if !options.AllowDefer {
		t.Fatalf("expected AllowDefer=true quando a policy nao traz o campo")
	}
}

func TestDispatchExecutionNotification_UserPromptUsesWelcomeLayoutAndTimeout(t *testing.T) {
	svc := &Service{}
	var dispatched []AutomationNotificationRequest
	dispatcher := func(req AutomationNotificationRequest) AutomationNotificationResponse {
		dispatched = append(dispatched, req)
		return AutomationNotificationResponse{Accepted: true, Result: "approved"}
	}

	task := AutomationTask{
		TaskID:               "task-prompt",
		Name:                 "Install TestApp",
		ActionType:           ActionInstallPackage,
		PackageID:            "Test.App",
		RequiresApproval:     true,
		PromptTimeoutSeconds: 90,
	}
	entry := database.AutomationExecutionEntry{
		ExecutionID: "exec-prompt",
		TaskID:      "task-prompt",
		TaskName:    "Install TestApp",
		ActionType:  string(ActionInstallPackage),
		Status:      string(ExecutionStatusDispatched),
	}

	svc.dispatchExecutionNotification(dispatcher, task, entry, nil, deferState{}, resolvePSADTWelcomeOptions(task))
	if len(dispatched) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(dispatched))
	}
	if dispatched[0].Mode != "require_confirmation" {
		t.Fatalf("expected require_confirmation, got %q", dispatched[0].Mode)
	}
	if dispatched[0].Layout != "welcome" {
		t.Fatalf("expected layout welcome, got %q", dispatched[0].Layout)
	}
	if dispatched[0].TimeoutSeconds != 90 {
		t.Fatalf("expected timeout 90s, got %d", dispatched[0].TimeoutSeconds)
	}
	if dispatched[0].Metadata["promptTimeoutSeconds"] != 90 {
		t.Fatalf("expected metadata promptTimeoutSeconds=90, got %#v", dispatched[0].Metadata["promptTimeoutSeconds"])
	}
}

func TestDispatchExecutionNotification_NonPackageWithPrompt(t *testing.T) {
	svc := &Service{}
	called := false
	dispatcher := func(req AutomationNotificationRequest) AutomationNotificationResponse {
		called = true
		return AutomationNotificationResponse{Accepted: true, Result: "approved"}
	}

	task := AutomationTask{
		TaskID:           "task-script-prompt",
		Name:             "Run script",
		ActionType:       ActionRunScript,
		RequiresApproval: true,
	}
	entry := database.AutomationExecutionEntry{ExecutionID: "exec-script", TaskID: "task-script-prompt"}

	svc.dispatchExecutionNotification(dispatcher, task, entry, nil, deferState{}, resolvePSADTWelcomeOptions(task))
	if !called {
		t.Fatalf("RunScript com RequiresApproval deve notificar (pedir confirmacao)")
	}
}

// Adiar NAO pode registrar o dedup: a proxima tentativa precisa voltar a
// perguntar em vez de executar em silencio.
func TestDispatchExecutionNotification_DeferredDoesNotRecordDedup(t *testing.T) {
	svc := &Service{}
	dispatcher := func(req AutomationNotificationRequest) AutomationNotificationResponse {
		return AutomationNotificationResponse{Accepted: true, Result: "deferred"}
	}

	task := AutomationTask{
		TaskID:           "task-defer",
		Name:             "Install TestApp",
		ActionType:       ActionInstallPackage,
		PackageID:        "Test.App",
		RequiresApproval: true,
	}
	entry := database.AutomationExecutionEntry{ExecutionID: "exec-defer", TaskID: "task-defer", TaskName: "Install TestApp"}

	svc.dispatchExecutionNotification(dispatcher, task, entry, nil, deferState{}, resolvePSADTWelcomeOptions(task))

	svc.mu.Lock()
	_, exists := svc.notifDedup["task-defer"]
	svc.mu.Unlock()
	if exists {
		t.Fatalf("dedup nao deve ser gravado quando o usuario adia")
	}
}

func TestShouldDeferExecution_NonPackageAction(t *testing.T) {
	svc := &Service{}
	task := AutomationTask{ActionType: ActionRunScript}
	if !svc.shouldDeferExecution(task, AutomationNotificationResponse{Accepted: true, Result: "deferred"}) {
		t.Fatalf("RunScript deve poder adiar quando o prompt esta habilitado")
	}
	if svc.shouldDeferExecution(task, AutomationNotificationResponse{Accepted: true, Result: "timeout_policy_applied"}) {
		t.Fatalf("timeout deve CONTINUAR, nao adiar")
	}
}
