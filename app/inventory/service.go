package inventory

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"discovery/app/appstore"
	"discovery/app/debug"
	"discovery/internal/inventory"
	"discovery/internal/models"
	"discovery/internal/processutil"
	"discovery/internal/watchdog"
)

// AppsService defines the package manager surface used by inventory operations.
type AppsService interface {
	Install(ctx context.Context, id string) (string, error)
	Uninstall(ctx context.Context, id string) (string, error)
	Upgrade(ctx context.Context, id string) (string, error)
	UpgradeAll(ctx context.Context) (string, error)
	ListInstalled(ctx context.Context) (string, error)
}

// InventoryService exposes inventory collection operations.
type InventoryService interface {
	GetInventory(ctx context.Context) (models.InventoryReport, error)
}

// InventoryCache handles caching of inventory reports.
type InventoryCache interface {
	Get() (models.InventoryReport, bool)
	Set(models.InventoryReport)
}

// DB handles inventory sync persistence.
type DB interface {
	ShouldSyncInventory(agentID string, hardwareBody, softwareBody []byte) (bool, string, error)
	SaveInventorySnapshot(agentID string, hardwareBody, softwareBody []byte) error
	UpdateLastSyncTime(key, status string) error
}

// ActivityFunc starts and ends a user-visible activity.
type ActivityFunc func(string) func()

// Options wires the inventory service.
type Options struct {
	Apps                AppsService
	Inventory           InventoryService
	Cache               InventoryCache
	Watchdog            *watchdog.Watchdog
	ResolveAllowed      func(context.Context, string) (appstore.Item, error)
	GetCatalog          func(context.Context) (models.Catalog, error)
	BeginActivity       ActivityFunc
	Logf                func(string)
	Ctx                 func() context.Context
	DB                  DB
	DebugConfig         func() debug.Config
	Version             string
}

// Service handles inventory, installs and sync operations.
type Service struct {
	apps          AppsService
	inventory     InventoryService
	cache         InventoryCache
	watchdog      *watchdog.Watchdog
	resolveAllowed func(context.Context, string) (appstore.Item, error)
	getCatalog    func(context.Context) (models.Catalog, error)
	beginActivity ActivityFunc
	logf          func(string)
	ctx           func() context.Context
	db            DB
	debugConfig   func() debug.Config
	version       string
}

// NewService builds an inventory service.
func NewService(opts Options) *Service {
	logf := opts.Logf
	if logf == nil {
		logf = func(string) {}
	}
	return &Service{
		apps:          opts.Apps,
		inventory:     opts.Inventory,
		cache:         opts.Cache,
		watchdog:      opts.Watchdog,
		resolveAllowed: opts.ResolveAllowed,
		getCatalog:    opts.GetCatalog,
		beginActivity: opts.BeginActivity,
		logf:          logf,
		ctx:           opts.Ctx,
		db:            opts.DB,
		debugConfig:   opts.DebugConfig,
		version:       opts.Version,
	}
}

// GetCatalog resolves the package catalog.
func (s *Service) GetCatalog() (models.Catalog, error) {
	if s.getCatalog == nil {
		return models.Catalog{}, fmt.Errorf("catalogo indisponivel")
	}
	ctx := s.ctx()
	return s.getCatalog(ctx)
}

// Install installs the selected package.
func (s *Service) Install(id string) (string, error) {
	done := s.beginActivity("instalacao")
	if done != nil {
		defer done()
	}
	s.logf("[install " + id + "] " + time.Now().Format("15:04:05"))
	allowed, err := s.resolveAllowed(s.ctx(), id)
	if err != nil {
		s.logf("[install blocked] " + err.Error())
		return "", err
	}

	var out string
	switch normalizeAppStoreInstallationType(allowed.InstallationType) {
	case string(appstore.InstallationWinget):
		out, err = s.apps.Install(s.ctx(), id)
	case string(appstore.InstallationChocolatey):
		out, err = s.runChocolatey(s.ctx(), "install", id)
	default:
		err = fmt.Errorf("installationType %q nao suportado", allowed.InstallationType)
	}
	s.logf(out)
	return out, err
}

// Uninstall removes a package.
func (s *Service) Uninstall(id string) (string, error) {
	done := s.beginActivity("desinstalacao")
	if done != nil {
		defer done()
	}
	s.logf("[uninstall " + id + "] " + time.Now().Format("15:04:05"))
	out, err := s.apps.Uninstall(s.ctx(), id)
	s.logf(out)
	return out, err
}

// Upgrade upgrades a package.
func (s *Service) Upgrade(id string) (string, error) {
	done := s.beginActivity("atualizacao")
	if done != nil {
		defer done()
	}
	s.logf("[upgrade " + id + "] " + time.Now().Format("15:04:05"))
	allowed, err := s.resolveAllowed(s.ctx(), id)
	if err != nil {
		s.logf("[upgrade blocked] " + err.Error())
		return "", err
	}

	var out string
	switch normalizeAppStoreInstallationType(allowed.InstallationType) {
	case string(appstore.InstallationWinget):
		out, err = s.apps.Upgrade(s.ctx(), id)
	case string(appstore.InstallationChocolatey):
		out, err = s.runChocolatey(s.ctx(), "upgrade", id)
	default:
		err = fmt.Errorf("installationType %q nao suportado", allowed.InstallationType)
	}
	s.logf(out)
	return out, err
}

