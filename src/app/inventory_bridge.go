package app

import (
	"context"

	"discovery/app/core/models"
)

func (a *App) GetCatalog() (models.Catalog, error) {
	// Companion mode (D3): resolve o catálogo NO serviço — ele tem a config de
	// conexão correta (a cópia da UI pode estar stale) e o cache persistido da
	// app-store (SQLite 7 dias); a UI não tem DB local. Com a API fora do ar a
	// loja continua carregando pelo cache do serviço. Em falha de IPC (ex.:
	// serviço ausente) cai no caminho local (standalone).
	if catalog, ok := a.getStoreCatalogCompanion(); ok {
		return catalog, nil
	}
	if err := a.requireInventorySvc(); err != nil {
		return models.Catalog{}, err
	}
	return a.InventorySvc.GetCatalog()
}

func (a *App) Install(id string) (string, error) {
	if err := a.requireInventorySvc(); err != nil {
		return "", err
	}
	return a.InventorySvc.Install(id)
}

func (a *App) Uninstall(id string) (string, error) {
	if err := a.requireInventorySvc(); err != nil {
		return "", err
	}
	return a.InventorySvc.Uninstall(id)
}

func (a *App) Upgrade(id string) (string, error) {
	if err := a.requireInventorySvc(); err != nil {
		return "", err
	}
	return a.InventorySvc.Upgrade(id)
}

func (a *App) UpgradeAll() (string, error) {
	if err := a.requireInventorySvc(); err != nil {
		return "", err
	}
	return a.InventorySvc.UpgradeAll()
}

func (a *App) ListInstalled() (string, error) {
	if err := a.requireInventorySvc(); err != nil {
		return "", err
	}
	return a.InventorySvc.ListInstalled()
}

func (a *App) GetInventory() (models.InventoryReport, error) {
	if err := a.requireInventorySvc(); err != nil {
		return models.InventoryReport{}, err
	}
	return a.InventorySvc.GetInventory()
}

func (a *App) RefreshInventory() (models.InventoryReport, error) {
	if err := a.requireInventorySvc(); err != nil {
		return models.InventoryReport{}, err
	}
	return a.InventorySvc.RefreshInventory()
}

func (a *App) RefreshNetworkConnections() (models.NetworkConnectionsReport, error) {
	if err := a.requireInventorySvc(); err != nil {
		return models.NetworkConnectionsReport{}, err
	}
	return a.InventorySvc.RefreshNetworkConnections()
}

func (a *App) SyncNetworkConnections() error {
	if err := a.requireInventorySvc(); err != nil {
		return err
	}
	return a.InventorySvc.SyncNetworkConnections(context.Background())
}

// SyncStartupAndScheduledTasks re-coleta e envia itens de inicialização e
// tarefas agendadas para a API (sync parcial, usado pós-ação de comando).
func (a *App) SyncStartupAndScheduledTasks() error {
	if err := a.requireInventorySvc(); err != nil {
		return err
	}
	return a.InventorySvc.SyncStartupAndScheduledTasks(context.Background())
}

func (a *App) RefreshSoftware() ([]models.SoftwareItem, error) {
	if err := a.requireInventorySvc(); err != nil {
		return []models.SoftwareItem{}, err
	}
	return a.InventorySvc.RefreshSoftware()
}

func (a *App) RefreshStartupItems() ([]models.StartupItem, error) {
	if err := a.requireInventorySvc(); err != nil {
		return []models.StartupItem{}, err
	}
	return a.InventorySvc.RefreshStartupItems()
}

func (a *App) RefreshListeningPorts() ([]models.ListeningPortInfo, error) {
	if err := a.requireInventorySvc(); err != nil {
		return []models.ListeningPortInfo{}, err
	}
	return a.InventorySvc.RefreshListeningPorts()
}

func (a *App) GetOsqueryStatus() (models.OsqueryStatus, error) {
	if err := a.requireInventorySvc(); err != nil {
		return models.OsqueryStatus{}, err
	}
	return a.InventorySvc.GetOsqueryStatus()
}

func (a *App) InstallOsquery() (string, error) {
	if err := a.requireInventorySvc(); err != nil {
		return "", err
	}
	return a.InventorySvc.InstallOsquery()
}

func (a *App) collectInventoryWithHeartbeat(ctx context.Context) (models.InventoryReport, error) {
	if err := a.requireInventorySvc(); err != nil {
		return models.InventoryReport{}, err
	}
	_ = ctx
	return a.InventorySvc.GetInventory()
}

func (a *App) pulseInventoryHeartbeat() {
}
