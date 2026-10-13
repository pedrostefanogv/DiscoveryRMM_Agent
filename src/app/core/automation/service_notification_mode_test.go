package automation

import (
	"testing"

	"discovery/app/core/database"
)

func notifTask(mode, timing string) AutomationTask {
	return AutomationTask{
		TaskID:           "task-" + mode + timing,
		Name:             "Install TestApp",
		ActionType:       ActionInstallPackage,
		PackageID:        "Test.App",
		NotificationMode: mode,
		ToastTiming:      timing,
	}
}

func notifEntry(execID string) database.AutomationExecutionEntry {
	return database.AutomationExecutionEntry{
		ExecutionID: execID,
		TaskID:      "task-notif",
		TaskName:    "Install TestApp",
		ActionType:  string(ActionInstallPackage),
		Status:      string(ExecutionStatusDispatched),
		PackageID:   "Test.App",
	}
}

// Silent suprime inicio e resultado, mesmo em acao de pacote.
func TestDispatch_NotificationModeSilent_SuppressesAll(t *testing.T) {
	svc := &Service{}
	dispatched := 0
	dispatcher := func(req AutomationNotificationRequest) AutomationNotificationResponse {
		dispatched++
		return AutomationNotificationResponse{Accepted: true, Result: "approved"}
	}
	task := notifTask(NotificationModeSilent, ToastTimingAfter)
	success := ExecutionResult{Success: true, ExitCodeSet: true, ExitCode: 0}

	svc.dispatchExecutionNotification(dispatcher, task, notifEntry("s1"), nil, deferState{}, resolvePSADTWelcomeOptions(task))
	svc.dispatchExecutionNotification(dispatcher, task, notifEntry("s2"), &success, deferState{}, resolvePSADTWelcomeOptions(task))

	if dispatched != 0 {
		t.Fatalf("Silent deve suprimir tudo, got %d notificacoes", dispatched)
	}
}

// Toast + Before: avisa no inicio e NAO repete no resultado.
func TestDispatch_NotificationModeToastBefore_OnlyStart(t *testing.T) {
	svc := &Service{}
	var dispatched []AutomationNotificationRequest
	dispatcher := func(req AutomationNotificationRequest) AutomationNotificationResponse {
		dispatched = append(dispatched, req)
		return AutomationNotificationResponse{Accepted: true, Result: "approved"}
	}
	task := notifTask(NotificationModeToast, ToastTimingBefore)
	success := ExecutionResult{Success: true, ExitCodeSet: true, ExitCode: 0}

	svc.dispatchExecutionNotification(dispatcher, task, notifEntry("b1"), nil, deferState{}, resolvePSADTWelcomeOptions(task))
	svc.dispatchExecutionNotification(dispatcher, task, notifEntry("b2"), &success, deferState{}, resolvePSADTWelcomeOptions(task))

	if len(dispatched) != 1 {
		t.Fatalf("Toast Before: esperado 1 notificacao (inicio), got %d", len(dispatched))
	}
	if dispatched[0].EventType != "install_start" || dispatched[0].Mode != "notify_only" || dispatched[0].Layout != "toast" {
		t.Fatalf("Toast Before: esperado install_start/notify_only/toast, got %s/%s/%s", dispatched[0].EventType, dispatched[0].Mode, dispatched[0].Layout)
	}
	if dispatched[0].Metadata["notificationMode"] != NotificationModeToast {
		t.Fatalf("metadata notificationMode=%v", dispatched[0].Metadata["notificationMode"])
	}
}

// Toast + After: nao avisa no inicio, avisa so no resultado.
func TestDispatch_NotificationModeToastAfter_OnlyResult(t *testing.T) {
	svc := &Service{}
	var dispatched []AutomationNotificationRequest
	dispatcher := func(req AutomationNotificationRequest) AutomationNotificationResponse {
		dispatched = append(dispatched, req)
		return AutomationNotificationResponse{Accepted: true, Result: "approved"}
	}
	task := notifTask(NotificationModeToast, ToastTimingAfter)
	success := ExecutionResult{Success: true, ExitCodeSet: true, ExitCode: 0}

	svc.dispatchExecutionNotification(dispatcher, task, notifEntry("a1"), nil, deferState{}, resolvePSADTWelcomeOptions(task))
	svc.dispatchExecutionNotification(dispatcher, task, notifEntry("a2"), &success, deferState{}, resolvePSADTWelcomeOptions(task))

	if len(dispatched) != 1 {
		t.Fatalf("Toast After: esperado 1 notificacao (resultado), got %d", len(dispatched))
	}
	if dispatched[0].EventType != "install_end" || dispatched[0].Mode != "notify_only" {
		t.Fatalf("Toast After: esperado install_end/notify_only, got %s/%s", dispatched[0].EventType, dispatched[0].Mode)
	}
	if dispatched[0].Metadata["toastTiming"] != ToastTimingAfter {
		t.Fatalf("metadata toastTiming=%v", dispatched[0].Metadata["toastTiming"])
	}
}

