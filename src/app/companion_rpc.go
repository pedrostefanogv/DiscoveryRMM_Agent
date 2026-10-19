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
	"errors"
	"fmt"
	"strings"
	"time"

	"discovery/app/agentconfig"
	"discovery/app/core/database"
	"discovery/app/core/models"
	"discovery/app/core/platform"
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
//
// 45 min cobre o PIOR caso do router: espera de readiness P2P (45s) + winget
// download (5 min) + execução direta do instalador (orçamento de 12 min para
// MSI / 6 min para exe) + fallback winget install (5 min) + verificações
// posteriores (2 × winget list / upgrade, 5 min cada). Se o cliente estourar
// este prazo o serviço CONTINUA instalando (o ctx do serviço é independente),
// mas a UI perde a resposta — por isso o teto é folgado.
const storePackageActionRPCTimeout = 45 * time.Minute

// isProcessElevated é injetável em teste (evita depender do token do runner).
var isProcessElevated = platform.IsElevated

// packageActionViaService roteia install/uninstall/upgrade/upgrade_all da loja
// para o SERVIÇO (SYSTEM) via canal admin do IPC.
//
// Contrato de retorno:
//   - handled=true:  o serviço respondeu (sucesso) OU a operação é definitiva
//     (erro de domínio / resposta perdida). O caller NUNCA repete localmente.
//   - handled=false: não há serviço a quem pedir (UI sem IPC, serviço fora,
//     serviço ANTIGO sem o RPC). O caller pode tentar o caminho local — mas
//     SOMENTE se o processo for elevado (ver packageActionLocalAllowed).
//
// A decisão não depende de ServiceHandshake: aquele valor só é preenchido no
// hello do canal admin e, se o primeiro handshake falhasse, o fallback local
// ficaria valendo para a sessão inteira (UAC de novo). Tentamos o RPC sempre;
// adminAuth reconecta o canal quando necessário.
func (a *App) packageActionViaService(method, id string) (string, bool, error) {
	if a == nil || a.ipcClient == nil {
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
	if err == nil {
		return out, true, nil
	}
	if storeActionFallbackAllowed(resp, err, isProcessElevated()) {
		a.Logs.Append("[ipc-rpc] " + method + ": fallback local (processo elevado) motivo=" + err.Error())
		return "", false, nil
	}
	a.Logs.Append("[ipc-rpc] " + method + ": " + err.Error())
	if storeRPCNotSupported(resp, err) {
		// Serviço antigo (método ainda não existia): sem fallback local porque
		// a UI não é elevada — mensagem clara em vez do erro de canal/leitura.
		return out, true, fmt.Errorf("o serviço DiscoveryAgent em execução não suporta a instalação pela loja (versão antiga); atualize o agente: %w", err)
	}
	if resp == nil && !errors.Is(err, errIPCAdminConnClosed) &&
		!errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) &&
		!strings.Contains(strings.ToLower(err.Error()), "timeout") {
		// Serviço ausente/pipe indisponível ANTES do envio: mensagem acionável
		// (sem admin o caminho local é proibido).
		return out, true, fmt.Errorf("%w (detalhe: %v)", errPackageActionNeedsService, err)
	}
	return out, true, err
}

// storeRPCNotSupported detecta serviço ANTIGO sem os RPCs da loja. Duas
// respostas são possíveis conforme a versão:
//   - "método desconhecido" (dispatch novo, tabela antiga);
//   - "ipc_wrong_channel" (versão em que store:install era classificado como
//     LEITURA e o canal admin recusava com "método de leitura não permitido").
func storeRPCNotSupported(resp map[string]any, err error) bool {
	if err == nil {
		return false
	}
	if resp != nil {
		if code, _ := resp["code"].(string); code == "ipc_wrong_channel" {
			return true
		}
	}
	return strings.Contains(err.Error(), "método desconhecido")
}

// storeActionFallbackAllowed decide se a UI pode executar a ação localmente
// depois de um erro do RPC. Regra de segurança: só processo ELEVADO (admin/
// SYSTEM) pode cair no caminho local — usuário comum nunca deve disparar
// instalador/UAC. Erros que podem significar "o serviço já executou" também
// são definitivos mesmo para admin, para não duplicar a instalação.
func storeActionFallbackAllowed(resp map[string]any, err error, elevated bool) bool {
	if err == nil {
		return false
	}
	if storeRPCNotSupported(resp, err) {
		return elevated
	}
	if resp != nil {
		return false // resposta de domínio (ok=false): definitiva
	}
	if errors.Is(err, errIPCAdminConnClosed) {
		return false // request em voo: o serviço pode ter executado
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	if strings.Contains(strings.ToLower(err.Error()), "timeout") {
		return false // idem: pode ter executado e a resposta se perdeu
	}
	return elevated // transporte sem envio (serviço ausente/pipe indisponível)
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
