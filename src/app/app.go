package app

import (
	"context"
	"fmt"
	"log"
	"os"
	"runtime"
	runtimeDebug "runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/energye/systray"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"discovery/app/appstore"
	"discovery/app/debug"
	appinventory "discovery/app/inventory"
	appsupport "discovery/app/support"
	"discovery/app/updates"
	"discovery/internal/agentconn"
	"discovery/internal/ai"
	"discovery/internal/automation"
	"discovery/internal/buildinfo"
	"discovery/internal/chocolatey"
	"discovery/internal/data"
	"discovery/internal/database"
	"discovery/internal/dto"
	"discovery/internal/inventory"
	"discovery/internal/mcp"
	"discovery/internal/models"
	"discovery/internal/platform"
	"discovery/internal/printer"
	"discovery/internal/processutil"
	"discovery/internal/service"
	"discovery/internal/services"
	"discovery/internal/winget"
)

// Version is the application version, set at build time via ldflags:
//
//	go build -ldflags "-X discovery/app.Version=1.2.3"
var Version = "dev"

// Application-level constants for timeouts, URLs and window dimensions.
const (
	catalogURL       = "https://raw.githubusercontent.com/pedrostefanogv/winget-package-explo/refs/heads/main/public/data/packages.json"
	catalogTimeout   = 10 * time.Minute
	wingetTimeout    = 5 * time.Minute
	inventoryTimeout = 45 * time.Second
	printerTimeout   = 30 * time.Second
	chatConfigFile   = "chat_config.json"

	// Temporarily disable efficiency mode until we revisit this behavior.
	efficiencyModeEnabled = false

	WindowWidth     = 1280
	WindowHeight    = 860
	WindowMinWidth  = 980
	WindowMinHeight = 700
)

// GetDataDir retorna o diretório de dados da aplicação (exportado para uso em outros pacotes).
// Delega para internal/platform.
func GetDataDir() string {
	return platform.DataDir()
}

type App struct {
	ctx           context.Context
	cancel        context.CancelFunc
	runtimeFlags  RuntimeFlags
	catalogSvc    *services.CatalogService
	catalogClient *data.HTTPClient
	appsSvc       *services.AppsService
	invSvc        *services.InventoryService
	printerSvc    *services.PrinterService

	db        *database.DB
	invCache  inventoryCache
	exportCfg exportConfig
	logs      logBuffer

	mcpRegistry    *mcp.Registry
	chatSvc        *ai.Service
	automationSvc  *automation.Service
	agentConn      *agentconn.Runtime
	remoteDebug    *remoteDebugManager
	syncCoord      *syncCoordinator
	p2pCoord       *p2pCoordinator
	updateTrigger  chan struct{}
	agentInfo      agentInfoCache
	appStorePolicy appStorePolicyCache
	debugSvc       *debug.Service
	updatesSvc     *updates.Service
	exporter       *updates.Exporter
	inventorySvc   *appinventory.Service
	supportSvc     *appsupport.Service
	serviceClient  *service.ServiceClient

	consolEngine *ConsolidationEngine

	p2pMu                      sync.RWMutex
	p2pConfig                  P2PConfig
	p2pSeedPlanCache           cachedP2PSeedPlan
	p2pTelemetryRateLimitUntil time.Time
	nonCriticalMu              sync.RWMutex
	nonCriticalBackoffUntil    time.Time
	nonCriticalBackoffReason   string
	lastGlobalPongAt           time.Time
	lastGlobalPongServerTime   string
	lastGlobalPongKnown        bool
	lastGlobalPongOverloaded   bool

	agentConfigMu sync.RWMutex
	agentConfig   AgentConfiguration

	startupMu                sync.RWMutex
	startupErr               error
	startupWg                sync.WaitGroup
	activityMu               sync.Mutex
	activeOps                int
	lastIdle                 bool
	idleKnown                bool
	idleCapable              bool
	closeMu                  sync.RWMutex
	allowClose               bool
	trayReady                atomic.Bool
	trayIconState            atomic.Int32
	trayIcon                 []byte
	trayProvisioning         []byte
	trayOffline              []byte
	meshEnsureRunning        atomic.Bool
	zeroTouchAttemptInFlight atomic.Bool
	zeroTouchApprovalPending atomic.Bool

	// serviceConnectedMode é true quando o Windows Service foi detectado no startup.
	// Quando ativo, workers locais de automação e inventário são omitidos para
	// evitar duplicação com os workers do service (arquitetura service-first).
	serviceConnectedMode atomic.Bool

	// startupTime registra quando a aplicação iniciou, usado para calcular
	// uptimeSeconds nos heartbeats.
	startupTime time.Time

	notificationMu      sync.Mutex
	pendingNotifyResult map[string]chan string
	notificationByKey   map[string]string

	queuedForceHeartbeat atomic.Bool
}

