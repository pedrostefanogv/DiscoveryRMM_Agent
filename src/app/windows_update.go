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

// windowsUpdateAgentJitter devolve um deslocamento determinístico em
// [0, spread) derivado do agentId: mesmo agente, mesmo valor entre reinícios.
// Determinístico (e não aleatório) de propósito: reiniciar o agente não
// remaneja a frota, mas agentes provisionados pela mesma imagem (com agentIds
// diferentes) ficam espalhados.
func windowsUpdateAgentJitter(agentID string, spread time.Duration) time.Duration {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" || spread <= 0 {
		return 0
	}
	seconds := int64(spread / time.Second)
	if seconds <= 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(agentID))
	return time.Duration(int64(h.Sum32()%uint32(seconds))) * time.Second
}

// windowsUpdateInitialDelay espalha o primeiro scan da frota: 10 min + jitter
// determinístico de até 5 min derivado do agentId (mesmo agente, mesmo atraso
// entre reinícios — evita sincronia artificial).
func windowsUpdateInitialDelay(_ context.Context, a *App) time.Duration {
	agentID := ""
	if a != nil {
		agentID = strings.TrimSpace(a.GetDebugConfig().AgentID)
	}
	return windowsUpdateInitialDelayDefault + windowsUpdateAgentJitter(agentID, windowsUpdateJitterMax)
}

// windowsUpdateScanIntervalWithJitter aplica jitter determinístico de ±10% do
// intervalo em TODOS os ciclos seguintes do scan completo. Sem isso, os ciclos
// após o primeiro usavam time.After(interval) puro e a frota provisionada pela
// mesma imagem voltava a fazer rajada a cada 6h (COM + WMI + upload juntos).
func windowsUpdateScanIntervalWithJitter(agentID string, interval time.Duration) time.Duration {
	if interval <= 0 || strings.TrimSpace(agentID) == "" {
		return interval
	}
	spread := interval / 10
	jitter := windowsUpdateAgentJitter(agentID, spread) // [0, spread)
	// Converte [0, spread) em [-spread, +spread): ±10% em torno do intervalo.
	offset := 2*jitter - spread
	next := interval + offset
	// Nunca abaixo do intervalo minimo documentado (1h por padrão): com
	// -10% um intervalo configurado de 60 min viraria 54 min e furaria o
	// contrato do painel/configuração.
	if next < windowsUpdateScanIntervalMinimum {
		next = windowsUpdateScanIntervalMinimum
	}
	return next
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
		signature, stable := a.windowsUpdateSignaturesOfScan(ctx)
		a.InventorySvc.SetWindowsUpdateBaselineWithStable(signature, stable)
	}
}

// windowsUpdateSignaturesOfScan devolve a assinatura do estado atual (sonda
// barata) e a versao ESTAVEL (sem campos volateis de download) para servir de
// baseline depois de um scan completo.
func (a *App) windowsUpdateSignaturesOfScan(ctx context.Context) (string, string) {
	if a == nil || a.InventorySvc == nil {
		return "", ""
	}
	return a.InventorySvc.WindowsUpdateSignatures(ctx)
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
	// trigger="change": este é o caminho disparado pela mudança de assinatura
	// (o "periodic" é o loop do scan de 6h). Antes o envelope e o log diziam
	// "periodic" e o scanTrigger persistido pela API ficava sempre errado.
	probe := a.InventorySvc.PollWindowsUpdateChanges(ctx, windowsUpdateChangeScanMinGap, "change")
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
