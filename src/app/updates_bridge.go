package app

import "discovery/internal/models"

// GetPendingUpdates runs `winget upgrade` and parses the output into structured items.
func (a *App) GetPendingUpdates() ([]models.UpgradeItem, error) {
	if err := a.requireUpdatesSvc(); err != nil {
		return nil, err
	}
	return a.updatesSvc.GetPendingUpdates()
}

// GetPackageActions returns a contextual action map keyed by package id.
// Values: install, uninstall, upgrade.
func (a *App) GetPackageActions() (map[string]string, error) {
	if err := a.requireUpdatesSvc(); err != nil {
		return map[string]string{}, err
	}
	return a.updatesSvc.GetPackageActions()
}

// SetExportRedaction toggles redaction for export.
func (a *App) SetExportRedaction(redact bool) {
	if a == nil || a.exporter == nil {
		return
	}
	a.exporter.SetRedaction(redact)
}

// ExportInventoryMarkdown exports inventory data in Markdown format.
func (a *App) ExportInventoryMarkdown() (string, error) {
	if err := a.requireExporter(); err != nil {
		return "", err
	}
	return a.exporter.ExportInventoryMarkdown()
}

// ExportInventoryPDF exports inventory data in PDF format.
func (a *App) ExportInventoryPDF() (string, error) {
	if err := a.requireExporter(); err != nil {
		return "", err
	}
	return a.exporter.ExportInventoryPDF()
}
