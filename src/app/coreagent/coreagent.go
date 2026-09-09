package coreagent

// CoreAgent é a struct física do core do Discovery Agent
// (PLANO_SEPARACAO_SERVICO_UI.md, Fase A — migração física, lote 1).
//
// Estratégia incremental: o lote 1 move os campos cujos tipos vivem em
// pacotes externos ao package `app` (serviços de domínio e infra), sem
// ciclos de import. O App incorpora (`embed`) esta struct — a promoção de
// campos mantém todos os acessos `a.<campo>` existentes funcionando
// (refactor mecânico, zero mudança de comportamento).
//
// Lotes seguintes (pendentes): campos de tipos definidos no próprio package
// app (p2pCoordinator, logBuffer, caches, automationPackageManagerRouter,
// RuntimeFlags, agentInfoCache) — exigem mover os tipos primeiro para quebrar
// ciclos.
//
// Regra do pacote: nada aqui pode importar Wails (guardrail em
// coreagent_guardrail_test.go + TestCoreAgentNoWailsImports).

import (
	"sync"
	"sync/atomic"
	"time"

	"discovery/app/agentconfig"
	"discovery/app/apiclient"
	"discovery/app/appstore"
	"discovery/app/consolidation"
	"discovery/app/core/agentconn"
	"discovery/app/core/automation"
	"discovery/app/core/data"
	"discovery/app/core/database"
	"discovery/app/core/remotedebug"
	"discovery/app/core/remotesession"
	"discovery/app/core/selfupdate"
	"discovery/app/core/services"
	"discovery/app/customfields"
	"discovery/app/debug"
	appinventory "discovery/app/inventory"
	"discovery/app/p2p"
	"discovery/app/p2pmeta"
	"discovery/app/services/hardwareid"
	"discovery/app/services/memory"
	"discovery/app/services/notifications"
	appsupport "discovery/app/support"
	syncsvc "discovery/app/sync"
	"discovery/app/tickets"
	"discovery/app/updates"
)

// CoreAgent agrega os serviços de domínio do core. Embed no App.
type CoreAgent struct {
	// ── Infra básica ──
	DB            *database.DB
	CatalogSvc    *services.CatalogService
	CatalogClient *data.HTTPClient
	AppsSvc       *services.AppsService
	InvSvc        *services.InventoryService
	PrinterSvc    *services.PrinterService

	// ── Domínio ──
	AutomationSvc   *automation.Service
	SyncSvc         *syncsvc.Service
	AgentConn       *agentconn.Runtime
	DebugSvc        *debug.Service
	AgentConfigSvc  *agentconfig.Service
	TicketsSvc      *tickets.Service
	UpdatesSvc      *updates.Service
	Exporter        *updates.Exporter
	InventorySvc    *appinventory.Service
	SupportSvc      *appsupport.Service
	ConsolEngine    *consolidation.Engine
	HardwareIDSvc   *hardwareid.Service
	MemorySvc       *memory.Service
	NotificationSvc *notifications.Service
	ApiClientSvc    *apiclient.Service
	CustomFieldsSvc *customfields.Service
	AppStoreSvc     *appstore.Service
	SelfUpdater     *selfupdate.Updater
	SelfUpdaterCh   chan bool
	UpdateTrigger   chan struct{}

	// ── Sessões remotas (core; tray é atualizado pela UI via bridge) ──
	RemoteDebug      *remotedebug.Manager
	RemoteSessionMgr *remotesession.Manager

	// ── Configuração do agente (compartilhada core × UI) ──
	AgentConfigMu sync.RWMutex
	AgentConfig   agentconfig.AgentConfiguration

	// ── Força heartbeat / quit (core) ──
	QueuedForceHeartbeat atomic.Bool
	QuitRequested        atomic.Bool

	// ── Lote 2 (§0.8): estado de domínio que vivia no App ──
	Logs                       LogBuffer              // a.logs (247 usos via promoção)
	InvCache                   InventoryCache         // a.invCache
	ExportCfg                  ExportConfig           // a.exportCfg
	AgentInfo                  AgentInfoCache         // a.agentInfo
	AppStorePolicy             AppStorePolicyCache    // a.appStorePolicy
	P2PCoord                   *p2p.Coordinator       // a.p2pCoord (alias antigo p2pCoordinator)
	P2PMu                      sync.RWMutex           // a.p2pMu
	P2PConfig                  p2pmeta.Config         // a.p2pConfig (alias P2PConfig)
	P2PSeedPlanCache           p2pmeta.CachedSeedPlan // a.p2pSeedPlanCache
	P2PTelemetryRateLimitUntil time.Time              // a.p2pTelemetryRateLimitUntil
	StartupMu                  sync.RWMutex           // a.startupMu
	StartupErr                 error                  // a.startupErr
	StartupWg                  sync.WaitGroup         // a.startupWg
	ActivityMu                 sync.Mutex             // a.activityMu
	ActiveOps                  int                    // a.activeOps
	LastIdle                   bool                   // a.lastIdle
	IdleKnown                  bool                   // a.idleKnown
	IdleCapable                bool                   // a.idleCapable
	RuntimeFlags               RuntimeFlags           // a.runtimeFlags
	// Nota: deferredRestart fica no App (power-command de UI — revisão 5).
}
