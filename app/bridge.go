package app

import (
	"encoding/json"
	"fmt"
	"strings"

	"discovery/internal/models"
)

// ─── Helpers de guard de serviço ────────────────────────────────────────────
// Centralizam a verificação de nil para cada serviço, eliminando boilerplate
// repetido nas bridges.

func (a *App) requireInventorySvc() error {
	if a == nil || a.inventorySvc == nil {
		return fmt.Errorf("inventory service indisponivel")
	}
	return nil
}

func (a *App) requireSupportSvc() error {
	if a == nil || a.supportSvc == nil {
		return fmt.Errorf("support service indisponivel")
	}
	return nil
}

func (a *App) requireDebugSvc() error {
	if a == nil || a.debugSvc == nil {
		return fmt.Errorf("debug service indisponivel")
	}
	return nil
}

func (a *App) requireUpdatesSvc() error {
	if a == nil || a.updatesSvc == nil {
		return fmt.Errorf("updates service indisponivel")
	}
	return nil
}

func (a *App) requireExporter() error {
	if a == nil || a.exporter == nil {
		return fmt.Errorf("export service indisponivel")
	}
	return nil
}

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
