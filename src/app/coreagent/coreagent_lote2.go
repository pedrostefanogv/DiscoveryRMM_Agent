package coreagent

// coreagent_lote2.go — migração física lote 2 (PLANO_SEPARACAO_SERVICO_UI.md
// §0.8): tipos de domínio que viviam no package app e agora residem aqui.
//
// Estratégia: os tipos abaixo são wrappers/caches puros (sem dependência de
// *App nem de Wails). O App incorpora coreagent.CoreAgent e a promoção de
// campos mantém os acessos `a.logs`, `a.invCache`, `a.runtimeFlags`, etc.
// funcionando — refactor mecânico, zero mudança de comportamento.
//
// Ficaram no App (UI/bridge com dependência de *App): packageManagerRouter
// (depende de *App), mcpRegistry, chatSvc, psadtSvc, debugHTTP*, tray,
// closeMu/allowClose, zeroTouch, ipcServer/ipcClient, companionStatus.

import (
	"sync"
	"time"

	appstore "discovery/app/appstore"
	"discovery/app/core/models"
	"discovery/app/logs"
	appsupportmeta "discovery/app/supportmeta"
)

// ── RuntimeFlags (movido de app/types.go) ──────────────────────────────────

// RuntimeFlags controla o comportamento de execução (debug/serviço).
// Tags json preservadas do struct original do app (consumido pelo frontend
// via GetRuntimeFlags — ServiceMode não é exposto).
type RuntimeFlags struct {
	DebugMode      bool `json:"debugMode"`
	StartMinimized bool `json:"startMinimized"`
	ServiceMode    bool `json:"-"`
}

// ── LogBuffer (wrapper de logs.Buffer — movido de app/logging.go) ─────────

// LogBuffer é um wrapper sobre logs.Buffer que re-expõe os métodos com nomes
// minúsculos, mantendo compatibilidade com as chamadas existentes
// (a.logs.append, a.logs.getAll, etc.).
type LogBuffer struct {
	*logs.Buffer
}

func (l *LogBuffer) append(line string) {
	if l == nil || l.Buffer == nil {
		return
	}
	l.Buffer.Append(line)
}

// EnableFilePersistence expõe Buffer.EnableFilePersistence (cross-package).
func (l *LogBuffer) EnableFilePersistence(path string) error {
	if l == nil || l.Buffer == nil {
		return nil
	}
	return l.Buffer.EnableFilePersistence(path)
}

// Append / Subscribe / SnapshotAndSubscribe / GetAll / Count: métodos
// exportados usados cross-package (package app → coreagent). Os nomes
// minúsculos acima não são acessíveis de fora do pacote.
func (l *LogBuffer) Append(line string) {
	l.append(line)
}

func (l *LogBuffer) subscribe(fn func(string)) func() {
	if l == nil || l.Buffer == nil {
		return func() {}
	}
	return l.Buffer.Subscribe(fn)
}

func (l *LogBuffer) snapshotAndSubscribe(fn func(string)) func() {
	if l == nil || l.Buffer == nil {
		return func() {}
	}
	return l.Buffer.SnapshotAndSubscribe(fn)
}

func (l *LogBuffer) getAll() []string {
	if l == nil || l.Buffer == nil {
		return nil
	}
	return l.Buffer.GetAll()
}

// ── InventoryCache (movido de app/types.go) ────────────────────────────────

// InventoryCache manages thread-safe caching of the last inventory report.
type InventoryCache struct {
	mu     sync.RWMutex
	report models.InventoryReport
	loaded bool
}

func (c *InventoryCache) get() (models.InventoryReport, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.report, c.loaded
}

func (c *InventoryCache) set(r models.InventoryReport) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.report = r
	c.loaded = true
}

func (c *InventoryCache) has() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.loaded
}

// Has exporta has para uso cross-package (package app).
func (c *InventoryCache) Has() bool {
	return c.has()
}

// Reset limpa o cache (usado por clearMemoryCaches no package app).
func (c *InventoryCache) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loaded = false
	c.report = models.InventoryReport{}
}

func (c *InventoryCache) Get() (models.InventoryReport, bool) {
	return c.get()
}

func (c *InventoryCache) Set(r models.InventoryReport) {
	c.set(r)
}

// ── ExportConfig (movido de app/types.go) ──────────────────────────────────

// ExportConfig holds the current export options.
type ExportConfig struct {
	mu     sync.RWMutex
	redact bool
}

func (e *ExportConfig) get() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.redact
}

// Get exporta get para uso cross-package (package app).
func (e *ExportConfig) Get() bool {
	return e.get()
}

func (e *ExportConfig) set(v bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.redact = v
}

// Set exporta set para uso cross-package (package app).
func (e *ExportConfig) Set(v bool) {
	e.set(v)
}

// ── AgentInfoCache (wrapper de supportmeta.AgentInfoCache) ────────────────

// AgentInfoCache caches the agent identifiers resolved from /api/v1/agent-auth/me.
type AgentInfoCache struct {
	inner appsupportmeta.AgentInfoCache
}

func (c *AgentInfoCache) get() (appsupportmeta.AgentInfo, bool) {
	return c.inner.Get()
}

func (c *AgentInfoCache) set(info appsupportmeta.AgentInfo) {
	c.inner.Set(info)
}

func (c *AgentInfoCache) invalidate() {
	c.inner.Invalidate()
}

func (c *AgentInfoCache) Get() (appsupportmeta.AgentInfo, bool) {
	return c.inner.Get()
}

func (c *AgentInfoCache) Set(info appsupportmeta.AgentInfo) {
	c.inner.Set(info)
}

func (c *AgentInfoCache) Invalidate() {
	c.inner.Invalidate()
}

// ── AppStorePolicyCache (wrapper de appstore.PolicyCache) ─────────────────

// AppStorePolicyCache wraps the appstore policy cache.
type AppStorePolicyCache struct {
	inner appstore.PolicyCache
}

func (c *AppStorePolicyCache) get(maxAge time.Duration) (appstore.EffectivePolicy, bool) {
	return c.inner.Get(maxAge)
}

func (c *AppStorePolicyCache) set(policy appstore.EffectivePolicy) {
	c.inner.Set(policy)
}

func (c *AppStorePolicyCache) invalidate() {
	c.inner.Invalidate()
}

// Invalidate exporta invalidate para uso cross-package (package app).
func (c *AppStorePolicyCache) Invalidate() {
	c.invalidate()
}

// CachePointer expõe o ponteiro do cache interno (usado como *appstore.PolicyCache
// por consumers do package app, ex. automation.RuntimeConfig.Cache).
func (c *AppStorePolicyCache) CachePointer() *appstore.PolicyCache {
	return &c.inner
}

func (c *AppStorePolicyCache) Get(maxAge time.Duration) (appstore.EffectivePolicy, bool) {
	return c.inner.Get(maxAge)
}

func (c *AppStorePolicyCache) Set(policy appstore.EffectivePolicy) {
	c.inner.Set(policy)
}

// Nota (revisão 5): os structs agrupadores P2PState/StartupState/ActivityState/
// CoreAtoms/DeferredRestartState foram removidos — os campos correspondentes
// foram achatados diretamente em CoreAgent (coreagent.go), e
// deferredRestartState permanece no package app (power-command de UI).
