package services

import (
	"context"
	"errors"
	"testing"
)

type mockWingetProvider struct {
	installID       string
	uninstallID     string
	upgradeID       string
	listInstalled   string
	listUpgradable  string
	upgradeAllValue string
}

func (m *mockWingetProvider) Install(ctx context.Context, id string) (string, error) {
	m.installID = id
	return "winget-install", nil
}

func (m *mockWingetProvider) Uninstall(ctx context.Context, id string) (string, error) {
	m.uninstallID = id
	return "winget-uninstall", nil
}

func (m *mockWingetProvider) Upgrade(ctx context.Context, id string) (string, error) {
	m.upgradeID = id
	return "winget-upgrade", nil
}

func (m *mockWingetProvider) UpgradeAll(ctx context.Context) (string, error) {
	return m.upgradeAllValue, nil
}

func (m *mockWingetProvider) ListInstalled(ctx context.Context) (string, error) {
	return m.listInstalled, nil
}

func (m *mockWingetProvider) ListUpgradable(ctx context.Context) (string, error) {
	return m.listUpgradable, nil
}

type mockChocolateyProvider struct {
	installID      string
	uninstallID    string
	upgradeID      string
	listUpgradable string
	listErr        error
}

func (m *mockChocolateyProvider) Install(ctx context.Context, id string) (string, error) {
	m.installID = id
	return "choco-install", nil
}

func (m *mockChocolateyProvider) Uninstall(ctx context.Context, id string) (string, error) {
	m.uninstallID = id
	return "choco-uninstall", nil
}

func (m *mockChocolateyProvider) Upgrade(ctx context.Context, id string) (string, error) {
	m.upgradeID = id
	return "choco-upgrade", nil
}

func (m *mockChocolateyProvider) ListUpgradable(ctx context.Context) (string, error) {
	return m.listUpgradable, m.listErr
}

func TestAppsServiceUpgrade_UsesChocolateyWhenSourceHintIsChocolatey(t *testing.T) {
	winget := &mockWingetProvider{}
	choco := &mockChocolateyProvider{}
	svc := NewAppsService(winget, choco)

	out, err := svc.Upgrade(context.Background(), "chocolatey::git")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "choco-upgrade" {
		t.Fatalf("unexpected output: %q", out)
	}
	if choco.upgradeID != "git" {
		t.Fatalf("expected chocolatey id git, got %q", choco.upgradeID)
	}
	if winget.upgradeID != "" {
		t.Fatalf("expected winget not to be called, got %q", winget.upgradeID)
	}
}

func TestAppsServiceUpgrade_UsesWingetByDefault(t *testing.T) {
	winget := &mockWingetProvider{}
	choco := &mockChocolateyProvider{}
	svc := NewAppsService(winget, choco)

	out, err := svc.Upgrade(context.Background(), "Git.Git")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "winget-upgrade" {
		t.Fatalf("unexpected output: %q", out)
	}
	if winget.upgradeID != "Git.Git" {
		t.Fatalf("expected winget id Git.Git, got %q", winget.upgradeID)
	}
}

func TestListUpgradableChocolatey_IgnoresMissingChocolatey(t *testing.T) {
	winget := &mockWingetProvider{}
	choco := &mockChocolateyProvider{listErr: errors.New("Chocolatey nao encontrado no host")}
	svc := NewAppsService(winget, choco)

	out, err := svc.ListUpgradableChocolatey(context.Background())
	if err != nil {
		t.Fatalf("expected nil error when chocolatey is missing, got %v", err)
	}
	if out != "" {
		t.Fatalf("expected empty output when chocolatey is missing, got %q", out)
	}
}
