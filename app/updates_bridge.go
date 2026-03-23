package app

import (
	"fmt"

	"discovery/internal/models"
)

// GetPendingUpdates runs `winget upgrade` and parses the output into structured items.
func (a *App) GetPendingUpdates() ([]models.UpgradeItem, error) {
	if a == nil || a.updatesSvc == nil {
		return nil, fmt.Errorf("updates service indisponivel")
	}
	return a.updatesSvc.GetPendingUpdates()
}

// GetPackageActions returns a contextual action map keyed by package id.
// Values: install, uninstall, upgrade.
func (a *App) GetPackageActions() (map[string]string, error) {
	if a == nil || a.updatesSvc == nil {
		return map[string]string{}, fmt.Errorf("updates service indisponivel")
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
	if a == nil || a.exporter == nil {
		return "", fmt.Errorf("export service indisponivel")
	}
	return a.exporter.ExportInventoryMarkdown()
}

// ExportInventoryPDF exports inventory data in PDF format.
func (a *App) ExportInventoryPDF() (string, error) {
	if a == nil || a.exporter == nil {
		return "", fmt.Errorf("export service indisponivel")
	}
	return a.exporter.ExportInventoryPDF()
}