// Prompt continua com o Welcome no inicio (comportamento anterior) e resultado notify_only.
func TestDispatch_NotificationModePrompt_KeepWelcome(t *testing.T) {
	svc := &Service{}
	var dispatched []AutomationNotificationRequest
	dispatcher := func(req AutomationNotificationRequest) AutomationNotificationResponse {
		dispatched = append(dispatched, req)
		return AutomationNotificationResponse{Accepted: true, Result: "approved"}
	}
	task := notifTask(NotificationModePrompt, ToastTimingAfter)
	success := ExecutionResult{Success: true, ExitCodeSet: true, ExitCode: 0}

	svc.dispatchExecutionNotification(dispatcher, task, notifEntry("p1"), nil, deferState{}, resolvePSADTWelcomeOptions(task))
	svc.dispatchExecutionNotification(dispatcher, task, notifEntry("p2"), &success, deferState{}, resolvePSADTWelcomeOptions(task))

	if len(dispatched) != 2 {
		t.Fatalf("Prompt: esperado 2 notificacoes (inicio+resultado), got %d", len(dispatched))
	}
	if dispatched[0].Mode != "require_confirmation" || dispatched[0].Layout != "welcome" {
		t.Fatalf("Prompt start: esperado require_confirmation/welcome, got %s/%s", dispatched[0].Mode, dispatched[0].Layout)
	}
	if dispatched[1].Mode != "notify_only" {
		t.Fatalf("Prompt result: esperado notify_only, got %s", dispatched[1].Mode)
	}
}

// Toast explicito tambem vale para acao que nao e de pacote (RunScript).
func TestDispatch_NotificationModeToast_NonPackageAllowed(t *testing.T) {
	svc := &Service{}
	var dispatched []AutomationNotificationRequest
	dispatcher := func(req AutomationNotificationRequest) AutomationNotificationResponse {
		dispatched = append(dispatched, req)
		return AutomationNotificationResponse{Accepted: true, Result: "approved"}
	}
	task := AutomationTask{
		TaskID:           "task-script-toast",
		Name:             "Run script",
		ActionType:       ActionRunScript,
		NotificationMode: NotificationModeToast,
		ToastTiming:      ToastTimingAfter,
	}
	success := ExecutionResult{Success: true}
	svc.dispatchExecutionNotification(dispatcher, task, notifEntry("np1"), &success, deferState{}, resolvePSADTWelcomeOptions(task))
	if len(dispatched) != 1 {
		t.Fatalf("Toast explicito em RunScript deve notificar resultado, got %d", len(dispatched))
	}
}

// Toast nativo do app: mesma janela do Toast (Before/After), mas com a
// superficie marcada no metadata para o App enviar a notificacao nativa do
// sistema (mesmo caminho do Chat IA).
func TestDispatch_NotificationModeNativeToast_MarksNativeSurface(t *testing.T) {
	svc := &Service{}
	var dispatched []AutomationNotificationRequest
	dispatcher := func(req AutomationNotificationRequest) AutomationNotificationResponse {
		dispatched = append(dispatched, req)
		return AutomationNotificationResponse{Accepted: true, Result: "approved"}
	}
	task := notifTask(NotificationModeNativeToast, ToastTimingAfter)
	success := ExecutionResult{Success: true, ExitCodeSet: true, ExitCode: 0}

	svc.dispatchExecutionNotification(dispatcher, task, notifEntry("nt1"), nil, deferState{}, resolvePSADTWelcomeOptions(task))
	svc.dispatchExecutionNotification(dispatcher, task, notifEntry("nt2"), &success, deferState{}, resolvePSADTWelcomeOptions(task))

	if len(dispatched) != 1 {
		t.Fatalf("NativeToast After: esperado 1 notificacao (resultado), got %d", len(dispatched))
	}
	if dispatched[0].EventType != "install_end" || dispatched[0].Mode != "notify_only" || dispatched[0].Layout != "toast" {
		t.Fatalf("NativeToast: esperado install_end/notify_only/toast, got %s/%s/%s", dispatched[0].EventType, dispatched[0].Mode, dispatched[0].Layout)
	}
	if got := dispatched[0].Metadata[NotificationSurfaceMetadataKey]; got != NotificationSurfaceNative {
		t.Fatalf("metadata %s=%v, want %s", NotificationSurfaceMetadataKey, got, NotificationSurfaceNative)
	}
	if !IsNativeNotificationSurface(dispatched[0].Metadata) {
		t.Fatal("IsNativeNotificationSurface deve reconhecer a superficie nativa")
	}
}

