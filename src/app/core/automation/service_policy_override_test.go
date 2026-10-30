package automation

import (
	"testing"

	"discovery/app/core/database"
)

// Estado mínimo com uma task de pacote ativa (o cenário real das tasks
// bleachbit/thunderbird fora da App Store).
func overrideState(tasks ...AutomationTask) State {
	return State{Available: true, PolicyFingerprint: "fp-1", Tasks: tasks, TaskCount: len(tasks)}
}

func TestPackageAuthorizedByPolicyMatchesActiveTask(t *testing.T) {
	svc := NewService(func() RuntimeConfig { return RuntimeConfig{AgentID: "agent-1"} }, nil)
	svc.SetStateForTesting(overrideState(AutomationTask{
		TaskID:           "task-1",
		ActionType:       ActionInstallPackage,
		InstallationType: InstallationWinget,
		PackageID:        "BleachBit.BleachBit",
		LastUpdatedAt:    "2026-10-29T00:00:00Z",
	}))

	if !svc.PackageAuthorizedByPolicy(InstallationWinget, "bleachbit.bleachbit", "install") {
		t.Fatalf("task install ativa deve autorizar install")
	}
	if svc.PackageAuthorizedByPolicy(InstallationWinget, "bleachbit.bleachbit", "upgrade") {
		t.Fatalf("task install não deve autorizar upgrade")
	}
	if svc.PackageAuthorizedByPolicy(InstallationWinget, "outro.pacote", "install") {
		t.Fatalf("pacote diferente não deve ser autorizado")
	}
}

// UpdateOrInstallPackage cobre install e upgrade; RemovePackage só uninstall.
func TestPackageAuthorizedByPolicyActionTypeMapping(t *testing.T) {
	svc := NewService(func() RuntimeConfig { return RuntimeConfig{AgentID: "agent-1"} }, nil)
	svc.SetStateForTesting(overrideState(AutomationTask{
		TaskID:           "task-2",
		ActionType:       ActionUpdateOrInstallPackage,
		InstallationType: InstallationWinget,
		PackageID:        "mozilla.thunderbird.br",
		LastUpdatedAt:    "2026-10-29T00:00:00Z",
	}, AutomationTask{
		TaskID:           "task-3",
		ActionType:       ActionRemovePackage,
		InstallationType: InstallationWinget,
		PackageID:        "velho.pacote",
		LastUpdatedAt:    "2026-10-29T00:00:00Z",
	}))

	for _, op := range []string{"install", "upgrade"} {
		if !svc.PackageAuthorizedByPolicy(InstallationWinget, "mozilla.thunderbird.br", op) {
			t.Fatalf("UpdateOrInstallPackage deve autorizar %q", op)
		}
	}
	if svc.PackageAuthorizedByPolicy(InstallationWinget, "mozilla.thunderbird.br", "uninstall") {
		t.Fatalf("UpdateOrInstallPackage não deve autorizar uninstall")
	}
	if !svc.PackageAuthorizedByPolicy(InstallationWinget, "velho.pacote", "uninstall") {
		t.Fatalf("RemovePackage deve autorizar uninstall")
	}
}

// Tasks de script/comando (InstallationType vazio) nunca autorizam pacote.
func TestPackageAuthorizedByPolicyIgnoresScriptTasks(t *testing.T) {
	svc := NewService(func() RuntimeConfig { return RuntimeConfig{AgentID: "agent-1"} }, nil)
	svc.SetStateForTesting(overrideState(AutomationTask{
		TaskID:        "task-4",
		ActionType:    ActionRunScript,
		PackageID:     "bleachbit.bleachbit",
		LastUpdatedAt: "2026-10-29T00:00:00Z",
	}))
	if svc.PackageAuthorizedByPolicy(InstallationWinget, "bleachbit.bleachbit", "install") {
		t.Fatalf("task sem installationType não deve autorizar pacote")
	}
	if svc.PackageAuthorizedByPolicy(InstallationWinget, "", "install") {
		t.Fatalf("packageId vazio não deve ser autorizado")
	}
}

// Task editada na policy (mesmo TaskId, LastUpdatedAt novo) apaga os markers
// circuit:fail e skipbackoff — destrava a task pausada pelo breaker.
func TestResetBreakersForChangedTasksResetsOnEdit(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	svc := NewService(func() RuntimeConfig { return RuntimeConfig{AgentID: "agent-1"} }, nil)
	svc.SetDB(db)

	taskID := "01a17a4f"
	for _, key := range []string{"circuit:fail:" + taskID, "skipbackoff:" + taskID} {
		if err := db.SetAutomationMarker("agent-1", key, "estado-antigo"); err != nil {
			t.Fatalf("set marker %s: %v", key, err)
		}
	}

	previous := []AutomationTask{{TaskID: taskID, LastUpdatedAt: "2026-10-29T00:00:00Z"}}
	next := []AutomationTask{{TaskID: taskID, LastUpdatedAt: "2026-10-30T12:00:00Z"}}
	svc.resetBreakersForChangedTasks("agent-1", previous, next)

	for _, key := range []string{"circuit:fail:" + taskID, "skipbackoff:" + taskID} {
		if _, found, _ := db.GetAutomationMarker("agent-1", key); found {
			t.Fatalf("marker %s deveria ter sido apagado", key)
		}
	}
}

// Task NÃO editada (LastUpdatedAt igual) preserva o breaker — só a edição
// destrava.
func TestResetBreakersForChangedTasksPreservesUnchanged(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	svc := NewService(func() RuntimeConfig { return RuntimeConfig{AgentID: "agent-1"} }, nil)
	svc.SetDB(db)

	taskID := "01a18960"
	for _, key := range []string{"circuit:fail:" + taskID, "skipbackoff:" + taskID} {
		if err := db.SetAutomationMarker("agent-1", key, "estado-antigo"); err != nil {
			t.Fatalf("set marker %s: %v", key, err)
		}
	}

	updatedAt := "2026-10-29T00:00:00Z"
	svc.resetBreakersForChangedTasks("agent-1",
		[]AutomationTask{{TaskID: taskID, LastUpdatedAt: updatedAt}},
		[]AutomationTask{{TaskID: taskID, LastUpdatedAt: updatedAt}})

	for _, key := range []string{"circuit:fail:" + taskID, "skipbackoff:" + taskID} {
		if _, found, _ := db.GetAutomationMarker("agent-1", key); !found {
			t.Fatalf("marker %s deveria ter sido preservado", key)
		}
	}
}
