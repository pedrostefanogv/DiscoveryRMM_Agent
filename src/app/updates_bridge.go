package app

import (
	"context"

	"discovery/app/core/models"
)

// GetPendingUpdates runs `winget upgrade` and parses the output into structured items.
// Companion mode: o scan roda NO serviço via RPC updates:scan (decisão D2 —
// winget/updates no serviço; a UI só exibe). Standalone: executa localmente.
func (a *App) GetPendingUpdates() ([]models.UpgradeItem, error) {
	if items, ok := a.getPendingUpdatesCompanion(); ok {
		return items, nil
	}
	if err := a.requireUpdatesSvc(); err != nil {
		return nil, err
	}
	return a.UpdatesSvc.GetPendingUpdates()
}

// pendingUpdatesForInventory expõe os updates pendentes (winget + chocolatey)
// ao inventário de software, que os usa para reportar a versão disponível por
// app. Best-effort: sem o serviço de updates retorna vazio.
func (a *App) pendingUpdatesForInventory(_ context.Context) ([]models.UpgradeItem, error) {
	if a == nil || a.UpdatesSvc == nil {
		return nil, nil
	}
	return a.UpdatesSvc.GetPendingUpdates()
}

// GetPackageActions returns a contextual action map keyed by package id.
// Values: install, uninstall, upgrade.
func (a *App) GetPackageActions() (map[string]string, error) {
	if err := a.requireUpdatesSvc(); err != nil {
		return map[string]string{}, err
	}
	return a.UpdatesSvc.GetPackageActions()
}

// SetExportRedaction toggles redaction for export.
func (a *App) SetExportRedaction(redact bool) {
	if a == nil || a.Exporter == nil {
		return
	}
	a.Exporter.SetRedaction(redact)
}

// ExportInventoryMarkdown exports inventory data in Markdown format.
func (a *App) ExportInventoryMarkdown() (string, error) {
	if err := a.requireExporter(); err != nil {
		return "", err
	}
	return a.Exporter.ExportInventoryMarkdown()
}

// ExportInventoryPDF exports inventory data in PDF format.
func (a *App) ExportInventoryPDF() (string, error) {
	if err := a.requireExporter(); err != nil {
		return "", err
	}
	return a.Exporter.ExportInventoryPDF()
}
