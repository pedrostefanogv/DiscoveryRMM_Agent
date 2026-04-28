package inventory

import (
	"context"
	"strings"
	"testing"

	"discovery/app/debug"
	"discovery/internal/models"
)

type mockInventoryProvider struct {
	getInventoryCalls int
	softwareCalls     int
}

func (m *mockInventoryProvider) GetInventory(ctx context.Context) (models.InventoryReport, error) {
	m.getInventoryCalls++
	return models.InventoryReport{Hardware: models.HardwareInfo{Hostname: "provider"}}, nil
}

func (m *mockInventoryProvider) GetNetworkConnections(ctx context.Context) (models.NetworkConnectionsReport, error) {
	return models.NetworkConnectionsReport{}, nil
}

func (m *mockInventoryProvider) CollectSoftware(ctx context.Context) ([]models.SoftwareItem, error) {
	m.softwareCalls++
	return nil, nil
}

func (m *mockInventoryProvider) CollectStartupItems(ctx context.Context) ([]models.StartupItem, error) {
	return nil, nil
}

func (m *mockInventoryProvider) CollectListeningPorts(ctx context.Context) ([]models.ListeningPortInfo, error) {
	return nil, nil
}

type mockInventoryCache struct {
	report models.InventoryReport
	loaded bool
}

func (m *mockInventoryCache) Get() (models.InventoryReport, bool) {
	return m.report, m.loaded
}

func (m *mockInventoryCache) Set(report models.InventoryReport) {
	m.report = report
	m.loaded = true
}

func TestRefreshInventory_SkipsCollectionWhenNotProvisioned(t *testing.T) {
	provider := &mockInventoryProvider{}
	svc := &Service{
		inventory:     provider,
		beginActivity: func(string) func() { return func() {} },
		ctx:           func() context.Context { return context.Background() },
		debugConfig:   func() debug.Config { return debug.Config{} },
	}

	_, err := svc.RefreshInventory()
	if err == nil {
		t.Fatalf("expected provisioning error")
	}
	if !strings.Contains(err.Error(), "nao estiver provisionado") {
		t.Fatalf("unexpected error: %v", err)
	}
	if provider.getInventoryCalls != 0 {
		t.Fatalf("expected inventory provider not to run, got %d calls", provider.getInventoryCalls)
	}

	_, err = svc.RefreshSoftware()
	if err == nil {
		t.Fatalf("expected provisioning error for software refresh")
	}
	if provider.softwareCalls != 0 {
		t.Fatalf("expected software provider not to run, got %d calls", provider.softwareCalls)
	}
}

func TestGetInventory_ReturnsCacheWithoutProvisioning(t *testing.T) {
	provider := &mockInventoryProvider{}
	cache := &mockInventoryCache{
		report: models.InventoryReport{Hardware: models.HardwareInfo{Hostname: "cached-host"}},
		loaded: true,
	}
	svc := &Service{
		inventory:     provider,
		cache:         cache,
		beginActivity: func(string) func() { return func() {} },
		ctx:           func() context.Context { return context.Background() },
		debugConfig:   func() debug.Config { return debug.Config{} },
	}

	report, err := svc.GetInventory()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Hardware.Hostname != "cached-host" {
		t.Fatalf("unexpected cached host: %q", report.Hardware.Hostname)
	}
	if provider.getInventoryCalls != 0 {
		t.Fatalf("expected provider not to be called, got %d", provider.getInventoryCalls)
	}
}
