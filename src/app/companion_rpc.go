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
	"strings"
	"time"

	"discovery/app/agentconfig"
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
func (a *App) getStoreCatalogCompanion(force bool) (models.Catalog, bool) {
	payload := map[string]any{}
	if force {
		payload["force"] = true
	}
	// Fetch frio forçado pode baixar várias páginas da API; 60s dá folga sem
	// travar a UI (que mostra o spinner de "atualizar catálogo").
	timeout := 30 * time.Second
	if force {
		timeout = 60 * time.Second
	}
	resp, ok := a.ipcRequestTimeout("store:catalog", payload, timeout)
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

// refreshStoreCatalogCompanion consulta o catálogo forçando fetch novo no serviço.
func (a *App) refreshStoreCatalogCompanion() (models.Catalog, bool) {
	return a.getStoreCatalogCompanion(true)
}

// ipcRPCStoreCatalog devolve o catálogo da loja (lado do serviço).
// payload["force"]=true força um fetch novo da API antes de montar o catálogo.
func (a *App) ipcRPCStoreCatalog(payload map[string]any) map[string]any {
	if err := a.requireInventorySvc(); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	force, _ := payload["force"].(bool)
	var catalog models.Catalog
	var err error
	if force {
		catalog, err = a.InventorySvc.RefreshCatalog()
	} else {
		catalog, err = a.InventorySvc.GetCatalog()
	}
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	raw, err := json.Marshal(catalog)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true, "data": map[string]any{"catalog": json.RawMessage(raw)}}
}

// ── Ações da loja (companion → serviço, execução como SYSTEM) ──────────────

// storePackageActionRPCTimeout é o teto de espera da UI por uma ação da loja
// executada no serviço. Instalações reais (download + MSI de centenas de MB)
// levam minutos — muito acima do timeout padrão de 10s dos RPCs de leitura.
const storePackageActionRPCTimeout = 30 * time.Minute

// packageActionViaService roteia install/uninstall/upgrade/upgrade_all da loja
// para o SERVIÇO (SYSTEM) via canal admin do IPC.
//
// Contrato de retorno:
//   - handled=false: não há serviço (UI sem IPC) ou o serviço é ANTIGO e não
//     conhece o método (método desconhecido) — o caller segue no caminho
//     local, preservando o comportamento anterior.
//   - handled=true:  o serviço respondeu (sucesso ou erro de domínio). NUNCA
//     repetir localmente nesse caso: a operação já foi executada/pedida lá.
//
// Falha de transporte (timeout, pipe caído, canal admin indisponível) também
// NÃO cai no fallback local: repetir às cegas poderia instalar duas vezes, e
// o caminho local é justamente o que exige UAC para usuário não-admin.
func (a *App) packageActionViaService(method, id string) (string, bool, error) {
	if a == nil || a.ipcClient == nil {
		return "", false, nil
	}
	// Sem serviço conectado (UI antiga standalone ou boot antes do primeiro
	// handshake): mantém o caminho local de sempre. Com serviço conectado a
	// ação NUNCA cai no local — lá o policy é aplicado e a execução é SYSTEM.
	if _, _, ok := a.ipcClient.ServiceHandshake(); !ok {
		return "", false, nil
	}
	payload := map[string]any{}
	if id = strings.TrimSpace(id); id != "" {
		payload["id"] = id
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, storePackageActionRPCTimeout)
	defer cancel()

	resp, err := a.ipcClient.Request(ctx, method, payload)
	out := ipcRPCDataOutput(resp)
	if err != nil {
		// Serviço ANTIGO (UI nova após update parcial): o método ainda não
		// existe. Fallback local é seguro porque nada foi executado lá.
		if strings.Contains(err.Error(), "método desconhecido") {
			return "", false, nil
		}
		// Qualquer outro erro (domínio OU transporte) é definitivo: o serviço
		// pode ter executado a operação e perdido a resposta; repetir local
		// duplicaria a instalação — e o caminho local é o que pede UAC.
		return out, true, err
	}
	return out, true, nil
}

// ipcRPCDataOutput extrai data.output de uma resposta RPC ("" quando ausente).
func ipcRPCDataOutput(resp map[string]any) string {
	if resp == nil {
		return ""
	}
	data, _ := resp["data"].(map[string]any)
	if data == nil {
		return ""
	}
	out, _ := data["output"].(string)
	return out
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

// hydrateAgentConfigurationCompanion adota a configuração do agente resolvida
// pelo serviço (RPC config:get) na cópia em memória da UI companion.
// Sem isso a UI tem AgentConfig zero e os fallbacks dependentes de
// clientId/siteId (ex.: support.AgentInfoFallback) nunca disparam offline.
// A cópia é apenas em memória — a UI não abre o DB (decisão D3) nem persiste.
func (a *App) hydrateAgentConfigurationCompanion() {
	if a == nil || a.ipcClient == nil {
		return
	}
	resp, ok := a.ipcRequest("config:get", nil)
	if !ok {
		return
	}
	data, _ := resp["data"].(map[string]any)
	raw, err := json.Marshal(data["configuration"])
	if err != nil {
		return
	}
	var cfg agentconfig.AgentConfiguration
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return
	}
	if strings.TrimSpace(cfg.ClientID) == "" && strings.TrimSpace(cfg.SiteID) == "" {
		// Serviço ainda sem config resolvida: não grava config vazia; tenta de
		// novo na próxima reconexão.
		return
	}
	a.AgentConfigMu.Lock()
	a.AgentConfig = cfg
	a.AgentConfigMu.Unlock()
	a.Logs.Append("[ipc] configuração do agente adotada do serviço (config:get)")
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
