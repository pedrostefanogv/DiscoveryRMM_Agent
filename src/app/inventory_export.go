package app

import "discovery/internal/models"

func (a *App) getInventoryForExport() (models.InventoryReport, error) {
	if cached, ok := a.invCache.get(); ok {
		return cached, nil
	}

	report, err := a.invSvc.GetInventory(a.ctx)
	if err != nil {
		return models.InventoryReport{}, err
	}
	a.invCache.set(report)
	return report, nil
}
