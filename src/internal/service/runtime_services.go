package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"discovery/internal/automation"
	"discovery/internal/database"
	"discovery/internal/inventory"
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
}

func NewInventoryRuntimeService(timeout time.Duration, loadConfig func() *SharedConfig) InventoryService {
	return &inventoryRuntimeService{provider: inventory.NewProvider(timeout), loadConfig: loadConfig}
}

func (s *inventoryRuntimeService) Collect(ctx context.Context) (interface{}, error) {
	if s != nil && s.loadConfig != nil {
		cfg := s.loadConfig()
		if cfg == nil || !cfg.IsProvisioned() {
			return nil, fmt.Errorf("inventario indisponivel enquanto o agente nao estiver provisionado")
		}
	}
	return s.provider.Collect(ctx)
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