func NewApp(opts AppStartupOptions) *App {
	catalogClient := data.NewHTTPClient(catalogURL, catalogTimeout)
	wingetClient := winget.NewClient(wingetTimeout)
	chocolateyClient := chocolatey.NewClient(wingetTimeout)
	inventoryProvider := inventory.NewProvider(inventoryTimeout)
	printerManager := printer.NewManager(printerTimeout)

	reg := mcp.NewRegistry()
	chatSvc := ai.NewService(reg)

	// Initialize service client for communicating with Windows Service
	serviceClient := service.NewServiceClient()

	a := &App{
		ctx:                 context.Background(), // inicializado para evitar nil; sobrescrito por SetContext()
		runtimeFlags:        RuntimeFlags{DebugMode: opts.DebugMode},
		trayIcon:            opts.TrayIcon,
		trayProvisioning:    opts.TrayProvisioningIcon,
		trayOffline:         opts.TrayOfflineIcon,
		updateTrigger:       make(chan struct{}, 1),
		catalogSvc:          services.NewCatalogService(catalogClient),
		catalogClient:       catalogClient,
		appsSvc:             services.NewAppsService(wingetClient, chocolateyClient),
		invSvc:              services.NewInventoryService(inventoryProvider),
		printerSvc:          services.NewPrinterService(printerManager),
		mcpRegistry:         reg,
		chatSvc:             chatSvc,
		serviceClient:       serviceClient,
		pendingNotifyResult: make(map[string]chan string),
		notificationByKey:   make(map[string]string),
		startupTime:         time.Now(),
	}
	a.automationSvc = automation.NewService(func() automation.RuntimeConfig {
		cfg := a.GetDebugConfig()
		baseURL := strings.TrimSpace(cfg.ApiScheme) + "://" + strings.TrimSpace(cfg.ApiServer)
		if strings.TrimSpace(cfg.ApiScheme) == "" || strings.TrimSpace(cfg.ApiServer) == "" {
			baseURL = ""
		}
		return automation.RuntimeConfig{
			BaseURL: baseURL,
			Token:   strings.TrimSpace(cfg.AuthToken),
			AgentID: strings.TrimSpace(cfg.AgentID),
		}
	}, func(line string) {
		a.logs.append("[automation] " + line)
	})
	a.automationSvc.SetPackageManager(newAutomationPackageManagerRouter(a, a.appsSvc))
	a.automationSvc.SetPackageAuthorization(func(ctx context.Context, installationType automation.AppInstallationType, packageID, operation string) error {
		return a.authorizeAutomationPackage(ctx, string(installationType), packageID, operation)
	})
	a.automationSvc.SetPSADTPolicyResolver(func() automation.PSADTPolicy {
		cfg := a.GetAgentConfiguration().PSADT
		policy := automation.PSADTPolicy{
			RequiredVersion:       strings.TrimSpace(cfg.RequiredVersion),
			SuccessExitCodes:      append([]int(nil), cfg.SuccessExitCodes...),
			RebootExitCodes:       append([]int(nil), cfg.RebootExitCodes...),
			IgnoreExitCodes:       append([]int(nil), cfg.IgnoreExitCodes...),
			FallbackPolicy:        strings.TrimSpace(cfg.FallbackPolicy),
			TimeoutAction:         strings.TrimSpace(cfg.TimeoutAction),
			UnknownExitCodePolicy: strings.TrimSpace(cfg.UnknownExitCodePolicy),
		}
		if cfg.ExecutionTimeoutSeconds != nil {
			policy.ExecutionTimeoutSeconds = *cfg.ExecutionTimeoutSeconds
		}
		return policy
	})
	a.automationSvc.SetNotificationDispatcher(func(req automation.AutomationNotificationRequest) automation.AutomationNotificationResponse {
		resp := a.DispatchNotification(NotificationDispatchRequest{
			NotificationID: req.NotificationID,
			IdempotencyKey: req.IdempotencyKey,
			Title:          req.Title,
			Message:        req.Message,
			Mode:           req.Mode,
			Severity:       req.Severity,
			EventType:      req.EventType,
			Layout:         req.Layout,
			TimeoutSeconds: req.TimeoutSeconds,
			Metadata:       req.Metadata,
		})
		if !resp.Accepted {
			a.logs.append("[automation] notificacao nao aceita: " + strings.TrimSpace(resp.AgentAction))
		}
		return automation.AutomationNotificationResponse{
			Accepted:    resp.Accepted,
			Result:      resp.Result,
			AgentAction: resp.AgentAction,
			Message:     resp.Message,
		}
	})
	a.remoteDebug = newRemoteDebugManager(a.logs.append, a.GetDebugConfig, a.logs.subscribe)
	inventoryProvider.SetProgressCallback(func() {
		a.pulseInventoryHeartbeat()
	})
	a.agentConn = agentconn.NewRuntime(agentconn.Options{
		LoadConfig: func() agentconn.Config {
			cfg := a.GetDebugConfig()
			agentCfg := a.GetAgentConfiguration()
			return agentconn.Config{
				ApiScheme:                cfg.ApiScheme,
				ApiServer:                cfg.ApiServer,
				NatsServer:               cfg.NatsServer,
				NatsWsServer:             cfg.NatsWsServer,
				NatsServerHost:           cfg.NatsServerHost,
				NatsUseWssExternal:       cfg.NatsUseWssExternal,
				EnforceTLSHashValidation: cfg.EnforceTlsHashValidation,
				HandshakeEnabled:         cfg.HandshakeEnabled,
				ApiTLSCertHash:           cfg.ApiTlsCertHash,
				NatsTLSCertHash:          cfg.NatsTlsCertHash,
				AuthToken:                cfg.AuthToken,
				AgentID:                  cfg.AgentID,
				AgentVersion:             buildinfo.Version,
				ClientID:                 agentCfg.ClientID,
				SiteID:                   agentCfg.SiteID,
				HeartbeatInterval:        heartbeatIntervalFromAgentConfig(agentCfg),
			}
		},
		Logf: func(format string, args ...any) {
			a.logs.append("[agent] " + fmt.Sprintf(format, args...))
		},
		OnSyncPing: func(ping agentconn.SyncPing) {
			if a.syncCoord != nil {
				a.syncCoord.HandlePing(ping)
			}
		},
		OnGlobalPong:                  a.handleGlobalPong,
		GetHeartbeatMetrics:           a.getHeartbeatMetrics,
		OnP2PDiscoverySnapshot:        a.handleP2PDiscoverySnapshot,
		HandleCommand:                 a.handleAgentRuntimeCommand,
		OnCommandOutput:               a.onAgentCommandOutput,
		EnqueueCommandResultOutbox:    a.enqueueCommandResultOutbox,
		ListDueCommandResultOutbox:    a.listDueCommandResultOutbox,
		MarkSentCommandResultOutbox:   a.markSentCommandResultOutbox,
		RescheduleCommandResultOutbox: a.rescheduleCommandResultOutbox,
	})
	a.debugSvc = debug.NewService(debug.Options{
		Logf: func(line string) {
			a.logs.append(line)
		},
		AgentConn:          a.agentConn,
		AgentInfo:          &a.agentInfo,
		DB:                 a.db,
		NormalizeP2PConfig: normalizeP2PConfig,
		ApplyP2PConfig:     a.applyP2PConfig,
		DefaultP2PConfig:   defaultP2PConfig,
		Version:            Version,
	})
	a.syncCoord = newSyncCoordinator(a, a.updateTrigger)
	a.p2pConfig = defaultP2PConfig()
	a.p2pCoord = newP2PCoordinator(a)
	a.chatSvc.SetLogger(func(line string) {
		a.logs.append("[chat] " + line)
	})
	a.inventorySvc = appinventory.NewService(appinventory.Options{
		Apps:           a.appsSvc,
		Inventory:      a.invSvc,
		Cache:          &a.invCache,
		ResolveAllowed: a.resolveAllowedPackage,
		ResolveAllowedByType: func(ctx context.Context, installationType, packageID string) (appstore.Item, error) {
			return a.findAllowedPackage(ctx, installationType, packageID)
		},
		GetCatalog:    a.getCatalogFromAppStore,
		BeginActivity: a.beginActivity,
		DispatchNotification: func(req appinventory.InventoryNotification) appinventory.InventoryNotificationResponse {
			resp := a.DispatchNotification(NotificationDispatchRequest{
				NotificationID: req.NotificationID,
				IdempotencyKey: req.IdempotencyKey,
				Title:          req.Title,
				Message:        req.Message,
				Mode:           req.Mode,
				Severity:       req.Severity,
				EventType:      req.EventType,
				Layout:         req.Layout,
				TimeoutSeconds: req.TimeoutSeconds,
				Metadata:       req.Metadata,
			})
			return appinventory.InventoryNotificationResponse{
				Accepted:    resp.Accepted,
				Result:      resp.Result,
				AgentAction: resp.AgentAction,
				Message:     resp.Message,
			}
		},
		Logf: a.logs.append,
		Ctx: func() context.Context {
			return a.ctx
		},
		DB:                       a.db,
		DebugConfig:              a.GetDebugConfig,
		Version:                  Version,
		ResolveMeshCentralNodeID: a.getMeshCentralNodeIDForReport,
		OnHardwareReportSuccess:  a.markMeshCentralReportSuccess,
		ShouldDeferNonCritical:   a.nonCriticalBackoffWindow,
	})
	a.supportSvc = appsupport.NewService(appsupport.Options{
		Logf:        a.logs.append,
		Ctx:         func() context.Context { return a.ctx },
		DB:          a.db,
		AgentInfo:   &a.agentInfo,
		DebugConfig: a.GetDebugConfig,
		FeatureEnabled: func(flag *bool) bool {
			return a.featureEnabled(flag)
		},
		SupportEnabled: func() *bool {
			cfg := a.GetAgentConfiguration()
			return cfg.SupportEnabled
		},
		KnowledgeEnabled: func() *bool {
			cfg := a.GetAgentConfiguration()
			return cfg.KnowledgeBaseEnabled
		},
	})
	a.updatesSvc = updates.NewService(updates.Options{
		Apps:          a.appsSvc,
		BeginActivity: a.beginActivity,
		Logf:          a.logs.append,
		Ctx: func() context.Context {
			return a.ctx
		},
	})
	a.exporter = updates.NewExporter(updates.ExportOptions{
		BeginActivity: a.beginActivity,
		Inventory: func() (models.InventoryReport, error) {
			return a.getInventoryForExport()
		},
		GetRedact: a.getRedact,
		SetRedact: a.exportCfg.set,
	})
	if logPath := platform.LogFilePath(); logPath != "" {
		if err := a.logs.enableFilePersistence(logPath); err != nil {
			log.Printf("[startup] aviso: falha ao habilitar persistencia de logs em arquivo: %v", err)
		} else {
			a.logs.append("[startup] persistencia de logs habilitada em " + logPath)
		}
	}
	a.loadPersistedChatConfig()
	a.debugSvc.LoadConnectionConfigFromProduction()

	// Register all Discovery tools in the MCP registry.
	mcp.RegisterDiscoveryTools(reg, a)

	a.queuedForceHeartbeat.Store(false)

	if opts.DebugMode {
		a.logs.append("[startup] modo debug ativo por tecla de atalho (execucao atual)")
	}

	// Garantir defaults de configuração PSADT antes da primeira resposta da API.
	normalizePSADTConfigDefaults(&a.agentConfig.PSADT)
	normalizeRolloutDefaults(&a.agentConfig.Rollout)

	return a
}

