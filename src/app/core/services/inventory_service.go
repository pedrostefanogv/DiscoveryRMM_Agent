package services

import (
	"context"

	"discovery/internal/models"
)

type InventoryProvider interface {
	Collect(ctx context.Context) (models.InventoryReport, error)
	CollectNetworkConnections(ctx context.Context) (models.NetworkConnectionsReport, error)
	CollectSoftware(ctx context.Context) ([]models.SoftwareItem, error)
	CollectStartupItems(ctx context.Context) ([]models.StartupItem, error)
	CollectListeningPorts(ctx context.Context) ([]models.ListeningPortInfo, error)
}

type InventoryService struct {
	provider InventoryProvider
}

func NewInventoryService(provider InventoryProvider) *InventoryService {
	return &InventoryService{provider: provider}
}

func (s *InventoryService) GetInventory(ctx context.Context) (models.InventoryReport, error) {
	return s.provider.Collect(ctx)
}

func (s *InventoryService) GetNetworkConnections(ctx context.Context) (models.NetworkConnectionsReport, error) {
	return s.provider.CollectNetworkConnections(ctx)
}

func (s *InventoryService) CollectSoftware(ctx context.Context) ([]models.SoftwareItem, error) {
	return s.provider.CollectSoftware(ctx)
}

func (s *InventoryService) CollectStartupItems(ctx context.Context) ([]models.StartupItem, error) {
	return s.provider.CollectStartupItems(ctx)
}

func (s *InventoryService) CollectListeningPorts(ctx context.Context) ([]models.ListeningPortInfo, error) {
	return s.provider.CollectListeningPorts(ctx)
}