// UpgradeAll upgrades all packages.
func (s *Service) UpgradeAll() (string, error) {
	done := s.beginActivity("atualizacao em lote")
	if done != nil {
		defer done()
	}
	s.logf("[upgrade --all] " + time.Now().Format("15:04:05"))
	out, err := s.apps.UpgradeAll(s.ctx())
	s.logf(out)
	return out, err
}

// ListInstalled lists installed packages.
func (s *Service) ListInstalled() (string, error) {
	done := s.beginActivity("listagem de instalados")
	if done != nil {
		defer done()
	}
	out, err := s.apps.ListInstalled(s.ctx())
	s.logf("[list] " + time.Now().Format("15:04:05"))
	s.logf(out)
	return out, err
}

func (s *Service) pulseInventoryHeartbeat() {
	if s.watchdog != nil {
		s.watchdog.Heartbeat(watchdog.ComponentInventory)
	}
}

func (s *Service) collectInventoryWithHeartbeat(ctx context.Context) (models.InventoryReport, error) {
	if s.watchdog == nil {
		return s.inventory.GetInventory(ctx)
	}

	heartbeat := watchdog.NewPeriodicHeartbeat(s.watchdog, watchdog.ComponentInventory, 20*time.Second)
	heartbeat.Start(ctx)
	defer heartbeat.Stop()

	return s.inventory.GetInventory(ctx)
}

// GetInventory returns cached inventory or collects if needed.
func (s *Service) GetInventory() (models.InventoryReport, error) {
	done := s.beginActivity("coleta de inventario")
	if done != nil {
		defer done()
	}
	if s.cache != nil {
		if cached, ok := s.cache.Get(); ok {
			return cached, nil
		}
	}

	report, err := s.collectInventoryWithHeartbeat(s.ctx())
	if err != nil {
		return models.InventoryReport{}, err
	}
	if s.cache != nil {
		s.cache.Set(report)
	}
	return report, nil
}

// RefreshInventory collects inventory and refreshes cache.
func (s *Service) RefreshInventory() (models.InventoryReport, error) {
	done := s.beginActivity("atualizacao de inventario")
	if done != nil {
		defer done()
	}
	report, err := s.collectInventoryWithHeartbeat(s.ctx())
	if err != nil {
		return models.InventoryReport{}, err
	}
	if s.cache != nil {
		s.cache.Set(report)
	}
	return report, nil
}

// GetOsqueryStatus exposes osquery status info.
func (s *Service) GetOsqueryStatus() (models.OsqueryStatus, error) {
	return inventory.GetOsqueryStatus(), nil
}

// InstallOsquery installs osquery if needed.
func (s *Service) InstallOsquery() (string, error) {
	status := inventory.GetOsqueryStatus()
	if status.Installed {
		if status.Path != "" {
			return "osquery ja instalado em " + status.Path, nil
		}
		return "osquery ja instalado", nil
	}
	allowed, err := s.resolveAllowed(s.ctx(), status.SuggestedPackageID)
	if err != nil {
		return "", err
	}

	var out string
	switch normalizeAppStoreInstallationType(allowed.InstallationType) {
	case string(appstore.InstallationWinget):
		out, err = s.apps.Install(s.ctx(), status.SuggestedPackageID)
	case string(appstore.InstallationChocolatey):
		out, err = s.runChocolatey(s.ctx(), "install", status.SuggestedPackageID)
	default:
		err = fmt.Errorf("installationType %q nao suportado", allowed.InstallationType)
	}
	if err != nil {
		return out, err
	}
	inventory.InvalidateOsqueryBinaryCache()
	return out, nil
}

func (s *Service) runChocolatey(ctx context.Context, operation, packageID string) (string, error) {
	if _, err := exec.LookPath("choco"); err != nil {
		return "", fmt.Errorf("Chocolatey nao encontrado no host")
	}

	packageID = strings.TrimSpace(packageID)
	if packageID == "" {
		return "", fmt.Errorf("id do pacote e obrigatorio")
	}

	args := []string{operation, packageID, "-y", "--no-progress"}
	if operation == "install" {
		args = []string{"install", packageID, "-y", "--no-progress"}
	}
	if operation == "upgrade" {
		args = []string{"upgrade", packageID, "-y", "--no-progress"}
	}

	runCtx := ctx
	if runCtx == nil {
		runCtx = context.Background()
	}
	cmd := exec.CommandContext(runCtx, "choco", args...)
	processutil.HideWindow(cmd)
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return text, fmt.Errorf("erro executando chocolatey %s: %w", strings.Join(args, " "), err)
	}
	return text, nil
}

func normalizeAppStoreInstallationType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "winget":
		return string(appstore.InstallationWinget)
	case "chocolatey":
		return string(appstore.InstallationChocolatey)
	default:
		return strings.TrimSpace(value)
	}
}
