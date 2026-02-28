package services

import "context"

type WingetProvider interface {
	Install(ctx context.Context, id string) (string, error)
	Uninstall(ctx context.Context, id string) (string, error)
	Upgrade(ctx context.Context, id string) (string, error)
	UpgradeAll(ctx context.Context) (string, error)
	ListInstalled(ctx context.Context) (string, error)
	ListUpgradable(ctx context.Context) (string, error)
}

type AppsService struct {
	winget WingetProvider
}

func NewAppsService(winget WingetProvider) *AppsService {
	return &AppsService{winget: winget}
}

func (s *AppsService) Install(ctx context.Context, id string) (string, error) {
	return s.winget.Install(ctx, id)
}

func (s *AppsService) Uninstall(ctx context.Context, id string) (string, error) {
	return s.winget.Uninstall(ctx, id)
}

func (s *AppsService) Upgrade(ctx context.Context, id string) (string, error) {
	return s.winget.Upgrade(ctx, id)
}

func (s *AppsService) UpgradeAll(ctx context.Context) (string, error) {
	return s.winget.UpgradeAll(ctx)
}

func (s *AppsService) ListInstalled(ctx context.Context) (string, error) {
	return s.winget.ListInstalled(ctx)
}

func (s *AppsService) ListUpgradable(ctx context.Context) (string, error) {
	return s.winget.ListUpgradable(ctx)
}
