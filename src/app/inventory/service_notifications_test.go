package inventory

import (
	"context"
	"errors"
	"testing"

	"discovery/app/appstore"
)

type mockAppsService struct {
	installOutput string
	installErr    error
}

func (m *mockAppsService) Install(ctx context.Context, id string) (string, error) {
	return m.installOutput, m.installErr
}

func (m *mockAppsService) Uninstall(ctx context.Context, id string) (string, error) {
	return "", nil
}

func (m *mockAppsService) Upgrade(ctx context.Context, id string) (string, error) {
	return "", nil
}

func (m *mockAppsService) UpgradeAll(ctx context.Context) (string, error) {
	return "", nil
}

func (m *mockAppsService) ListInstalled(ctx context.Context) (string, error) {
	return "", nil
}

func TestInstall_NotificationsSuccessSequence(t *testing.T) {
	notifications := make([]InventoryNotification, 0, 4)
	svc := &Service{
		apps:          &mockAppsService{installOutput: "ok"},
		beginActivity: func(string) func() { return func() {} },
		logf:          func(string) {},
		ctx:           func() context.Context { return context.Background() },
		resolveAllowed: func(context.Context, string) (appstore.Item, error) {
			return appstore.Item{InstallationType: string(appstore.InstallationWinget)}, nil
		},
		dispatchNotification: func(req InventoryNotification) InventoryNotificationResponse {
			notifications = append(notifications, req)
			return InventoryNotificationResponse{Accepted: true, Result: "approved"}
		},
	}

	out, err := svc.Install("Test.App")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "ok" {
		t.Fatalf("unexpected output: %q", out)
	}

	if len(notifications) != 3 {
		t.Fatalf("expected 3 notifications, got %d", len(notifications))
	}
	if notifications[0].EventType != "install_start" || notifications[0].Metadata["phase"] != "download" {
		t.Fatalf("expected first event install_start/download, got %q/%v", notifications[0].EventType, notifications[0].Metadata["phase"])
	}
	if notifications[1].EventType != "install_start" || notifications[1].Metadata["phase"] != "instalacao" {
		t.Fatalf("expected second event install_start/instalacao, got %q/%v", notifications[1].EventType, notifications[1].Metadata["phase"])
	}
	if notifications[2].EventType != "install_end" || notifications[2].Metadata["phase"] != "validacao" {
		t.Fatalf("expected third event install_end/validacao, got %q/%v", notifications[2].EventType, notifications[2].Metadata["phase"])
	}
}

func TestInstall_NotificationOnAuthorizationFailure(t *testing.T) {
	notifications := make([]InventoryNotification, 0, 2)
	svc := &Service{
		apps:          &mockAppsService{},
		beginActivity: func(string) func() { return func() {} },
		logf:          func(string) {},
		ctx:           func() context.Context { return context.Background() },
		resolveAllowed: func(context.Context, string) (appstore.Item, error) {
			return appstore.Item{}, errors.New("denied")
		},
		dispatchNotification: func(req InventoryNotification) InventoryNotificationResponse {
			notifications = append(notifications, req)
			return InventoryNotificationResponse{}
		},
	}

	_, err := svc.Install("Blocked.App")
	if err == nil {
		t.Fatalf("expected error")
	}
	if len(notifications) != 2 {
		t.Fatalf("expected 2 notifications, got %d", len(notifications))
	}
	last := notifications[len(notifications)-1]
	if last.EventType != "install_failed" {
		t.Fatalf("expected install_failed, got %q", last.EventType)
	}
	if last.Metadata["status"] != "failed" {
		t.Fatalf("expected status failed, got %v", last.Metadata["status"])
	}
}

func TestInstall_NotificationOnExecutionFailure(t *testing.T) {
	notifications := make([]InventoryNotification, 0, 3)
	svc := &Service{
		apps:          &mockAppsService{installErr: errors.New("winget failed")},
		beginActivity: func(string) func() { return func() {} },
		logf:          func(string) {},
		ctx:           func() context.Context { return context.Background() },
		resolveAllowed: func(context.Context, string) (appstore.Item, error) {
			return appstore.Item{InstallationType: string(appstore.InstallationWinget)}, nil
		},
		dispatchNotification: func(req InventoryNotification) InventoryNotificationResponse {
			notifications = append(notifications, req)
			return InventoryNotificationResponse{}
		},
	}

	_, err := svc.Install("Fail.App")
	if err == nil {
		t.Fatalf("expected error")
	}
	if len(notifications) != 3 {
		t.Fatalf("expected 3 notifications, got %d", len(notifications))
	}
	last := notifications[len(notifications)-1]
	if last.EventType != "install_failed" {
		t.Fatalf("expected install_failed, got %q", last.EventType)
	}
	if last.Metadata["phase"] != "instalacao" {
		t.Fatalf("expected phase instalacao, got %v", last.Metadata["phase"])
	}
}
