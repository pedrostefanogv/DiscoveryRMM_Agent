package main

import (
	"encoding/json"
	"strings"

	"winget-store/internal/models"
)

// -----------------------------------------------------------------------
// AppBridge implementation (used by MCP tool registry)
// -----------------------------------------------------------------------

func (a *App) GetInventoryJSON() (json.RawMessage, error) {
	report, err := a.GetInventory()
	if err != nil {
		return nil, err
	}
	return json.Marshal(report)
}

func (a *App) SearchCatalog(query string) (json.RawMessage, error) {
	catalog, err := a.GetCatalog()
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(query)
	var matches []models.AppItem
	for _, item := range catalog.Packages {
		if strings.Contains(strings.ToLower(item.Name), q) ||
			strings.Contains(strings.ToLower(item.ID), q) ||
			strings.Contains(strings.ToLower(item.Publisher), q) {
			matches = append(matches, item)
			if len(matches) >= 20 {
				break
			}
		}
	}
	return json.Marshal(matches)
}

func (a *App) InstallPackage(id string) (string, error)   { return a.Install(id) }
func (a *App) UninstallPackage(id string) (string, error) { return a.Uninstall(id) }
func (a *App) UpgradePackage(id string) (string, error)   { return a.Upgrade(id) }
func (a *App) UpgradeAllPackages() (string, error)        { return a.UpgradeAll() }

func (a *App) GetPendingUpdatesJSON() (json.RawMessage, error) {
	items, err := a.GetPendingUpdates()
	if err != nil {
		return nil, err
	}
	return json.Marshal(items)
}

func (a *App) GetPackageActionsJSON() (json.RawMessage, error) {
	actions, err := a.GetPackageActions()
	if err != nil {
		return nil, err
	}
	return json.Marshal(actions)
}

func (a *App) ExportMarkdown() (string, error) { return a.ExportInventoryMarkdown() }
func (a *App) ExportPDF() (string, error)      { return a.ExportInventoryPDF() }

func (a *App) GetOsqueryStatusJSON() (json.RawMessage, error) {
	status, _ := a.GetOsqueryStatus()
	return json.Marshal(status)
}

func (a *App) GetLogsText() string {
	return strings.Join(a.logs.getAll(), "\n")
}
