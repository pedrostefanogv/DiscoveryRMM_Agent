package app

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"discovery/internal/inventory"
	"discovery/internal/models"
	"discovery/internal/processutil"
	"discovery/internal/watchdog"
)

func (a *App) GetCatalog() (models.Catalog, error) {
	return a.getCatalogFromAppStore(a.ctx)
}

func (a *App) Install(id string) (string, error) {
	done := a.beginActivity("instalacao")
	defer done()
	a.logs.append("[install " + id + "] " + time.Now().Format("15:04:05"))
	allowed, err := a.resolveAllowedPackage(a.ctx, id)
	if err != nil {
		a.logs.append("[install blocked] " + err.Error())
		return "", err
	}

	var out string
	switch normalizeAppStoreInstallationType(allowed.InstallationType) {
	case string(AppStoreInstallationWinget):
		out, err = a.appsSvc.Install(a.ctx, id)
	case string(AppStoreInstallationChocolatey):
		out, err = a.runChocolatey(a.ctx, "install", id)
	default:
		err = fmt.Errorf("installationType %q não suportado", allowed.InstallationType)
	}
	a.logs.append(out)
	return out, err
}

func (a *App) Uninstall(id string) (string, error) {
	done := a.beginActivity("desinstalacao")
	defer done()
	a.logs.append("[uninstall " + id + "] " + time.Now().Format("15:04:05"))
	out, err := a.appsSvc.Uninstall(a.ctx, id)
	a.logs.append(out)
	return out, err
}

func (a *App) Upgrade(id string) (string, error) {
	done := a.beginActivity("atualizacao")
	defer done()
	a.logs.append("[upgrade " + id + "] " + time.Now().Format("15:04:05"))
	allowed, err := a.resolveAllowedPackage(a.ctx, id)
	if err != nil {
		a.logs.append("[upgrade blocked] " + err.Error())
		return "", err
	}

	var out string
	switch normalizeAppStoreInstallationType(allowed.InstallationType) {
	case string(AppStoreInstallationWinget):
		out, err = a.appsSvc.Upgrade(a.ctx, id)
	case string(AppStoreInstallationChocolatey):
		out, err = a.runChocolatey(a.ctx, "upgrade", id)
	default:
		err = fmt.Errorf("installationType %q não suportado", allowed.InstallationType)
	}
	a.logs.append(out)
	return out, err
}

func (a *App) UpgradeAll() (string, error) {
	done := a.beginActivity("atualizacao em lote")
	defer done()
	a.logs.append("[upgrade --all] " + time.Now().Format("15:04:05"))
	out, err := a.appsSvc.UpgradeAll(a.ctx)
	a.logs.append(out)
	return out, err
}

func (a *App) ListInstalled() (string, error) {
	done := a.beginActivity("listagem de instalados")
	defer done()
	out, err := a.appsSvc.ListInstalled(a.ctx)
	a.logs.append("[list] " + time.Now().Format("15:04:05"))
	a.logs.append(out)
	return out, err
}

func (a *App) pulseInventoryHeartbeat() {
	if a.watchdogSvc != nil {
		a.watchdogSvc.Heartbeat(watchdog.ComponentInventory)
	}
}

func (a *App) collectInventoryWithHeartbeat(ctx context.Context) (models.InventoryReport, error) {
	if a.watchdogSvc == nil {
		return a.invSvc.GetInventory(ctx)
	}

	heartbeat := watchdog.NewPeriodicHeartbeat(a.watchdogSvc, watchdog.ComponentInventory, 20*time.Second)
	heartbeat.Start(ctx)
	defer heartbeat.Stop()

	return a.invSvc.GetInventory(ctx)
}

func (a *App) GetInventory() (models.InventoryReport, error) {
	done := a.beginActivity("coleta de inventario")
	defer done()
	if cached, ok := a.invCache.get(); ok {
		return cached, nil
	}

	report, err := a.collectInventoryWithHeartbeat(a.ctx)
	if err != nil {
		return models.InventoryReport{}, err
	}
	a.invCache.set(report)
	return report, nil
}

func (a *App) RefreshInventory() (models.InventoryReport, error) {
	done := a.beginActivity("atualizacao de inventario")
	defer done()
	report, err := a.collectInventoryWithHeartbeat(a.ctx)
	if err != nil {
		return models.InventoryReport{}, err
	}
	a.invCache.set(report)
	return report, nil
}

func (a *App) GetOsqueryStatus() (models.OsqueryStatus, error) {
	return inventory.GetOsqueryStatus(), nil
}

func (a *App) InstallOsquery() (string, error) {
	status := inventory.GetOsqueryStatus()
	if status.Installed {
		if status.Path != "" {
			return "osquery ja instalado em " + status.Path, nil
		}
		return "osquery ja instalado", nil
	}
	allowed, err := a.resolveAllowedPackage(a.ctx, status.SuggestedPackageID)
	if err != nil {
		return "", err
	}

	var out string
	switch normalizeAppStoreInstallationType(allowed.InstallationType) {
	case string(AppStoreInstallationWinget):
		out, err = a.appsSvc.Install(a.ctx, status.SuggestedPackageID)
	case string(AppStoreInstallationChocolatey):
		out, err = a.runChocolatey(a.ctx, "install", status.SuggestedPackageID)
	default:
		err = fmt.Errorf("installationType %q não suportado", allowed.InstallationType)
	}
	if err != nil {
		return out, err
	}
	inventory.InvalidateOsqueryBinaryCache()
	return out, nil
}

func (a *App) runChocolatey(ctx context.Context, operation, packageID string) (string, error) {
	if _, err := exec.LookPath("choco"); err != nil {
		return "", fmt.Errorf("Chocolatey não encontrado no host")
	}

	packageID = strings.TrimSpace(packageID)
	if packageID == "" {
		return "", fmt.Errorf("id do pacote é obrigatório")
	}

	args := []string{operation, packageID, "-y", "--no-progress"}
	if operation == "install" {
		args = []string{"install", packageID, "-y", "--no-progress"}
	}
	if operation == "upgrade" {
		args = []string{"upgrade", packageID, "-y", "--no-progress"}
	}

	runCtx := ctx
	if runCtx == nil {
		runCtx = context.Background()
	}
	cmd := exec.CommandContext(runCtx, "choco", args...)
	processutil.HideWindow(cmd)
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return text, fmt.Errorf("erro executando chocolatey %s: %w", strings.Join(args, " "), err)
	}
	return text, nil
}

// GetPendingUpdates runs `winget upgrade` and parses the output into structured items.
// GetLogs returns the accumulated command log lines.
func (a *App) GetLogs() []string {
	return a.logs.getAll()
}

// ClearLogs empties the log buffer.
func (a *App) ClearLogs() {
	a.logs.clear()
}
