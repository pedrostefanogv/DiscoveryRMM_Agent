package app

import "discovery/internal/models"

func (a *App) getInventoryForExport() (models.InventoryReport, error) {
	if cached, ok := a.invCache.get(); ok {
		return cached, nil
	}
	if err := a.requireInventorySvc(); err != nil {
		return models.InventoryReport{}, err
	}

	return a.inventorySvc.GetInventory()
}
