package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	appdebug "discovery/app/debug"
	appinventory "discovery/app/inventory"
	"discovery/internal/automation"
	"discovery/internal/database"
	"discovery/internal/inventory"
	"discovery/internal/models"
	"discovery/internal/services"
	"discovery/internal/winget"
)

type automationRuntimeService struct {
	svc *automation.Service
}

func NewAutomationRuntimeService(loadConfig func() *SharedConfig, logger func(string)) AutomationService {
	service := automation.NewService(func() automation.RuntimeConfig {
		cfg := loadConfig()
		if cfg == nil {
			return automation.RuntimeConfig{}
		}

		baseURL := strings.TrimSpace(cfg.ServerURL)
		if scheme := strings.TrimSpace(cfg.ApiScheme); scheme != "" {
			if server := strings.TrimSpace(cfg.ApiServer); server != "" {
				baseURL = scheme + "://" + server
			}
		}

		return automation.RuntimeConfig{
			BaseURL: baseURL,
			Token:   strings.TrimSpace(cfg.AuthToken),
			AgentID: strings.TrimSpace(cfg.AgentID),
		}
	}, logger)

	return &automationRuntimeService{svc: service}
}

func (s *automationRuntimeService) RefreshPolicy(ctx context.Context, includeScriptContent bool) (interface{}, error) {
	return s.svc.RefreshPolicy(ctx, includeScriptContent)
}

func (s *automationRuntimeService) GetCurrentTasks() []automation.AutomationTask {
	state := s.svc.GetState()
	if !state.Available || len(state.Tasks) == 0 {
		return nil
	}
	return state.Tasks
}

func (s *automationRuntimeService) SetDB(db *database.DB) {
	if s == nil || s.svc == nil {
		return
	}
	s.svc.SetDB(db)
}

type inventoryRuntimeService struct {
	provider   *inventory.Provider
	loadConfig func() *SharedConfig
	collect    func(context.Context) (models.InventoryReport, error)
	db         *database.DB
	logf       func(string)
	version    string
}

func NewInventoryRuntimeService(timeout time.Duration, loadConfig func() *SharedConfig, logf func(string), version string) InventoryService {
	if logf == nil {
		logf = func(string) {}
	}
	return &inventoryRuntimeService{
		provider:   inventory.NewProvider(timeout),
		loadConfig: loadConfig,
		logf:       logf,
		version:    strings.TrimSpace(version),
	}
}

func (s *inventoryRuntimeService) Collect(ctx context.Context) (interface{}, error) {
	if s != nil && s.loadConfig != nil {
		cfg := s.loadConfig()
		if cfg == nil || !cfg.IsProvisioned() {
			return nil, fmt.Errorf("inventario indisponivel enquanto o agente nao estiver provisionado")
		}
	}

	var (
		report models.InventoryReport
		err    error
	)
	if s.collect != nil {
		report, err = s.collect(ctx)
	} else if s.provider != nil {
		report, err = s.provider.Collect(ctx)
	} else {
		return nil, fmt.Errorf("inventory provider nao configurado")
	}
	if err != nil {
		return nil, err
	}

	s.syncCollectedInventory(ctx, report)
	return report, nil
}

func (s *inventoryRuntimeService) SetDB(db *database.DB) {
	if s == nil {
		return
	}
	s.db = db
}

func (s *inventoryRuntimeService) syncCollectedInventory(ctx context.Context, report models.InventoryReport) {
	syncSvc := appinventory.NewService(appinventory.Options{
		DB: s.db,
		DebugConfig: func() appdebug.Config {
			return sharedConfigToDebugConfig(s.loadConfig)
		},
		Logf:    s.logf,
		Version: s.version,
	})
	syncSvc.SyncInventoryOnStartup(ctx, report)
}

func sharedConfigToDebugConfig(loadConfig func() *SharedConfig) appdebug.Config {
	if loadConfig == nil {
		return appdebug.Config{}
	}

	cfg := loadConfig()
	if cfg == nil {
		return appdebug.Config{}
	}

	return appdebug.Config{
		ApiScheme: strings.TrimSpace(strings.ToLower(cfg.ApiScheme)),
		ApiServer: strings.TrimSpace(cfg.ApiServer),
		AuthToken: strings.TrimSpace(cfg.AuthToken),
		AgentID:   strings.TrimSpace(cfg.AgentID),
	}
}

type appsRuntimeService struct {
	svc *services.AppsService
}

func NewAppsRuntimeService(timeout time.Duration) AppsService {
	return &appsRuntimeService{svc: services.NewAppsService(winget.NewClient(timeout))}
}

func (s *appsRuntimeService) Install(ctx context.Context, id string) (string, error) {
	return s.svc.Install(ctx, id)
}

func (s *appsRuntimeService) Uninstall(ctx context.Context, id string) (string, error) {
	return s.svc.Uninstall(ctx, id)
}

func (s *appsRuntimeService) Upgrade(ctx context.Context, id string) (string, error) {
	return s.svc.Upgrade(ctx, id)
}

func (s *appsRuntimeService) UpgradeAll(ctx context.Context) (string, error) {
	return s.svc.UpgradeAll(ctx)
}
