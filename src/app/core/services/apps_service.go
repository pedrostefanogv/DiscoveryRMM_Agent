package services

import (
	"context"
	"fmt"
	"strings"
)

const (
	packageManagerTargetSeparator = "::"
	packageManagerWinget          = "winget"
	packageManagerChocolatey      = "chocolatey"
	packageManagerChocoAlias      = "choco"
)

type WingetProvider interface {
	Install(ctx context.Context, id string) (string, error)
	Uninstall(ctx context.Context, id string) (string, error)
	Upgrade(ctx context.Context, id string) (string, error)
	UpgradeAll(ctx context.Context) (string, error)
	ListInstalled(ctx context.Context) (string, error)
	ListUpgradable(ctx context.Context) (string, error)
}

type ChocolateyProvider interface {
	Install(ctx context.Context, id string) (string, error)
	Uninstall(ctx context.Context, id string) (string, error)
	Upgrade(ctx context.Context, id string) (string, error)
	ListUpgradable(ctx context.Context) (string, error)
}

type AppsService struct {
	winget     WingetProvider
	chocolatey ChocolateyProvider
}

func NewAppsService(winget WingetProvider, chocolatey ChocolateyProvider) *AppsService {
	return &AppsService{winget: winget, chocolatey: chocolatey}
}

func (s *AppsService) Install(ctx context.Context, id string) (string, error) {
	manager, packageID, err := s.resolvePackageTarget(id)
	if err != nil {
		return "", err
	}
	if manager == packageManagerChocolatey {
		return s.chocolatey.Install(ctx, packageID)
	}
	return s.winget.Install(ctx, packageID)
}

func (s *AppsService) Uninstall(ctx context.Context, id string) (string, error) {
	manager, packageID, err := s.resolvePackageTarget(id)
	if err != nil {
		return "", err
	}
	if manager == packageManagerChocolatey {
		return s.chocolatey.Uninstall(ctx, packageID)
	}
	return s.winget.Uninstall(ctx, packageID)
}

func (s *AppsService) Upgrade(ctx context.Context, id string) (string, error) {
	manager, packageID, err := s.resolvePackageTarget(id)
	if err != nil {
		return "", err
	}
	if manager == packageManagerChocolatey {
		return s.chocolatey.Upgrade(ctx, packageID)
	}
	return s.winget.Upgrade(ctx, packageID)
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

func (s *AppsService) ListUpgradableChocolatey(ctx context.Context) (string, error) {
	if s.chocolatey == nil {
		return "", nil
	}
	out, err := s.chocolatey.ListUpgradable(ctx)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "chocolatey nao encontrado") {
		return "", nil
	}
	return out, err
}

func (s *AppsService) resolvePackageTarget(raw string) (string, string, error) {
	manager, packageID := splitPackageManagerTarget(raw)
	if packageID == "" {
		return "", "", fmt.Errorf("id do pacote e obrigatorio")
	}
	if manager == packageManagerChocolatey && s.chocolatey == nil {
		return "", "", fmt.Errorf("Chocolatey nao encontrado no host")
	}
	if manager == packageManagerWinget || manager == "" {
		return packageManagerWinget, packageID, nil
	}
	if manager == packageManagerChocolatey {
		return packageManagerChocolatey, packageID, nil
	}
	return "", "", fmt.Errorf("gerenciador de pacote invalido: %s", manager)
}

func splitPackageManagerTarget(raw string) (string, string) {
	target := strings.TrimSpace(raw)
	parts := strings.SplitN(target, packageManagerTargetSeparator, 2)
	if len(parts) != 2 {
		return "", target
	}
	manager := normalizePackageManager(parts[0])
	packageID := strings.TrimSpace(parts[1])
	if manager == "" {
		return "", target
	}
	return manager, packageID
}

func normalizePackageManager(raw string) string {
	manager := strings.ToLower(strings.TrimSpace(raw))
	switch manager {
	case packageManagerWinget:
		return packageManagerWinget
	case packageManagerChocolatey, packageManagerChocoAlias:
		return packageManagerChocolatey
	default:
		return ""
	}
}
