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

	// Sonda de estado BARATA (registro/log/SCM, sem WUA). E ela que dispara o
	// scan quando algo muda no Windows Update, evitando esperar até 6h por
	// dados novos ("Baixando 0%" congelado, instalação pendente sem reboot).
	windowsUpdateStateIntervalDefault = 5 * time.Minute
	windowsUpdateStateIntervalMinimum = 30 * time.Second
	// Janela mínima entre dois scans disparados por mudança de assinatura: um
	// download em andamento muda a porcentagem a cada minuto e um scan WUA
	// custa ~15s nestas máquinas.
	windowsUpdateChangeScanMinGap = 2 * time.Minute
)

// windowsUpdateInitialDelay espalha o primeiro scan da frota: 10 min + jitter
// determinístico de até 5 min derivado do agentId (mesmo agente, mesmo atraso
// entre reinícios — evita sincronia artificial).
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

// windowsUpdateScanInterval resolve a cadência do scan COMPLETO a partir da
// config efetiva do agente (default 6h, mínimo 1h). O scan periódico é a rede
// de segurança; o caminho rápido é a sonda de estado.
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

// windowsUpdateStateInterval resolve a cadência da sonda de estado
// (config: windowsUpdateStateIntervalMinutes; default 5 min, mínimo 30s).
func (a *App) windowsUpdateStateInterval() time.Duration {
	if a == nil {
		return windowsUpdateStateIntervalDefault
	}
	interval := windowsUpdateStateIntervalDefault
	if cfg := a.GetAgentConfiguration(); cfg.WindowsUpdateStateIntervalMinutes != nil {
		if minutes := *cfg.WindowsUpdateStateIntervalMinutes; minutes > 0 {
			interval = time.Duration(minutes) * time.Minute
		}
	}
	if interval < windowsUpdateStateIntervalMinimum {
		interval = windowsUpdateStateIntervalMinimum
	}
	return interval
}

// windowsUpdatesReady informa se o módulo pode rodar agora.
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

// runWindowsUpdateScan executa o scan completo + upload (trigger=periodic).
// Nunca panica: erros são logados (o loop não pode morrer por causa de uma
// máquina sem WUA).
func (a *App) runWindowsUpdateScan(ctx context.Context) {
	if !a.windowsUpdatesReady() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	result, err := a.InventorySvc.ScanAndSyncWindowsUpdatesTriggered(ctx, "periodic")
	if err != nil {
		a.Logs.Append("[windows-update] scan periodico falhou (" + inventoryScanErrorCode(err) + "): " + err.Error())
	}
	// A referência da assinatura passa a ser o estado que acabou de ser
	// enviado: sem isso o próximo ciclo da sonda dispararia outro scan.
	if result != nil && !result.StartedAt.IsZero() {
		a.InventorySvc.SetWindowsUpdateBaseline(a.windowsUpdateSignatureOfScan(ctx))
	}
}

// windowsUpdateSignatureOfScan devolve a assinatura do estado atual (sonda
// barata) para servir de baseline depois de um scan completo.
func (a *App) windowsUpdateSignatureOfScan(ctx context.Context) string {
	if a == nil || a.InventorySvc == nil {
		return ""
	}
	return a.InventorySvc.WindowsUpdateStateSignature(ctx)
}

// runWindowsUpdateStateProbe roda UM ciclo da sonda barata. Quando a
// assinatura muda, o InventorySvc dispara scan + upload (trigger=change).
func (a *App) runWindowsUpdateStateProbe(ctx context.Context) {
	if !a.windowsUpdatesReady() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	probe := a.InventorySvc.PollWindowsUpdateChanges(ctx, windowsUpdateChangeScanMinGap, "periodic")
	if !probe.Changed {
		return
	}
	if probe.Scanned && probe.ScanErrorCode != "" {
		a.Logs.Append("[windows-update] scan sob demanda (" + probe.Trigger + ") terminou com " + probe.ScanErrorCode)
	}
}

// reportPendingReboot envia a sonda de reinício pendente (somente em mudança).
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