// GetRuntimeFlags returns runtime-only startup flags for the current execution.
func (a *App) GetRuntimeFlags() RuntimeFlags {
	return a.runtimeFlags
}

// SetContext sets the application context and cancel func from an external caller
// (e.g. the MCP server mode in main.go that doesn't go through Wails startup).
func (a *App) SetContext(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	a.ctx = ctx
	a.cancel = cancel
}

// Ctx returns the current Wails context (may be nil before startup).
// Used by the root package for secondary-instance window focus.
func (a *App) Ctx() context.Context { return a.ctx }

// ClearMemoryCaches clears in-memory caches (inventory, etc).
// Exposed so main.go can call it from the OnBeforeClose Wails hook.
func (a *App) ClearMemoryCaches() { a.clearMemoryCaches() }

// AppStartup returns the Wails OnStartup callback for the given App.
// Using a package-level function avoids exposing startup as a bound Wails method.
func AppStartup(a *App) func(context.Context) { return a.startup }

// AppShutdown returns the Wails OnShutdown callback for the given App.
func AppShutdown(a *App) func(context.Context) { return a.shutdown }

// GetAgentConfiguration returns the last-known configuration retrieved from the server.
func (a *App) GetAgentConfiguration() AgentConfiguration {
	a.agentConfigMu.RLock()
	cfg := a.agentConfig
	a.agentConfigMu.RUnlock()
	return cfg
}

