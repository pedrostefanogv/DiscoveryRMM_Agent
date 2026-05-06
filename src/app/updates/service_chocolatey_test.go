package updates

import (
	"context"
	"testing"
)

type mockUpdatesAppsService struct {
	wingetOut string
	wingetErr error
	chocoOut  string
	chocoErr  error
}

func (m *mockUpdatesAppsService) ListUpgradable(ctx context.Context) (string, error) {
	return m.wingetOut, m.wingetErr
}

func (m *mockUpdatesAppsService) ListUpgradableChocolatey(ctx context.Context) (string, error) {
	return m.chocoOut, m.chocoErr
}

func (m *mockUpdatesAppsService) ListInstalled(ctx context.Context) (string, error) {
	return "", nil
}

func TestParseChocolateyOutdatedOutput_LimitOutput(t *testing.T) {
	raw := "Chocolatey v2.5.1\n" +
		"git|2.45.1|2.46.0|false\n" +
		"7zip|24.07|24.08|false\n"

	items := parseChocolateyOutdatedOutput(raw)
	if len(items) != 2 {
		t.Fatalf("expected 2 chocolatey items, got %d", len(items))
	}
	if items[0].ID != "git" || items[0].CurrentVersion != "2.45.1" || items[0].AvailableVersion != "2.46.0" || items[0].Source != "chocolatey" {
		t.Fatalf("unexpected first chocolatey item: %+v", items[0])
	}
	if items[1].ID != "7zip" || items[1].Source != "chocolatey" {
		t.Fatalf("unexpected second chocolatey item: %+v", items[1])
	}
}

func TestGetPendingUpdates_MergesWingetAndChocolatey(t *testing.T) {
	apps := &mockUpdatesAppsService{
		wingetOut: "Name        Id            Version Available Source\n" +
			"---------------------------------------------------\n" +
			"Git         Git.Git       2.45.1  2.46.0    winget\n" +
			"1 upgrades available.\n",
		chocoOut: "git|2.45.1|2.46.0|false\n",
	}

	svc := NewService(Options{Apps: apps})
	items, err := svc.GetPendingUpdates()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 update items (winget + chocolatey), got %d", len(items))
	}

	seenWinget := false
	seenChocolatey := false
	for _, item := range items {
		if item.Source == "winget" && item.ID == "Git.Git" {
			seenWinget = true
		}
		if item.Source == "chocolatey" && item.ID == "git" {
			seenChocolatey = true
		}
	}
	if !seenWinget || !seenChocolatey {
		t.Fatalf("missing expected sources in merged updates: %+v", items)
	}
}
