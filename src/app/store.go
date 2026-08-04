package app

import (
	"context"
	"strings"

	"discovery/app/appstore"
	"discovery/app/core/models"
)

// normalizeAppStoreInstallationType normaliza o tipo de instalação.
func normalizeAppStoreInstallationType(value string) string {
	return appstore.NormalizeInstallationType(value)
}

func appStoreLookupKey(installationType, packageID string) string {
	return strings.ToLower(strings.TrimSpace(installationType)) + "|" + strings.ToLower(strings.TrimSpace(packageID))
}

// fetchAppStoreByInstallationType coleta todas as páginas via cursor pagination (CQRS).
func (a *App) fetchAppStoreByInstallationType(ctx context.Context, installationType AppStoreInstallationType) (AppStoreResponse, error) {
	if a == nil || a.appStoreSvc == nil {
		return AppStoreResponse{}, nil
	}
	return a.appStoreSvc.FetchByInstallationType(ctx, installationType)
}

// fetchAppStorePage faz uma única requisição ao endpoint app-store com cursor opcional.
func (a *App) fetchAppStorePage(ctx context.Context, installationType AppStoreInstallationType, cursor, token, agentID string) (AppStoreResponse, error) {
	if a == nil || a.appStoreSvc == nil {
		return AppStoreResponse{}, nil
	}
	return a.appStoreSvc.FetchPage(ctx, installationType, cursor, token, agentID)
}

// loadEffectiveAppStorePolicy carrega a política efetiva da app-store.
func (a *App) loadEffectiveAppStorePolicy(ctx context.Context, forceRefresh bool) (AppStoreEffectivePolicy, error) {
	if a == nil || a.appStoreSvc == nil {
		return AppStoreEffectivePolicy{}, nil
	}
	return a.appStoreSvc.LoadEffectivePolicy(ctx, forceRefresh)
}

// getCatalogFromAppStore converte a política efetiva em um catálogo.
func (a *App) getCatalogFromAppStore(ctx context.Context) (models.Catalog, error) {
	if a == nil || a.appStoreSvc == nil {
		return models.Catalog{}, nil
	}
	return a.appStoreSvc.GetCatalogFromAppStore(ctx)
}

// findAllowedPackage valida se um pacote está autorizado para o agent.
func (a *App) findAllowedPackage(ctx context.Context, installationType, packageID string) (AppStoreItem, error) {
	if a == nil || a.appStoreSvc == nil {
		return AppStoreItem{}, nil
	}
	return a.appStoreSvc.FindAllowedPackage(ctx, installationType, packageID)
}

// resolveAllowedPackage resolve um pacote autorizado, detectando ambiguidade.
func (a *App) resolveAllowedPackage(ctx context.Context, packageID string) (AppStoreItem, error) {
	if a == nil || a.appStoreSvc == nil {
		return AppStoreItem{}, nil
	}
	return a.appStoreSvc.ResolveAllowedPackage(ctx, packageID)
}

// authorizeAutomationPackage autoriza um pacote para automação.
func (a *App) authorizeAutomationPackage(ctx context.Context, installationType, packageID, operation string) error {
	if a == nil || a.appStoreSvc == nil {
		return nil
	}
	return a.appStoreSvc.AuthorizeAutomationPackage(ctx, installationType, packageID, operation)
}