func (a *App) isZeroTouchApprovalPending() bool {
	if a == nil {
		return false
	}
	return a.zeroTouchApprovalPending.Load()
}

func (a *App) setZeroTouchApprovalPending(pending bool) bool {
	if a == nil {
		return false
	}
	previous := a.zeroTouchApprovalPending.Load()
	if previous == pending {
		return false
	}
	a.zeroTouchApprovalPending.Store(pending)
	return true
}

func (a *App) featureEnabled(flag *bool) bool {
	if flag == nil {
		return true
	}
	return *flag
}

const debugForcedHeartbeatIntervalSeconds = 10

// heartbeatIntervalFromAgentConfig retorna, temporariamente, um intervalo fixo
// de 10 segundos para facilitar debug de eventos de heartbeat.
// TODO: restaurar comportamento padrão (config remota / fallback de runtime) após o debug.
func heartbeatIntervalFromAgentConfig(_ AgentConfiguration) int {
	return debugForcedHeartbeatIntervalSeconds
}

// getHeartbeatMetrics coleta métricas do sistema para incluir no heartbeat
// padronizado (HeartbeatV2 / NATS). Usa uma única query osquery otimizada
// para coletar CPU, memória, disco, hostname, uptime e processos.
//
// Se o osquery não estiver disponível ou falhar, retorna métricas básicas
// (hostname + uptime) sem os campos percentuais — o payload JSON os omitirá.
func (a *App) getHeartbeatMetrics() agentconn.AgentHeartbeatMetrics {
	hostname, _ := os.Hostname()
	metrics := agentconn.AgentHeartbeatMetrics{
		Hostname:      hostname,
		UptimeSeconds: int64(time.Since(a.startupTime).Seconds()),
		P2pPeers:      a.getKnownP2PPeers(),
	}

	// Tenta coleta completa via osquery (CPU, memória, disco, processos).
	if runtime.GOOS == "windows" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if m := inventory.CollectHeartbeatMetrics(ctx); m != nil {
			metrics = *m
			// Sobrescreve P2P com o contador local que usa lock correto.
			metrics.P2pPeers = a.getKnownP2PPeers()
		}
	}

	return metrics
}

