package app

// companion_rpc.go — bridge da UI companion para o core do serviço via IPC
// RPC (PLANO_SEPARACAO_SERVICO_UI.md, Fase C).
//
// Padrão de uso nos bridges: quando a App está em modo companion
// (ipcClient != nil), os métodos que consultam o DB local do core são
// roteados ao serviço via ipcRequest; no standalone, executam localmente
// (decisão D3 — DB único, dono = serviço; a UI companion nunca abre o SQLite
// do serviço).

import (
	"context"
	"encoding/json"
	"time"

	"discovery/app/core/database"
	"discovery/app/core/models"
)

// ipcRequest executa um RPC ao serviço quando em modo companion.
// ok=false quando não há IPC client (standalone) — o caller cai no caminho local.
func (a *App) ipcRequest(method string, payload map[string]any) (map[string]any, bool) {
	return a.ipcRequestTimeout(method, payload, 10*time.Second)
}

// ipcRequestTimeout é a variante de ipcRequest com timeout explícito — usado
// por RPCs que podem demorar mais que o padrão de 10s (ex.: fetch frio do
// catálogo da loja, que baixa várias páginas paginadas da API).
func (a *App) ipcRequestTimeout(method string, payload map[string]any, timeout time.Duration) (map[string]any, bool) {
	if a == nil || a.ipcClient == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(a.ctx, timeout)
	defer cancel()
	resp, err := a.ipcClient.Request(ctx, method, payload)
	if err != nil {
		a.Logs.Append("[ipc-rpc] " + method + ": " + err.Error())
		return nil, false
	}
	return resp, true
}

// ── Loja de apps (companion → serviço) ─────────────────────────────────────
// A UI companion não tem DB local (decisão D3) nem config de conexão própria
// confiável (cópia em memória do boot fica stale). O catálogo é resolvido NO
// serviço, que tem a config correta e o cache persistido da app-store
// (SQLite, 7 dias) — com API fora do ar a loja continua carregando.

// getStoreCatalogCompanion consulta o catálogo da loja via IPC quando
// companion. Retorna (zero, false) quando standalone ou falhou.
// Timeout de 30s: um fetch frio no serviço (cache vazio + várias páginas da
// API) pode passar do padrão de 10s.
func (a *App) getStoreCatalogCompanion() (models.Catalog, bool) {
	resp, ok := a.ipcRequestTimeout("store:catalog", nil, 30*time.Second)
	if !ok {
		return models.Catalog{}, false
	}
	data, _ := resp["data"].(map[string]any)
	raw, err := json.Marshal(data["catalog"])
	if err != nil {
		return models.Catalog{}, false
	}
	var catalog models.Catalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return models.Catalog{}, false
	}
	return catalog, true
}

// ipcRPCStoreCatalog devolve o catálogo da loja (lado do serviço).
func (a *App) ipcRPCStoreCatalog() map[string]any {
	if err := a.requireInventorySvc(); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	catalog, err := a.InventorySvc.GetCatalog()
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	raw, err := json.Marshal(catalog)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true, "data": map[string]any{"catalog": json.RawMessage(raw)}}
}

// ── Config de debug (companion ↔ serviço) ──────────────────────────────────
// O SetDebugConfig da UI roda na memória do processo da UI; o serviço (que
// roda o core) precisa receber a mesma config, senão fica divergente até
// reiniciar. debug:get devolve a config VIVA do serviço para a UI adotar.

// getDebugConfigCompanion consulta a config de debug do serviço via IPC.
// Retorna (zero, false) quando standalone ou falhou.
func (a *App) getDebugConfigCompanion() (DebugConfig, bool) {
	resp, ok := a.ipcRequest("debug:get", nil)
	if !ok {
		return DebugConfig{}, false
	}
	data, _ := resp["data"].(map[string]any)
	raw, err := json.Marshal(data["config"])
	if err != nil {
		return DebugConfig{}, false
	}
	var cfg DebugConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return DebugConfig{}, false
	}
	return cfg, true
}

// ipcRPCDebugGet devolve a config de debug VIVA do serviço.
func (a *App) ipcRPCDebugGet() map[string]any {
	if err := a.requireDebugSvc(); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	raw, err := json.Marshal(a.DebugSvc.GetConfig())
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true, "data": map[string]any{"config": json.RawMessage(raw)}}
}

// ipcRPCDebugSet aplica a config de debug recebida da UI companion NO serviço
// (valida, persiste em debug_config.json e recarrega o agentConn do core).
func (a *App) ipcRPCDebugSet(payload map[string]any) map[string]any {
	if err := a.requireDebugSvc(); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	raw, err := json.Marshal(payload["config"])
	if err != nil {
		return map[string]any{"ok": false, "error": "config inválida: " + err.Error()}
	}
	var cfg DebugConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return map[string]any{"ok": false, "error": "config inválida: " + err.Error()}
	}
	if err := a.DebugSvc.SetConfig(cfg); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true}
}

// GetLocalMemoriesCompanion consulta Memory Notes via IPC quando companion.
// Retorna (nil, false) quando standalone (usar caminho local).
func (a *App) getLocalMemoriesCompanion() ([]database.MemoryNote, bool) {
	resp, ok := a.ipcRequest("memory:list", nil)
	if !ok {
		return nil, false
	}
	data, _ := resp["data"].(map[string]any)
	raw, err := json.Marshal(data["notes"])
	if err != nil {
		return nil, false
	}
	var notes []database.MemoryNote
	if err := json.Unmarshal(raw, &notes); err != nil {
		return nil, false
	}
	return notes, true
}
