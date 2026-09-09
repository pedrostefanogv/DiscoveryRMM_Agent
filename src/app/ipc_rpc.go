package app

// Métodos RPC atendidos pelo serviço via IPC request/response
// (PLANO_SEPARACAO_SERVICO_UI.md, Fase C). A UI companion consulta o core
// que roda no serviço — sem abrir o SQLite do serviço (decisão D3).

import (
	"context"
	"encoding/json"
)

// handleIPCRequest processa um request RPC da UI (lado do serviço).
// payload contém "method" + parâmetros. Retorna o payload de resposta
// (com "ok", e "data"/"error").
func (a *App) handleIPCRequest(ctx context.Context, payload map[string]any) map[string]any {
	if a == nil || payload == nil {
		return map[string]any{"ok": false, "error": "app indisponível"}
	}
	method, _ := payload["method"].(string)
	delete(payload, "method")
	switch method {
	case "status:pending_counts":
		return a.ipcRPCPendingCounts()
	case "config:get":
		return a.ipcRPCConfigGet()
	case "inventory:snapshot":
		return a.ipcRPCInventorySnapshot()
	case "memory:list":
		return a.ipcRPCMemoryList()
	case "memory:add":
		content, _ := payload["content"].(string)
		return a.ipcRPCMemoryAdd(content)
	case "memory:delete":
		id, _ := payload["id"].(float64)
		return a.ipcRPCMemoryDelete(int64(id))
	case "logs:tail":
		count, _ := payload["count"].(float64)
		return a.ipcRPCTailLogs(int(count))
	case "automation:state":
		return a.ipcRPCAutomationState()
	case "updates:scan":
		return a.ipcRPCUpdatesScan()
	default:
		return map[string]any{"ok": false, "error": "método desconhecido: " + method}
	}
}

// ipcRPCPendingCounts expõe os contadores de outbox (usados por GetStatusOverview).
func (a *App) ipcRPCPendingCounts() map[string]any {
	out := map[string]any{"ok": true, "data": map[string]any{}}
	if a.CoreAgent.DB == nil {
		return out
	}
	agentID := a.GetDebugConfig().AgentID
	if agentID == "" {
		return out
	}
	data := out["data"].(map[string]any)
	if n, err := a.CoreAgent.DB.CountPendingCommandResultOutbox(agentID); err == nil {
		data["pendingCommandResults"] = n
	}
	if n, err := a.CoreAgent.DB.CountPendingP2PTelemetryOutbox(agentID); err == nil {
		data["pendingP2PTelemetry"] = n
	}
	return out
}

// ipcRPCConfigGet devolve a configuração do agente (parseada do cache raw).
func (a *App) ipcRPCConfigGet() map[string]any {
	cfg := a.GetAgentConfiguration()
	return map[string]any{"ok": true, "data": map[string]any{"configuration": cfg}}
}

// ipcRPCInventorySnapshot devolve o último inventário em cache.
func (a *App) ipcRPCInventorySnapshot() map[string]any {
	if inv, ok := a.InvCache.Get(); ok {
		return map[string]any{"ok": true, "data": map[string]any{"inventory": inv}}
	}
	return map[string]any{"ok": true, "data": map[string]any{}}
}

// ipcRPCMemoryList lista Memory Notes (DB único — decisão D3).
func (a *App) ipcRPCMemoryList() map[string]any {
	if a.MemorySvc == nil {
		return map[string]any{"ok": false, "error": "memory service indisponível"}
	}
	notes, err := a.MemorySvc.GetLocalMemories()
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true, "data": map[string]any{"notes": notes}}
}

// ipcRPCMemoryAdd cria uma Memory Note.
func (a *App) ipcRPCMemoryAdd(content string) map[string]any {
	if a.MemorySvc == nil {
		return map[string]any{"ok": false, "error": "memory service indisponível"}
	}
	note, err := a.MemorySvc.AddLocalMemory(content)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true, "data": map[string]any{"note": note}}
}

// ipcRPCMemoryDelete remove uma Memory Note.
func (a *App) ipcRPCMemoryDelete(id int64) map[string]any {
	if a.MemorySvc == nil {
		return map[string]any{"ok": false, "error": "memory service indisponível"}
	}
	if err := a.MemorySvc.DeleteLocalMemory(id); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true}
}

// ipcRPCTailLogs devolve as últimas linhas de log do serviço
// (PLANO_SEPARACAO_SERVICO_UI.md §0.4 — RPCs pendentes). A UI companion
// visualiza os logs do core que roda no serviço sem acesso direto ao DB.
func (a *App) ipcRPCTailLogs(count int) map[string]any {
	if count <= 0 {
		count = 100
	}
	if count > 2000 {
		count = 2000
	}
	lines := a.Logs.GetAll()
	if len(lines) > count {
		lines = lines[len(lines)-count:]
	}
	return map[string]any{"ok": true, "data": map[string]any{"lines": lines}}
}

// ipcRPCAutomationState devolve o estado do motor de automação do serviço.
// Serializado como JSON bruto para não acoplar o contrato IPC ao tipo
// AutomationStateView da UI (frontend reidrata via JSON).
func (a *App) ipcRPCAutomationState() map[string]any {
	if a.AutomationSvc == nil {
		return map[string]any{"ok": false, "error": "automation service indisponível"}
	}
	view := a.GetAutomationState()
	raw, err := json.Marshal(view)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true, "data": map[string]any{"state": json.RawMessage(raw)}}
}

// ipcRPCUpdatesScan roda a checagem de atualizações (winget/choco) NO serviço
// (decisão D2 — winget/updates rodam no serviço; a UI só exibe). Operação
// potencialmente lenta: se o scan passar do IPCRequestTimeout (10s), o
// Request da UI expira — por isso o resultado também é emitido como evento
// "updates:list" para as UIs conectadas, garantindo entrega tardia.
func (a *App) ipcRPCUpdatesScan() map[string]any {
	if err := a.requireUpdatesSvc(); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	items, err := a.GetPendingUpdates()
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	// Entrega tardia (revisão 4 — bug B8): o evento cobre o caso do Request
	// da UI ter expirado enquanto o scan continuava no serviço.
	a.EmitEvent("updates:list", "updates", json.RawMessage(raw))
	return map[string]any{"ok": true, "data": map[string]any{"updates": json.RawMessage(raw)}}
}