// getKnownP2PPeers retorna o número de peers P2P conhecidos, ou 0 se o
// coordenador P2P não estiver inicializado. Usa GetPeers() que já adquire
// o RLock internamente, garantindo thread-safety.
func (a *App) getKnownP2PPeers() int {
	if a.p2pCoord == nil {
		return 0
	}
	return len(a.p2pCoord.GetPeers())
}

func (a *App) shouldRunLocalP2P() bool {
	if a == nil {
		return false
	}
	if a.serviceConnectedMode.Load() && !a.runtimeFlags.DebugMode {
		return false
	}
	return true
}

func (a *App) startup(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	a.ctx = ctx
	a.cancel = cancel

	a.safeGo(func() { a.StartP2PTelemetryLoop(ctx) })

	a.startTray()
	if a.runtimeFlags.StartMinimized {
		a.hideWindowOnStartup()
	}
	a.applyIdleMode(true)

	// Inicializar database SQLite
	dataDir := GetDataDir()
	db, err := database.Open(dataDir)
	if err != nil {
		log.Printf("[startup] AVISO: falha ao abrir database: %v", err)
	} else {
		a.db = db
		log.Printf("[startup] database SQLite inicializado em %s", dataDir)

		// Configurar cache persistente no catalogClient
		if a.catalogClient != nil {
			a.catalogClient.SetDatabase(db)
		}
		if a.automationSvc != nil {
			a.automationSvc.SetDB(db)
		}
		// Inicializar ConsolidationEngine (feature-flagged, desabilitado por padrão)
		agentIDForEngine := strings.TrimSpace(a.GetDebugConfig().AgentID)
		a.consolEngine = newConsolidationEngine(db, agentIDForEngine)
	}

	// Verificar conectividade com o Windows Service antes de iniciar workers locais.
	// Quando o service está disponível, workers de automação e inventário são omitidos
	// para evitar duplicação (arquitetura service-first: service executa, UI consome).
	if a.serviceClient != nil {
		probeCtx, probeCancel := context.WithTimeout(ctx, 2*time.Second)
		if a.serviceClient.Ping(probeCtx) {
			a.serviceConnectedMode.Store(true)
			log.Println("[startup] Windows Service detectado — modo cliente IPC ativo; workers locais de automação e inventário não iniciados")
		} else {
			log.Println("[startup] Windows Service não detectado — modo autônomo local; todos os workers locais iniciados")
		}
		probeCancel()
	}

	a.startupWg.Add(1)
	a.safeGo(func() {
		defer a.startupWg.Done()

		// Quando o service está disponível, ele já gerencia inventário; pular coleta local.
		if a.serviceConnectedMode.Load() {
			log.Println("[startup] inventory-startup: ignorado (service disponível)")
			return
		}

		if !a.isInventoryProvisioned() {
			log.Println("[startup] inventory-startup: ignorado (agente nao provisionado)")
			return
		}

		done := a.beginActivity("inventario inicial")
		defer done()

		a.ensureOsqueryInstalled(ctx)

		report, err := a.collectInventoryWithHeartbeat(ctx)
		if err != nil {
			log.Printf("[startup] falha ao coletar inventario em background: %v", err)
			a.startupMu.Lock()
			a.startupErr = err
			a.startupMu.Unlock()
			return
		}
		a.invCache.set(report)
		if a.inventorySvc != nil {
			a.inventorySvc.SyncInventoryOnStartup(ctx, report)
		}
	})

	a.startupWg.Add(1)
	a.safeGo(func() {
		defer a.startupWg.Done()

		// Bootstrap pós-instalação: se houver URL/KEY do instalador, resolver token/agentId.
		if a.debugSvc != nil {
			a.debugSvc.BootstrapAgentCredentialsFromInstallerConfig(ctx)
		}
		if a.serviceConnectedMode.Load() {
			a.requestServiceConfigReload(ctx, "startup-bootstrap")
		}

		// MeshCentral deve iniciar somente apos autenticar e carregar credenciais do agente.
		a.safeGo(func() {
			a.ensureMeshCentralInstalled(ctx, "startup-auth", false)
		})

		if a.serviceConnectedMode.Load() {
			log.Println("[startup] agent-runtime local: ignorado (service disponível)")
			return
		}

		a.agentConn.Run(ctx)
	})

	a.startupWg.Add(1)
	a.safeGo(func() {
		defer a.startupWg.Done()
		a.drainAgentDecommissionOutbox(ctx, "startup")
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.drainAgentDecommissionOutbox(ctx, "periodic")
			}
		}
	})

	a.startupWg.Add(1)
	a.safeGo(func() {
		defer a.startupWg.Done()
		if a.automationSvc == nil {
			return
		}

		// Quando o service está disponível, ele gerencia automação; pular worker local.
		if a.serviceConnectedMode.Load() {
			log.Println("[startup] automation-service: ignorado (service disponível)")
			return
		}

		a.automationSvc.Run(ctx, func() {})
	})

	a.startupWg.Add(1)
	a.safeGo(func() {
		defer a.startupWg.Done()
		if a.syncCoord == nil {
			return
		}
		a.syncCoord.Run(ctx)
	})

	a.startupWg.Add(1)
	a.safeGo(func() {
		defer a.startupWg.Done()
		if a.p2pCoord == nil {
			return
		}
		if !a.shouldRunLocalP2P() {
			log.Println("[startup] p2p local: ignorado (service disponível)")
			return
		}
		if a.serviceConnectedMode.Load() && a.runtimeFlags.DebugMode {
			log.Println("[startup] p2p local: iniciado em modo debug mesmo com service disponível")
		}
		if !isAgentConfigured() && a.zeroTouchConfigRegistrationAllowed() {
			a.safeGo(func() {
				a.RunOnboardingLoop(ctx)
			})
		}
		a.p2pCoord.Run(ctx)
	})

	a.startupWg.Add(1)
	a.safeGo(func() {
		defer a.startupWg.Done()
		const cleanupInterval = 6 * time.Hour
		const cleanupBatchSize = 500
		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if a.db == nil {
					continue
				}
				n1, err1 := a.db.CleanupExpiredCommandResultOutbox(time.Now(), cleanupBatchSize)
				n2, err2 := a.db.CleanupExpiredP2PTelemetryOutbox(time.Now(), cleanupBatchSize)
				if err1 != nil {
					log.Printf("[outbox][cleanup] erro command_result: %v", err1)
				}
				if err2 != nil {
					log.Printf("[outbox][cleanup] erro p2p_telemetry: %v", err2)
				}
				if n1 > 0 || n2 > 0 {
					log.Printf("[outbox][cleanup] expirados removidos: command_result=%d p2p_telemetry=%d", n1, n2)
				}
			}
		}
	})
}