// Toast da interface do agent continua marcado como superficie "agent" (o App
// nao envia notificacao nativa nesse caso).
func TestDispatch_NotificationModeToast_KeepsAgentSurface(t *testing.T) {
	svc := &Service{}
	var dispatched []AutomationNotificationRequest
	dispatcher := func(req AutomationNotificationRequest) AutomationNotificationResponse {
		dispatched = append(dispatched, req)
		return AutomationNotificationResponse{Accepted: true, Result: "approved"}
	}
	task := notifTask(NotificationModeToast, ToastTimingAfter)
	success := ExecutionResult{Success: true, ExitCodeSet: true, ExitCode: 0}

	svc.dispatchExecutionNotification(dispatcher, task, notifEntry("ag1"), &success, deferState{}, resolvePSADTWelcomeOptions(task))

	if len(dispatched) != 1 {
		t.Fatalf("Toast After: esperado 1 notificacao, got %d", len(dispatched))
	}
	if got := dispatched[0].Metadata[NotificationSurfaceMetadataKey]; got != NotificationSurfaceAgent {
		t.Fatalf("metadata %s=%v, want %s", NotificationSurfaceMetadataKey, got, NotificationSurfaceAgent)
	}
	if IsNativeNotificationSurface(dispatched[0].Metadata) {
		t.Fatal("Toast do agent nao pode ser marcado como superficie nativa")
	}
}

func TestNotificationSurfaceHelpers(t *testing.T) {
	if !IsToastNotificationMode(NotificationModeToast) || !IsToastNotificationMode(NotificationModeNativeToast) {
		t.Fatal("Toast e NativeToast sao modos de toast informativo")
	}
	if IsToastNotificationMode(NotificationModePrompt) || IsToastNotificationMode(NotificationModeSilent) {
		t.Fatal("Prompt/Silent nao sao modos de toast informativo")
	}
	if got := NotificationSurfaceForMode(NotificationModeNativeToast); got != NotificationSurfaceNative {
		t.Fatalf("NotificationSurfaceForMode(NativeToast)=%s", got)
	}
	if got := NotificationSurfaceForMode(NotificationModeToast); got != NotificationSurfaceAgent {
		t.Fatalf("NotificationSurfaceForMode(Toast)=%s", got)
	}
	if IsNativeNotificationSurface(nil) || IsNativeNotificationSurface(map[string]any{}) {
		t.Fatal("metadata vazio nao e superficie nativa")
	}
	if !IsNativeNotificationSurface(map[string]any{NotificationSurfaceMetadataKey: "NATIVE"}) {
		t.Fatal("superficie nativa deve ser case-insensitive")
	}
}

// Compatibilidade: policy antiga (modo vazio) mantem o comportamento anterior
// (RequiresApproval=false -> toast de inicio e resultado).
func TestDispatch_LegacyPolicyWithoutMode_KeepsOldBehavior(t *testing.T) {
	svc := &Service{}
	var dispatched []AutomationNotificationRequest
	dispatcher := func(req AutomationNotificationRequest) AutomationNotificationResponse {
		dispatched = append(dispatched, req)
		return AutomationNotificationResponse{Accepted: true, Result: "approved"}
	}
	task := AutomationTask{TaskID: "legacy", Name: "Install", ActionType: ActionInstallPackage, PackageID: "Test.App"}
	success := ExecutionResult{Success: true, ExitCodeSet: true, ExitCode: 0}

	svc.dispatchExecutionNotification(dispatcher, task, notifEntry("l1"), nil, deferState{}, resolvePSADTWelcomeOptions(task))
	svc.dispatchExecutionNotification(dispatcher, task, notifEntry("l2"), &success, deferState{}, resolvePSADTWelcomeOptions(task))
	if len(dispatched) != 2 {
		t.Fatalf("legado sem modo: esperado 2 notificacoes, got %d", len(dispatched))
	}
}

func TestResolveNotificationMode_AndTiming(t *testing.T) {
	cases := []struct {
		task       AutomationTask
		wantMode   string
		wantTiming string
	}{
		{AutomationTask{NotificationMode: "silent"}, NotificationModeSilent, ToastTimingAfter},
		{AutomationTask{NotificationMode: "Prompt"}, NotificationModePrompt, ToastTimingAfter},
		{AutomationTask{NotificationMode: "toast", ToastTiming: "before"}, NotificationModeToast, ToastTimingBefore},
		{AutomationTask{NotificationMode: "toast", ToastTiming: "garbage"}, NotificationModeToast, ToastTimingAfter},
		{AutomationTask{NotificationMode: "nativetoast", ToastTiming: "after"}, NotificationModeNativeToast, ToastTimingAfter},
		{AutomationTask{NotificationMode: "NativeToast", ToastTiming: "before"}, NotificationModeNativeToast, ToastTimingBefore},
		{AutomationTask{RequiresApproval: true}, NotificationModePrompt, ToastTimingAfter},
		{AutomationTask{}, NotificationModeToast, ToastTimingAfter},
	}
	for _, tc := range cases {
		if got := ResolveNotificationMode(tc.task); got != tc.wantMode {
			t.Fatalf("ResolveNotificationMode(%+v)=%s, want %s", tc.task, got, tc.wantMode)
		}
		if got := ResolveToastTiming(tc.task); got != tc.wantTiming {
			t.Fatalf("ResolveToastTiming(%+v)=%s, want %s", tc.task, got, tc.wantTiming)
		}
	}
}
