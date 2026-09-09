package app

import "discovery/app/core/models"

func (a *App) getInventoryForExport() (models.InventoryReport, error) {
	if cached, ok := a.InvCache.Get(); ok {
		return cached, nil
	}
	if err := a.requireInventorySvc(); err != nil {
		return models.InventoryReport{}, err
	}

	return a.InventorySvc.GetInventory()
}
