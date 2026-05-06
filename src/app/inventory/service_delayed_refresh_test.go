package inventory

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"discovery/app/appstore"
	"discovery/app/debug"
	"discovery/internal/models"
)

type delayedRefreshAppsService struct {
	installOutput string
	installErr    error
	upgradeOutput string
	upgradeErr    error
}

func (m *delayedRefreshAppsService) Install(ctx context.Context, id string) (string, error) {
	return m.installOutput, m.installErr
}

func (m *delayedRefreshAppsService) Uninstall(ctx context.Context, id string) (string, error) {
	return "", nil
}

func (m *delayedRefreshAppsService) Upgrade(ctx context.Context, id string) (string, error) {
	return m.upgradeOutput, m.upgradeErr
}

func (m *delayedRefreshAppsService) UpgradeAll(ctx context.Context) (string, error) {
	return "", nil
}

func (m *delayedRefreshAppsService) ListInstalled(ctx context.Context) (string, error) {
	return "", nil
}

type delayedRefreshInventoryProvider struct {
	calls  atomic.Int32
	called chan struct{}
}

func newDelayedRefreshInventoryProvider() *delayedRefreshInventoryProvider {
	return &delayedRefreshInventoryProvider{called: make(chan struct{}, 8)}
}

func (m *delayedRefreshInventoryProvider) GetInventory(ctx context.Context) (models.InventoryReport, error) {
	m.calls.Add(1)
	select {
	case m.called <- struct{}{}:
	default:
	}
	return models.InventoryReport{Hardware: models.HardwareInfo{Hostname: "refresh-host"}}, nil
}

func (m *delayedRefreshInventoryProvider) GetNetworkConnections(ctx context.Context) (models.NetworkConnectionsReport, error) {
	return models.NetworkConnectionsReport{}, nil
}

func (m *delayedRefreshInventoryProvider) CollectSoftware(ctx context.Context) ([]models.SoftwareItem, error) {
	return nil, nil
}

func (m *delayedRefreshInventoryProvider) CollectStartupItems(ctx context.Context) ([]models.StartupItem, error) {
	return nil, nil
}

func (m *delayedRefreshInventoryProvider) CollectListeningPorts(ctx context.Context) ([]models.ListeningPortInfo, error) {
	return nil, nil
}

func provisionedDebugConfigForRefreshTests() debug.Config {
	return debug.Config{
		ApiScheme: "https",
		ApiServer: "server.local",
		AuthToken: "mdz_test_token",
		AgentID:   "9b8bc549-6a5f-4e8d-8149-69bdf3cc1d15",
	}
}

func newServiceForDelayedRefreshTests(apps AppsService, provider InventoryService, delay time.Duration) *Service {
	return &Service{
		apps:      apps,
		inventory: provider,
		beginActivity: func(string) func() {
			return func() {}
		},
		logf: func(string) {},
		ctx: func() context.Context {
			return context.Background()
		},
		resolveAllowed: func(context.Context, string) (appstore.Item, error) {
			return appstore.Item{InstallationType: string(appstore.InstallationWinget)}, nil
		},
		debugConfig: func() debug.Config {
			return provisionedDebugConfigForRefreshTests()
		},
		postInstallInventoryRefreshDelay: delay,
	}
}

func stopDelayedRefreshTimer(svc *Service) {
	svc.postInstallInventoryRefreshMu.Lock()
	defer svc.postInstallInventoryRefreshMu.Unlock()
	if svc.postInstallInventoryRefreshTimer != nil {
		svc.postInstallInventoryRefreshTimer.Stop()
		svc.postInstallInventoryRefreshTimer = nil
	}
}

func TestInstall_SchedulesDelayedInventoryRefreshOnSuccess(t *testing.T) {
	provider := newDelayedRefreshInventoryProvider()
	svc := newServiceForDelayedRefreshTests(&delayedRefreshAppsService{installOutput: "ok"}, provider, 20*time.Millisecond)
	t.Cleanup(func() { stopDelayedRefreshTimer(svc) })

	if _, err := svc.Install("Vendor.App"); err != nil {
		t.Fatalf("expected install success, got error: %v", err)
	}

	select {
	case <-provider.called:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected delayed inventory refresh after successful install")
	}
}

func TestUpgrade_DebouncesDelayedInventoryRefresh(t *testing.T) {
	provider := newDelayedRefreshInventoryProvider()
	svc := newServiceForDelayedRefreshTests(&delayedRefreshAppsService{upgradeOutput: "ok"}, provider, 80*time.Millisecond)
	t.Cleanup(func() { stopDelayedRefreshTimer(svc) })

	if _, err := svc.Upgrade("Vendor.App.One"); err != nil {
		t.Fatalf("expected first upgrade success, got error: %v", err)
	}

	time.Sleep(25 * time.Millisecond)

	if _, err := svc.Upgrade("Vendor.App.Two"); err != nil {
		t.Fatalf("expected second upgrade success, got error: %v", err)
	}

	select {
	case <-provider.called:
	case <-time.After(700 * time.Millisecond):
		t.Fatal("expected debounced delayed inventory refresh")
	}

	select {
	case <-provider.called:
		t.Fatal("expected a single inventory refresh after debounced upgrades")
	case <-time.After(180 * time.Millisecond):
	}

	if got := provider.calls.Load(); got != 1 {
		t.Fatalf("expected 1 inventory refresh call, got %d", got)
	}
}

func TestUpgrade_DoesNotScheduleDelayedInventoryRefreshOnFailure(t *testing.T) {
	provider := newDelayedRefreshInventoryProvider()
	svc := newServiceForDelayedRefreshTests(&delayedRefreshAppsService{upgradeErr: errors.New("upgrade failed")}, provider, 20*time.Millisecond)
	t.Cleanup(func() { stopDelayedRefreshTimer(svc) })

	if _, err := svc.Upgrade("Vendor.Broken.App"); err == nil {
		t.Fatal("expected upgrade error")
	}

	select {
	case <-provider.called:
		t.Fatal("did not expect inventory refresh when upgrade fails")
	case <-time.After(180 * time.Millisecond):
	}

	if got := provider.calls.Load(); got != 0 {
		t.Fatalf("expected 0 inventory refresh calls, got %d", got)
	}
}
