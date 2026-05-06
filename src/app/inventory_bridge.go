package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"discovery/internal/models"
)

const (
	serviceActionStatusPollInterval = 1 * time.Second
	serviceActionStatusTimeout      = 20 * time.Minute
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
	if a.shouldRouteChocolateyUpgradeViaService(id) {
		return a.executeServicePackageAction("upgrade_package", id)
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

func (a *App) RefreshNetworkConnections() (models.NetworkConnectionsReport, error) {
	if err := a.requireInventorySvc(); err != nil {
		return models.NetworkConnectionsReport{}, err
	}
	return a.inventorySvc.RefreshNetworkConnections()
}

func (a *App) RefreshSoftware() ([]models.SoftwareItem, error) {
	if err := a.requireInventorySvc(); err != nil {
		return []models.SoftwareItem{}, err
	}
	return a.inventorySvc.RefreshSoftware()
}

func (a *App) RefreshStartupItems() ([]models.StartupItem, error) {
	if err := a.requireInventorySvc(); err != nil {
		return []models.StartupItem{}, err
	}
	return a.inventorySvc.RefreshStartupItems()
}

func (a *App) RefreshListeningPorts() ([]models.ListeningPortInfo, error) {
	if err := a.requireInventorySvc(); err != nil {
		return []models.ListeningPortInfo{}, err
	}
	return a.inventorySvc.RefreshListeningPorts()
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
	// no-op: watchdog system removed.
}

func (a *App) shouldRouteChocolateyUpgradeViaService(target string) bool {
	if a == nil || !a.serviceConnectedMode.Load() || a.serviceClient == nil {
		return false
	}
	source, _ := splitUpgradeTargetSource(target)
	return source == "chocolatey"
}

func splitUpgradeTargetSource(target string) (string, string) {
	target = strings.TrimSpace(target)
	parts := strings.SplitN(target, "::", 2)
	if len(parts) != 2 {
		return "", target
	}
	source := strings.ToLower(strings.TrimSpace(parts[0]))
	if source == "choco" {
		source = "chocolatey"
	}
	return source, strings.TrimSpace(parts[1])
}

func (a *App) executeServicePackageAction(action, packageTarget string) (string, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	if !a.serviceClient.IsConnected() {
		if err := a.serviceClient.Connect(ctx); err != nil {
			return "", fmt.Errorf("falha ao conectar no Windows Service: %w", err)
		}
	}

	payload := map[string]interface{}{"package": strings.TrimSpace(packageTarget)}
	resp, err := a.serviceClient.Execute(ctx, action, payload)
	if err != nil {
		_ = a.serviceClient.Close()
		if reconnectErr := a.serviceClient.Connect(ctx); reconnectErr != nil {
			return "", fmt.Errorf("falha ao reconectar no Windows Service: %w", reconnectErr)
		}
		resp, err = a.serviceClient.Execute(ctx, action, payload)
		if err != nil {
			return "", fmt.Errorf("falha ao enfileirar acao no Windows Service: %w", err)
		}
	}

	if resp == nil {
		return "", fmt.Errorf("resposta vazia do Windows Service")
	}
	if resp.Code >= 400 {
		message := strings.TrimSpace(resp.Message)
		if message == "" {
			message = "falha ao enfileirar acao no Windows Service"
		}
		return "", errors.New(message)
	}

	actionID := serviceActionIDFromResponse(resp.Data)
	if actionID == "" {
		return strings.TrimSpace(resp.Message), nil
	}

	return a.waitForServiceActionCompletion(ctx, actionID)
}

func serviceActionIDFromResponse(data map[string]interface{}) string {
	if data == nil {
		return ""
	}
	raw, ok := data["action_id"]
	if !ok || raw == nil {
		return ""
	}
	value := strings.TrimSpace(fmt.Sprint(raw))
	if strings.EqualFold(value, "<nil>") {
		return ""
	}
	return value
}

func (a *App) waitForServiceActionCompletion(parent context.Context, actionID string) (string, error) {
	waitCtx, cancel := context.WithTimeout(parent, serviceActionStatusTimeout)
	defer cancel()

	ticker := time.NewTicker(serviceActionStatusPollInterval)
	defer ticker.Stop()

	for {
		resp, err := a.serviceClient.GetActionStatus(waitCtx, actionID)
		if err != nil {
			return "", fmt.Errorf("falha ao consultar status da acao no service: %w", err)
		}
		if resp == nil {
			return "", fmt.Errorf("resposta vazia ao consultar status da acao")
		}
		if resp.Code >= 400 {
			message := strings.TrimSpace(resp.Message)
			if message == "" {
				message = "falha ao consultar status da acao no service"
			}
			return "", errors.New(message)
		}

		status := strings.ToLower(strings.TrimSpace(fmt.Sprint(resp.Data["status"])))
		switch status {
		case "completed":
			return "acao concluida via Windows Service", nil
		case "failed":
			message := strings.TrimSpace(fmt.Sprint(resp.Data["error_message"]))
			if message == "" || strings.EqualFold(message, "<nil>") {
				message = "acao falhou no Windows Service"
			}
			return "", errors.New(message)
		}

		select {
		case <-waitCtx.Done():
			return "", fmt.Errorf("tempo limite aguardando conclusao da acao no Windows Service")
		case <-ticker.C:
		}
	}
}