// SendTestHeartbeat triggers an immediate heartbeat send on the active
// NATS connection and returns diagnostic info.
// This is exposed as a Wails binding for the debug page.
func (a *App) SendTestHeartbeat() string {
	if !a.queuedForceHeartbeat.CompareAndSwap(false, true) {
		return "erro: heartbeat manual ja em andamento"
	}
	defer a.queuedForceHeartbeat.Store(false)

	a.logs.append("[heartbeat][manual] enviando heartbeat manual...")
	if a.serviceConnectedMode.Load() && a.serviceClient != nil {
		message, err := a.requestServiceForceHeartbeat(a.ctx, "debug-manual-heartbeat")
		if err != nil {
			a.logs.append("[heartbeat][manual] falha ao enviar heartbeat manual via service: " + err.Error())
			return "falha ao enviar heartbeat manual via service: " + err.Error()
		}
		if strings.TrimSpace(message) == "" {
			message = "heartbeat manual enviado com sucesso via Windows Service"
		}
		a.logs.append("[heartbeat][manual] " + message)
		return message
	}
	if a.agentConn == nil {
		a.logs.append("[heartbeat][manual] falha ao enviar heartbeat manual: agent runtime nao inicializado")
		return "erro: agent runtime nao inicializado"
	}
	if a.agentConn.ForceHeartbeat() {
		a.logs.append("[heartbeat][manual] heartbeat manual enviado com sucesso")
		return "heartbeat manual enviado com sucesso"
	}
	a.logs.append("[heartbeat][manual] falha ao enviar heartbeat manual: timeout ou nenhuma conexao ativa")
	return "falha ao enviar heartbeat manual: timeout ou nenhuma conexao ativa"
}

func (a *App) startupLogf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	log.Print(line)
	a.logs.append(line)
}

// safeGo executa fn em uma goroutine com recovery de panic.
// Se a goroutine panica, o stack trace é logado e o app não é derrubado.
// Substitui o watchdog.SafeGoWithContext removido no refactor.
func (a *App) safeGo(fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				stack := string(runtimeDebug.Stack())
				log.Printf("[PANIC] goroutine panicou: %v\n%s", r, stack)
				if a != nil {
					a.logs.append(fmt.Sprintf("[PANIC] goroutine panicou: %v", r))
					a.logs.append("[PANIC] stack trace:")
					a.logs.append(stack)
				}
			}
		}()
		fn()
	}()
}

func (a *App) isInventoryProvisioned() bool {
	if a == nil {
		return false
	}
	return a.GetDebugConfig().IsProvisioned()
}

