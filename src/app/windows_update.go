package app

import (
	"context"
	"hash/fnv"
	"strings"
	"time"
)

const (
	windowsUpdateInitialDelayDefault = 10 * time.Minute
	windowsUpdateJitterMax           = 5 * time.Minute
	windowsUpdateScanIntervalDefault = 6 * time.Hour
	windowsUpdateScanIntervalMinimum = time.Hour
)

// windowsUpdateInitialDelay espalha o primeiro scan da frota: 10 min + jitter
// deterministico de ate 5 min derivado do agentId (mesmo agente, mesmo atraso
// entre reinicios — evita sincronia artificial).
func windowsUpdateInitialDelay(_ context.Context, a *App) time.Duration {
	jitter := time.Duration(0)
	if a != nil {
		agentID := strings.TrimSpace(a.GetDebugConfig().AgentID)
		if agentID != "" {
			h := fnv.New32a()
			_, _ = h.Write([]byte(agentID))
			jitter = time.Duration(h.Sum32()%uint32(windowsUpdateJitterMax/time.Second)) * time.Second
		}
	}
	return windowsUpdateInitialDelayDefault + jitter
}

// windowsUpdateScanInterval resolve a cadencia do scan a partir da config
// efetiva do agente (default 6h, minimo 1h).
func (a *App) windowsUpdateScanInterval() time.Duration {
	if a == nil {
		return windowsUpdateScanIntervalDefault
	}
	minutes := 0
	if cfg := a.GetAgentConfiguration(); cfg.WindowsUpdateScanIntervalMinutes != nil {
		minutes = *cfg.WindowsUpdateScanIntervalMinutes
	}
	interval := windowsUpdateScanIntervalDefault
	if minutes > 0 {
		interval = time.Duration(minutes) * time.Minute
	}
	if interval < windowsUpdateScanIntervalMinimum {
		interval = windowsUpdateScanIntervalMinimum
	}
	return interval
}

// windowsUpdatesReady informa se o modulo pode rodar agora.
//
// Em UI companion (service presente via IPC) o WUA NÃO roda: o serviço
// (LocalSystem) é quem faz scan/instalação. Rodar nos dois processos duplicaria
// scans e poderia falhar por integridade/Contexto.
func (a *App) windowsUpdatesReady() bool {
	if a == nil || a.InventorySvc == nil {
		return false
	}
	if a.ipcClient != nil {
		return false
	}
	if !a.isInventoryProvisioned() {
		return false
	}
	return a.InventorySvc.WindowsUpdateSupported()
}

// runWindowsUpdateScan executa o scan + upload. Nunca panica: erros sao logados
// (o loop nao pode morrer por causa de uma maquina sem WUA).
func (a *App) runWindowsUpdateScan(ctx context.Context) {
	if !a.windowsUpdatesReady() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := a.InventorySvc.ScanAndSyncWindowsUpdates(ctx); err != nil {
		a.Logs.Append("[windows-update] scan periodico falhou: " + err.Error())
	}
}

// reportPendingReboot envia a sonda de reinicio pendente (somente em mudanca).
func (a *App) reportPendingReboot(ctx context.Context) {
	if a == nil || a.InventorySvc == nil {
		return
	}
	if !a.InventorySvc.WindowsUpdateSupported() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := a.InventorySvc.ReportPendingReboot(ctx); err != nil {
		a.Logs.Append("[windows-update] sonda de reinicio falhou: " + err.Error())
	}
}
