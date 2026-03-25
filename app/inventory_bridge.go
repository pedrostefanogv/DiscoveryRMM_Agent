package app

import (
	"context"

	"discovery/internal/models"
)

func (a *App) GetCatalog() (models.Catalog, error) {
	if err := a.requireInventorySvc(); err != nil {
		return models.Catalog{}, err
	}
	return a.inventorySvc.GetCatalog()
}

func (a *App) Install(id string) (string, error) {
	if err := a.requireInventorySvc(); err != nil {
		return "", err
	}
	return a.inventorySvc.Install(id)
}

func (a *App) Uninstall(id string) (string, error) {
	if err := a.requireInventorySvc(); err != nil {
		return "", err
	}
	return a.inventorySvc.Uninstall(id)
}

func (a *App) Upgrade(id string) (string, error) {
	if err := a.requireInventorySvc(); err != nil {
		return "", err
	}
	return a.inventorySvc.Upgrade(id)
}

func (a *App) UpgradeAll() (string, error) {
	if err := a.requireInventorySvc(); err != nil {
		return "", err
	}
	return a.inventorySvc.UpgradeAll()
}

func (a *App) ListInstalled() (string, error) {
	if err := a.requireInventorySvc(); err != nil {
		return "", err
	}
	return a.inventorySvc.ListInstalled()
}

func (a *App) GetInventory() (models.InventoryReport, error) {
	if err := a.requireInventorySvc(); err != nil {
		return models.InventoryReport{}, err
	}
	return a.inventorySvc.GetInventory()
}

func (a *App) RefreshInventory() (models.InventoryReport, error) {
	if err := a.requireInventorySvc(); err != nil {
		return models.InventoryReport{}, err
	}
	return a.inventorySvc.RefreshInventory()
}

func (a *App) GetOsqueryStatus() (models.OsqueryStatus, error) {
	if err := a.requireInventorySvc(); err != nil {
		return models.OsqueryStatus{}, err
	}
	return a.inventorySvc.GetOsqueryStatus()
}

func (a *App) InstallOsquery() (string, error) {
	if err := a.requireInventorySvc(); err != nil {
		return "", err
	}
	return a.inventorySvc.InstallOsquery()
}

// keep for API compatibility with earlier calls.
func (a *App) collectInventoryWithHeartbeat(ctx context.Context) (models.InventoryReport, error) {
	if err := a.requireInventorySvc(); err != nil {
		return models.InventoryReport{}, err
	}
	_ = ctx
	return a.inventorySvc.GetInventory()
}

func (a *App) pulseInventoryHeartbeat() {
	if a == nil || a.inventorySvc == nil {
		return
	}
	// no-op: retained for compatibility, inventory service now owns heartbeats.
}