func (a *App) ensureOsqueryInstalled(ctx context.Context) {
	if runtime.GOOS != "windows" {
		return
	}
	if !a.isInventoryProvisioned() {
		a.startupLogf("[startup] osquery: verificacao ignorada (agente nao provisionado)")
		return
	}
	if a.appsSvc == nil {
		a.startupLogf("[startup] aviso: apps service indisponivel; nao foi possivel verificar osquery")
		return
	}

	status := inventory.GetOsqueryStatus()
	if status.Installed {
		return
	}

	packageID := strings.TrimSpace(status.SuggestedPackageID)
	if packageID == "" {
		packageID = "osquery.osquery"
	}

	a.startupLogf("[startup] osquery ausente; instalando via winget (%s)", packageID)
	out, err := a.appsSvc.Install(ctx, packageID)
	if out != "" {
		a.startupLogf("[startup] winget install output: %s", out)
	}
	if err != nil {
		a.startupLogf("[startup] aviso: falha ao instalar osquery via winget: %v", err)
		return
	}

	inventory.InvalidateOsqueryBinaryCache()
	a.startupLogf("[startup] osquery instalado com sucesso")
}

// hideWindowOnStartup keeps the app running in tray when launched by Windows startup.
func (a *App) hideWindowOnStartup() {
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()

		timeout := time.NewTimer(12 * time.Second)
		defer timeout.Stop()

		for {
			select {
			case <-a.ctx.Done():
				return
			case <-timeout.C:
				log.Println("[startup] aviso: timeout aguardando tray para iniciar minimizado")
				return
			case <-ticker.C:
				if !a.IsTrayReady() {
					continue
				}
				wailsRuntime.WindowMinimise(a.ctx)
				wailsRuntime.WindowHide(a.ctx)
				log.Println("[startup] janela iniciada minimizada no tray")
				return
			}
		}
	}()
}

// shutdown is called when the application is closing; it cancels background
// work and waits for goroutines to finish.
func (a *App) shutdown(ctx context.Context) {
	systray.Quit()
	a.applyIdleMode(false)

	if a.cancel != nil {
		a.cancel()
	}
	a.startupWg.Wait()

	// Fechar database
	if a.db != nil {
		if err := a.db.Close(); err != nil {
			log.Printf("[shutdown] erro ao fechar database: %v", err)
		}
	}
	a.logs.closeFile()
}

// RequestAppClose allows the next window-close cycle to terminate the process.
func (a *App) RequestAppClose() {
	a.closeMu.Lock()
	a.allowClose = true
	a.closeMu.Unlock()
}

// ShouldHideOnClose reports whether close events should hide to tray.
func (a *App) ShouldHideOnClose() bool {
	a.closeMu.RLock()
	defer a.closeMu.RUnlock()
	return !a.allowClose
}

// IsTrayReady reports whether tray menu/actions are fully initialized.
func (a *App) IsTrayReady() bool {
	return a.trayReady.Load()
}

// clearMemoryCaches limpa caches em memória para economizar recursos quando
// o app está minimizado no tray. Os dados persistem no SQLite e serão
// recarregados quando necessário.
func (a *App) clearMemoryCaches() {
	// Limpar cache de AgentInfo em memória (mantém no SQLite)
	a.agentInfo.invalidate()
	a.appStorePolicy.Invalidate()

	// Limpar cache de inventário em memória
	a.invCache.mu.Lock()
	a.invCache.loaded = false
	a.invCache.report = models.InventoryReport{}
	a.invCache.mu.Unlock()

	log.Println("[tray] caches em memória limpos para economizar recursos")
}

// GetStartupError returns the error (if any) from the background startup
// inventory collection, so the frontend can display a meaningful message.
func (a *App) GetStartupError() string {
	a.startupMu.RLock()
	defer a.startupMu.RUnlock()
	if a.startupErr != nil {
		return a.startupErr.Error()
	}
	return ""
}

// GetServiceHealth retorna o status de saúde do Windows Service (processo headless)
// Conecta ao named pipe do serviço e recupera dados de saúde dos componentes
func normalizeServiceHealthPayload(data map[string]interface{}) dto.ServiceHealthPayload {
	if data == nil {
		return dto.ServiceHealthPayload{}
	}

	payload := dto.ServiceHealthPayload{
		Running:     true,
		ServiceOnly: true,
	}
	if v, ok := data["uptime"].(string); ok {
		payload.Uptime = v
	}
	if v, ok := data["version"].(string); ok {
		payload.Version = v
	}

	rawComponents, ok := data["components"].([]interface{})
	if !ok {
		return payload
	}

	components := make([]dto.HealthCheckItem, 0, len(rawComponents))
	for _, raw := range rawComponents {
		componentMap, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}

		item := dto.HealthCheckItem{}
		if v, ok := componentMap["Component"].(string); ok {
			item.Component = v
		}
		if v, ok := componentMap["Status"].(string); ok {
			item.Status = v
		}
		if v, ok := componentMap["LastBeat"].(string); ok {
			item.LastBeat = v
		}
		if v, ok := componentMap["Message"].(string); ok {
			item.Message = v
		}
		if v, ok := componentMap["CheckedAt"].(string); ok {
			item.CheckedAt = v
		}
		if v, ok := componentMap["Recoverable"].(bool); ok {
			item.Recoverable = v
		}
		components = append(components, item)
	}

	payload.Components = components
	return payload
}

