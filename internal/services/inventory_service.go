package services

import (
	"context"

	"discovery/internal/models"
)

type InventoryProvider interface {
	Collect(ctx context.Context) (models.InventoryReport, error)
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
