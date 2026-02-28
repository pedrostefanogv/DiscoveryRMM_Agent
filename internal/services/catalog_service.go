package services

import (
	"context"

	"winget-store/internal/models"
)

type CatalogProvider interface {
	GetCatalog(ctx context.Context) (models.Catalog, error)
}

type CatalogService struct {
	provider CatalogProvider
}

func NewCatalogService(provider CatalogProvider) *CatalogService {
	return &CatalogService{provider: provider}
}

func (s *CatalogService) GetCatalog(ctx context.Context) (models.Catalog, error) {
	return s.provider.GetCatalog(ctx)
}