func serviceOnlyUnavailablePayload(detail string) dto.ServiceHealthPayload {
	return dto.ServiceHealthPayload{
		Error:       &detail,
		Running:     false,
		ServiceOnly: true,
		UserMessage: "Nao foi possivel comunicar com o servico Discovery. Reinicie o computador e tente novamente. Se o problema persistir, contate o suporte.",
	}
}

// GetServiceHealth returns the health of the headless Windows Service.
// Retained as map[string]interface{} for Wails frontend compatibility.
func (a *App) GetServiceHealth() map[string]interface{} {
	var payload dto.ServiceHealthPayload

	if a.serviceClient == nil {
		payload = serviceOnlyUnavailablePayload("service client not initialized")
		return toMap(payload)
	}

	// Tentar conectar ao serviço se não conectado
	if !a.serviceClient.IsConnected() {
		if err := a.serviceClient.Connect(a.ctx); err != nil {
			payload = serviceOnlyUnavailablePayload(fmt.Sprintf("failed to connect to service: %v", err))
			return toMap(payload)
		}
	}

	// Fazer requisição ao serviço
	resp, err := a.serviceClient.GetServiceHealth(a.ctx)
	if err != nil {
		payload = serviceOnlyUnavailablePayload(fmt.Sprintf("failed to get service health: %v", err))
		return toMap(payload)
	}

	if resp != nil && resp.Data != nil {
		payload = normalizeServiceHealthPayload(resp.Data)
	} else {
		payload = serviceOnlyUnavailablePayload("empty response from service")
	}
	return toMap(payload)
}

// toMap converte um dto.ServiceHealthPayload para map[string]interface{} para
// compatibilidade com o frontend Wails.
func toMap(p dto.ServiceHealthPayload) map[string]interface{} {
	m := map[string]interface{}{
		"running":      p.Running,
		"service_only": p.ServiceOnly,
	}
	if p.Error != nil {
		m["error"] = *p.Error
	}
	if p.UserMessage != "" {
		m["user_message"] = p.UserMessage
	}
	if p.Uptime != "" {
		m["uptime"] = p.Uptime
	}
	if p.Version != "" {
		m["version"] = p.Version
	}
	if len(p.Components) > 0 {
		components := make([]map[string]interface{}, len(p.Components))
		for i, c := range p.Components {
			components[i] = map[string]interface{}{
				"component":   c.Component,
				"status":      c.Status,
				"message":     c.Message,
				"lastBeat":    c.LastBeat,
				"checkedAt":   c.CheckedAt,
				"recoverable": c.Recoverable,
			}
		}
		m["components"] = components
	}
	return m
}

func (a *App) beginActivity(activity string) func() {
	a.activityMu.Lock()
	a.activeOps++
	shouldLeaveIdle := a.activeOps == 1
	a.activityMu.Unlock()

	if shouldLeaveIdle {
		supported := a.applyIdleMode(false)
		if supported {
			a.logs.append("[efficiency] modo eficiencia desativado: " + activity)
		}
	}

	return func() {
		a.activityMu.Lock()
		if a.activeOps > 0 {
			a.activeOps--
		}
		shouldEnterIdle := a.activeOps == 0
		a.activityMu.Unlock()

		if shouldEnterIdle {
			supported := a.applyIdleMode(true)
			if supported {
				a.logs.append("[efficiency] modo eficiencia ativado (aguardo)")
			}
		}
	}
}

func (a *App) applyIdleMode(idle bool) bool {
	if !efficiencyModeEnabled {
		a.activityMu.Lock()
		a.idleKnown = true
		a.idleCapable = false
		a.lastIdle = false
		a.activityMu.Unlock()
		a.updateTrayIdleState(false, false)
		return false
	}

	a.activityMu.Lock()
	if a.lastIdle == idle && a.idleKnown {
		supported := a.idleCapable
		a.activityMu.Unlock()
		return supported
	}
	a.lastIdle = idle
	a.activityMu.Unlock()

	supported, err := processutil.SetEfficiencyMode(idle)
	a.activityMu.Lock()
	a.idleKnown = true
	a.idleCapable = supported
	a.activityMu.Unlock()

	if err != nil {
		a.logs.append("[efficiency] erro ao alterar modo: " + err.Error())
	}

	if idle {
		if trimErr := processutil.TrimCurrentProcessWorkingSet(); trimErr != nil {
			a.logs.append("[efficiency] erro ao reduzir memoria: " + trimErr.Error())
		}
	}

	a.updateTrayIdleState(idle, supported)
	return supported
}
