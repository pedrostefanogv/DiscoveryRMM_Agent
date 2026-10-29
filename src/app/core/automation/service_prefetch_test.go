package automation

import (
	"context"
	"errors"
	"testing"

	"discovery/app/core/database"
)

// fakePreparePM implementa PackageManager + PackagePreparer para testar a ordem
// prefetch -> prompt sem tocar no winget real.
type fakePreparePM struct {
	events     *[]string
	prepareErr error
}

func (f *fakePreparePM) Install(ctx context.Context, id string) (string, error)   { return "", nil }
func (f *fakePreparePM) Uninstall(ctx context.Context, id string) (string, error) { return "", nil }
func (f *fakePreparePM) Upgrade(ctx context.Context, id string) (string, error)   { return "", nil }
func (f *fakePreparePM) UpgradeAll(ctx context.Context) (string, error)           { return "", nil }
func (f *fakePreparePM) ListInstalled(ctx context.Context) (string, error)        { return "", nil }
func (f *fakePreparePM) ListUpgradable(ctx context.Context) (string, error)       { return "", nil }
func (f *fakePreparePM) Prepare(ctx context.Context, id string) error {
	*f.events = append(*f.events, "prepare:"+id)
	return f.prepareErr
}

func TestPrefetchRunsBeforePromptAndNotExecution(t *testing.T) {
	var events []string
	pm := &fakePreparePM{events: &events}
	svc := &Service{}
	task := AutomationTask{
		TaskID:           "task-pf",
		Name:             "Install X",
		ActionType:       ActionInstallPackage,
		PackageID:        "Vendor.X",
		InstallationType: InstallationWinget,
		RequiresApproval: true,
	}
	dispatcher := func(req AutomationNotificationRequest) AutomationNotificationResponse {
		events = append(events, "prompt")
		return AutomationNotificationResponse{Accepted: true, Result: "approved"}
	}
	svc.prefetchPackageArtifact(context.Background(), pm, task, PSADTPolicy{})
	svc.dispatchExecutionNotification(dispatcher, task, database.AutomationExecutionEntry{TaskID: "task-pf", ExecutionID: "e1", CorrelationID: "c1"}, nil, deferState{}, resolvePSADTWelcomeOptions(task))

	if len(events) < 2 || events[0] != "prepare:Vendor.X" || events[1] != "prompt" {
		t.Fatalf("expected prepare before prompt, got %v", events)
	}
}

func TestPrefetchFailureIsFailOpen(t *testing.T) {
	var events []string
	pm := &fakePreparePM{events: &events, prepareErr: errors.New("p2p indisponivel")}
	svc := &Service{}
	task := AutomationTask{TaskID: "t", ActionType: ActionInstallPackage, PackageID: "Vendor.X", InstallationType: InstallationWinget}
	// Não deve panicar nem propagar erro: é best-effort.
	svc.prefetchPackageArtifact(context.Background(), pm, task, PSADTPolicy{})
	if len(events) != 1 {
		t.Fatalf("expected prepare attempted once, got %v", events)
	}
}

func TestPrefetchSkippedForNonPackageAndNonPreparer(t *testing.T) {
	var events []string
	pm := &fakePreparePM{events: &events}
	svc := &Service{}
	// Ação não-pacote: nunca prefetcha.
	svc.prefetchPackageArtifact(context.Background(), pm, AutomationTask{ActionType: ActionRunScript, PackageID: "X", InstallationType: InstallationWinget}, PSADTPolicy{})
	// Custom: não passa pelo roteador winget.
	svc.prefetchPackageArtifact(context.Background(), pm, AutomationTask{ActionType: ActionInstallPackage, PackageID: "X", InstallationType: InstallationCustom}, PSADTPolicy{})
	// PackageManager sem Prepare (ex.: choco/testes antigos): no-op.
	svc.prefetchPackageArtifact(context.Background(), noPreparePM{}, AutomationTask{ActionType: ActionInstallPackage, PackageID: "X", InstallationType: InstallationWinget}, PSADTPolicy{})
	if len(events) != 0 {
		t.Fatalf("expected no prepare calls, got %v", events)
	}
}

type noPreparePM struct{}

func (noPreparePM) Install(ctx context.Context, id string) (string, error)   { return "", nil }
func (noPreparePM) Uninstall(ctx context.Context, id string) (string, error) { return "", nil }
func (noPreparePM) Upgrade(ctx context.Context, id string) (string, error)   { return "", nil }
func (noPreparePM) UpgradeAll(ctx context.Context) (string, error)           { return "", nil }
func (noPreparePM) ListInstalled(ctx context.Context) (string, error)        { return "", nil }
func (noPreparePM) ListUpgradable(ctx context.Context) (string, error)       { return "", nil }
