package app

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"time"

	"discovery/app/core/inventory"
)

// runPeriodicInventorySync collects full inventory and syncs to server.
// Logs failures but never panics — safe to call from a ticker goroutine.
func (a *App) runPeriodicInventorySync(ctx context.Context) {
	if a == nil {
		return
	}
	if !a.isInventoryProvisioned() {
		log.Printf("[inventory] coleta periodica ignorada: agente nao provisionado")
		return
	}

	done := a.beginActivity("inventario periodico")
	if done != nil {
		defer done()
	}

	a.ensureOsqueryInstalled()
	report, err := a.collectInventoryWithHeartbeat(ctx)
	if err != nil {
		log.Printf("[inventory] coleta periodica falhou: %v", err)
		return
	}
	a.InvCache.Set(report)

	if a.InventorySvc != nil {
		a.Logs.Append("[inventory] coleta periodica concluida; sincronizando com servidor")
		a.InventorySvc.SyncInventoryOnStartup(ctx, report)
	}

	// TriggerOnAgentCheckIn: dispara a cada ciclo de inventario completo (~6h).
	if a.AutomationSvc != nil {
		a.AutomationSvc.TriggerAgentCheckInTasks(ctx)
	}
}

func (a *App) isInventoryProvisioned() bool {
	if a == nil {
		return false
	}
	return a.getDebugConfigInternal().IsProvisioned()
}

// applyStartupThrottleConfig pushes the agent's throttle policy to the
// inventory subsystem, controlling whether osquery queries are paced
// to avoid CPU saturation on modest machines.
func (a *App) applyStartupThrottleConfig() {
	if a == nil {
		return
	}
	cfg := a.GetAgentConfiguration()
	inventory.SetThrottleConfig(cfg.StartupThrottleEnabled, cfg.StartupMaxCPUPercent)
}

// onPostBootstrapProvisioned espera o agent ficar provisionado (com credenciais
// de API) e então dispara reconciliação de recursos que dependem disso:
// inventário inicial, refresh de configuração, app-store e suporte.
// Resolve o problema do primeiro boot onde o inventário não rodava e os
// serviços não iniciavam corretamente até o próximo ciclo agendado.
func (a *App) onPostBootstrapProvisioned(ctx context.Context) error {
	if a == nil {
		return fmt.Errorf("app indisponivel")
	}

	// Aguarda até 60s pelo bootstrap. Se já estiver provisionado, não espera.
	deadline := time.Now().Add(60 * time.Second)
	for !a.isInventoryProvisioned() && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}

	if !a.isInventoryProvisioned() {
		a.Logs.Append("[startup] post-bootstrap: timeout aguardando provisionamento")
		return nil
	}

	a.Logs.Append("[startup] post-bootstrap: agente provisionado — reconciliando recursos")

	// 0. Estado de liberação do servidor. O agente acabou de receber as
	//    credenciais (ou reiniciou já provisionado) e pode estar aguardando
	//    aprovação do zero-touch: sem isto a UI só descobria no próximo ciclo
	//    de sync e, nesse meio-tempo, Loja/Suporte/KB exibiam o 403 cru do
	//    servidor (relato 2026-11-17). Best-effort: falha não bloqueia nada.
	a.probeServerAuthorization()

	// 1. osquery + inventário inicial (não executou no goroutine #2 porque
	//    ainda não estava provisionado).
	a.ensureOsqueryInstalled()
	if !a.InvCache.Has() {
		a.startupLogf("[startup] post-bootstrap: executando inventario inicial")
		report, err := a.collectInventoryWithHeartbeat(ctx)
		if err != nil {
			a.startupLogf("[startup] post-bootstrap: falha ao coletar inventario: %v", err)
		} else {
			a.InvCache.Set(report)
			if a.InventorySvc != nil {
				a.InventorySvc.SyncInventoryOnStartup(ctx, report)
			}
		}
	}

	// 2. Refresh da configuração do agent (clientId/siteId, políticas).
	if a.SyncSvc != nil {
		_ = a.refreshAgentConfiguration(ctx)
		a.SyncSvc.ReconcileFromManifest(ctx, "post-bootstrap")
	}

	// 3. Automação — carrega políticas iniciais.
	if a.AutomationSvc != nil {
		if _, err := a.AutomationSvc.RefreshPolicy(ctx, false); err != nil {
			a.Logs.Append("[startup] post-bootstrap: falha ao carregar politicas de automacao: " + err.Error())
		}
	}

	// 4. App-store, suporte e registro de tools MCP (deferred 30s — non-critical at startup).
	a.safeGo(func() {
		select {
		case <-a.ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
		if _, err := a.loadEffectiveAppStorePolicy(a.ctx, true); err != nil {
			a.Logs.Append("[startup] post-bootstrap: falha ao carregar app-store: " + err.Error())
		}
		if a.SupportSvc != nil && a.featureEnabled(a.GetAgentConfiguration().KnowledgeBaseEnabled) {
			if err := a.SupportSvc.RefreshKnowledgeBase(); err != nil {
				a.Logs.Append("[startup] post-bootstrap: falha ao atualizar knowledge base: " + err.Error())
			}
		}
		// Snapshot local de chamados: sem este prefetch o cache offline só
		// existia depois de o usuário abrir a aba Suporte online ao menos uma vez.
		if a.SupportSvc != nil && a.featureEnabled(a.GetAgentConfiguration().SupportEnabled) {
			if _, err := a.SupportSvc.GetSupportTickets(); err != nil {
				a.Logs.Append("[startup] post-bootstrap: falha ao atualizar snapshot local de chamados: " + err.Error())
			}
		}
		// Registra tools MCP do agent na API para o fluxo multi-round do chat
		if err := a.RegisterAgentToolsOnServer(); err != nil {
			a.Logs.Append("[startup] post-bootstrap: falha ao registrar agent tools: " + err.Error())
		}
	})

	a.Logs.Append("[startup] post-bootstrap: reconciliacao concluida")
	return nil
}

func (a *App) ensureOsqueryInstalled() {
	// O coletor nativo é agora a fonte primária de inventário no Windows.
	// Não é mais necessário instalar o osquery automaticamente. O osquery
	// permanece apenas como fallback opcional quando já está presente no
	// sistema (ver inventory.Provider).
	if runtime.GOOS != "windows" {
		return
	}
	if !a.isInventoryProvisioned() {
		return
	}
	if a.AppsSvc == nil {
		return
	}

	// Apenas registra o status sem instalar.
	status := inventory.GetOsqueryStatus()
	if status.Installed {
		a.startupLogf("[startup] osquery presente (fallback opcional): %s", status.Path)
	}
}
