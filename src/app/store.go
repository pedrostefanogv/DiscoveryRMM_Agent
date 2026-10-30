package app

import (
	"context"
	"fmt"
	"strings"

	"discovery/app/appstore"
	"discovery/app/core/automation"
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
	if a == nil || a.AppStoreSvc == nil {
		return AppStoreResponse{}, nil
	}
	return a.AppStoreSvc.FetchByInstallationType(ctx, installationType)
}

// fetchAppStorePage faz uma única requisição ao endpoint app-store com cursor opcional.
func (a *App) fetchAppStorePage(ctx context.Context, installationType AppStoreInstallationType, cursor, token, agentID string) (AppStoreResponse, error) {
	if a == nil || a.AppStoreSvc == nil {
		return AppStoreResponse{}, nil
	}
	return a.AppStoreSvc.FetchPage(ctx, installationType, cursor, token, agentID)
}

// loadEffectiveAppStorePolicy carrega a política efetiva da app-store.
func (a *App) loadEffectiveAppStorePolicy(ctx context.Context, forceRefresh bool) (AppStoreEffectivePolicy, error) {
	if a == nil || a.AppStoreSvc == nil {
		return AppStoreEffectivePolicy{}, nil
	}
	return a.AppStoreSvc.LoadEffectivePolicy(ctx, forceRefresh)
}

// getCatalogFromAppStore converte a política efetiva em um catálogo.
func (a *App) getCatalogFromAppStore(ctx context.Context) (models.Catalog, error) {
	if a == nil || a.AppStoreSvc == nil {
		return models.Catalog{}, nil
	}
	catalog, err := a.AppStoreSvc.GetCatalogFromAppStore(ctx)
	if err != nil {
		return catalog, err
	}
	return a.markCustomInstalledInCatalog(ctx, catalog), nil
}

// refreshCatalogFromAppStore força um fetch novo da app-store antes de montar o
// catálogo (botão "atualizar" da loja).
func (a *App) refreshCatalogFromAppStore(ctx context.Context) (models.Catalog, error) {
	if a == nil || a.AppStoreSvc == nil {
		return models.Catalog{}, nil
	}
	catalog, err := a.AppStoreSvc.RefreshCatalogFromAppStore(ctx)
	if err != nil {
		return catalog, err
	}
	return a.markCustomInstalledInCatalog(ctx, catalog), nil
}

// markCustomInstalledInCatalog preenche AppItem.Installed dos pacotes custom com
// detecção por inventário usando UMA coleta do inventário local por montagem do
// catálogo (contrato §4.6). Nunca falha a listagem da loja: sem inventário, os
// itens apenas ficam com Installed=false.
func (a *App) markCustomInstalledInCatalog(ctx context.Context, catalog models.Catalog) models.Catalog {
	if a == nil || a.InventorySvc == nil {
		return catalog
	}
	return a.InventorySvc.MarkCatalogInstalled(ctx, catalog)
}

// findAllowedPackage valida se um pacote está autorizado para o agent.
func (a *App) findAllowedPackage(ctx context.Context, installationType, packageID string) (AppStoreItem, error) {
	if a == nil || a.AppStoreSvc == nil {
		return AppStoreItem{}, nil
	}
	return a.AppStoreSvc.FindAllowedPackage(ctx, installationType, packageID)
}

// resolveAllowedPackage resolve um pacote autorizado, detectando ambiguidade.
func (a *App) resolveAllowedPackage(ctx context.Context, packageID string) (AppStoreItem, error) {
	if a == nil || a.AppStoreSvc == nil {
		return AppStoreItem{}, nil
	}
	return a.AppStoreSvc.ResolveAllowedPackage(ctx, packageID)
}

// authorizeAutomationPackage autoriza um pacote para automação.
//
// Regra de produto: a policy de automação SOBREPÕE a App Store. Antes de
// consultar a política efetiva da Loja, verifica se o (operação,
// installationType, packageId) corresponde a uma task ATIVA da policy de
// automação do agent (estado em memória do automation.Service). Se sim, autoriza
// com um aviso informativo — sem isso, uma task de automação configurada fora
// da Loja falhava no gate com exit 13 ("pacote não autorizado para este agent")
// antes de executar qualquer winget, invisível ao admin, e abria o circuit
// breaker da task. O gate da instalação MANUAL pela Loja (inventory/resolveAllowed)
// não é afetado.
func (a *App) authorizeAutomationPackage(ctx context.Context, installationType, packageID, operation string) error {
	if a == nil {
		return nil
	}
	if a.AutomationSvc != nil && a.AutomationSvc.PackageAuthorizedByPolicy(automation.AppInstallationType(installationType), packageID, operation) {
		a.Logs.Append(fmt.Sprintf("[automation] pacote %q (%s) autorizado pela policy de automação (sobrepõe a App Store)", packageID, installationType))
		return nil
	}
	if a.AppStoreSvc == nil {
		return nil
	}
	return a.AppStoreSvc.AuthorizeAutomationPackage(ctx, installationType, packageID, operation)
}
