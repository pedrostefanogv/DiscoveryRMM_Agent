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
	"discovery/internal/inventory"
	"discovery/internal/mcp"
	"discovery/internal/models"
	"discovery/internal/platform"
	"discovery/internal/printer"
	"discovery/internal/processutil"
	"discovery/internal/selfupdate"
	"discovery/internal/services"
	"discovery/internal/winget"
	"path/filepath"
)

var Version = "dev"

const (
	catalogURL       = "https://raw.githubusercontent.com/pedrostefanogv/winget-package-explo/refs/heads/main/public/data/packages.json"
	catalogTimeout   = 10 * time.Minute
	wingetTimeout    = 5 * time.Minute
	inventoryTimeout = 45 * time.Second
	printerTimeout   = 30 * time.Second
	chatConfigFile   = "chat_config.json"

	efficiencyModeEnabled = false

	WindowWidth     = 1280
	WindowHeight    = 860
	WindowMinWidth  = 980
	WindowMinHeight = 700
)

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

	consolEngine *ConsolidationEngine

	debugHTTP *debugHTTPServer

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

	startupTime time.Time

	notificationMu      sync.Mutex
	pendingNotifyResult map[string]chan string
	notificationByKey   map[string]string

	queuedForceHeartbeat atomic.Bool

	selfUpdater   *selfupdate.Updater
	selfUpdaterCh chan bool
}

func NewApp(opts AppStartupOptions) *App {
	catalogClient := data.NewHTTPClient(catalogURL, catalogTimeout)
	wingetClient := winget.NewClient(wingetTimeout)
	chocolateyClient := chocolatey.NewClient(wingetTimeout)
	inventoryProvider := inventory.NewProvider(inventoryTimeout)
	printerManager := printer.NewManager(printerTimeout)

	reg := mcp.NewRegistry()
	chatSvc := ai.NewService(reg)

	a := &App{
		ctx:                 context.Background(),
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
	a.remoteDebug = newRemoteDebugManager(a.logs.append, a.GetDebugConfig, a.GetAgentConfiguration, a.logs.subscribe, a.logs.snapshotAndSubscribe)
	inventoryProvider.SetProgressCallback(func() {
		a.pulseInventoryHeartbeat()
	})
	a.agentConn = agentconn.NewRuntime(agentconn.Options{
		LoadConfig: func() agentconn.Config {
			cfg := a.GetDebugConfig()
			agentCfg := a.GetAgentConfiguration()

			// Fallback para clientId/siteId do InstallerConfig quando
			// AgentConfiguration ainda nao foi populada pelo sync (ex.: primeiro
			// boot apos instalacao, onde o bootstrap ja persiste os valores mas
			// o /me/configuration ainda nao respondeu).
			clientID := agentCfg.ClientID
			siteID := agentCfg.SiteID
			if strings.TrimSpace(clientID) == "" || strings.TrimSpace(siteID) == "" {
				if inst, _, err := loadInstallerConfig(); err == nil {
					if strings.TrimSpace(clientID) == "" {
						clientID = strings.TrimSpace(inst.ClientID)
					}
					if strings.TrimSpace(siteID) == "" {
						siteID = strings.TrimSpace(inst.SiteID)
					}
				}
			}

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
				ClientID:                 clientID,
				SiteID:                   siteID,
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
		OnP2PEvent:                    a.handleP2PEvent,
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
		DB:                       nil,
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
	a.selfUpdaterCh = make(chan bool, 4)
	a.selfUpdater = &selfupdate.Updater{
		GetToken:            func() string { return a.GetDebugConfig().AuthToken },
		GetAgentID:          func() string { return a.GetDebugConfig().AgentID },
		GetApiScheme:        func() string { return a.GetDebugConfig().ApiScheme },
		GetApiServer:        func() string { return a.GetDebugConfig().ApiServer },
		GetPolicy:           func() selfupdate.Policy { return selfupdate.NormalizePolicy(a.GetAgentConfiguration().AgentUpdate) },
		TempDir:             filepath.Join(platform.DataDir(), "updates"),
		Logf:                func(format string, args ...any) { a.logs.append("[selfupdate] " + fmt.Sprintf(format, args...)) },
		InvalidateCh:        a.selfUpdaterCh,
		OnSelfUpdateInstall: a.selfUpdateInstallWithPSADT,
	}
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

	mcp.RegisterDiscoveryTools(reg, a)

	a.queuedForceHeartbeat.Store(false)

	if opts.DebugMode {
		a.logs.append("[startup] modo debug ativo por tecla de atalho (execucao atual)")
	}

	normalizePSADTConfigDefaults(&a.agentConfig.PSADT)
	normalizeRolloutDefaults(&a.agentConfig.Rollout)

	return a
}

func (a *App) GetRuntimeFlags() RuntimeFlags {
	return a.runtimeFlags
}

func (a *App) SetContext(ctx context.Context) {
	if a.cancel != nil {
		a.cancel()
	}
	ctx, cancel := context.WithCancel(ctx)
	a.ctx = ctx
	a.cancel = cancel
}

func (a *App) Ctx() context.Context { return a.ctx }

func (a *App) ClearMemoryCaches() { a.clearMemoryCaches() }

func AppStartup(a *App) func(context.Context) { return a.startup }

func AppShutdown(a *App) func(context.Context) { return a.shutdown }

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

const defaultHeartbeatIntervalSeconds = 60
const minHeartbeatIntervalSeconds = 30

func heartbeatIntervalFromAgentConfig(agentCfg AgentConfiguration) int {
	if agentCfg.AgentHeartbeatIntervalSeconds != nil && *agentCfg.AgentHeartbeatIntervalSeconds > 0 {
		if *agentCfg.AgentHeartbeatIntervalSeconds < minHeartbeatIntervalSeconds {
			return minHeartbeatIntervalSeconds
		}
		return *agentCfg.AgentHeartbeatIntervalSeconds
	}
	return defaultHeartbeatIntervalSeconds
}

func (a *App) getHeartbeatMetrics() agentconn.AgentHeartbeatMetrics {
	hostname, _ := os.Hostname()
	metrics := agentconn.AgentHeartbeatMetrics{
		Hostname:         hostname,
		CpuPercent:       -1,
		MemoryPercent:    -1,
		DiskPercent:      -1,
		DiskReadPercent:  -1,
		DiskWritePercent: -1,
		DiskResponseMs:   -1,
		UptimeSeconds:    int64(time.Since(a.startupTime).Seconds()),
		P2pPeers:         a.getKnownP2PPeers(),
	}

	// CollectHeartbeatMetrics usa APIs nativas no Windows (zero subprocessos)
	// e osquery socket no Linux/macOS. Todos os fallbacks de CPU/memória/disco
	// estão internalizados — não precisa de fallback adicional aqui.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if m := inventory.CollectHeartbeatMetrics(ctx); m != nil {
		mergeHeartbeatMetrics(&metrics, m)
		metrics.P2pPeers = a.getKnownP2PPeers()
	}

	// Enriquecer com dados de endereçamento P2P (libp2p peer ID, addrs, port)
	if a.p2pCoord != nil {
		metrics.PeerID, metrics.Addrs, metrics.Port = a.p2pCoord.getP2PAddressingInfo()
	}

	return metrics
}

func mergeHeartbeatMetrics(dst *agentconn.AgentHeartbeatMetrics, src *agentconn.AgentHeartbeatMetrics) {
	if dst == nil || src == nil {
		return
	}

	if host := strings.TrimSpace(src.Hostname); host != "" {
		dst.Hostname = host
	}
	if src.CpuPercent >= 0 {
		dst.CpuPercent = src.CpuPercent
	}
	if src.MemoryPercent >= 0 {
		dst.MemoryPercent = src.MemoryPercent
	}
	if src.MemoryTotalGb > 0 {
		dst.MemoryTotalGb = src.MemoryTotalGb
	}
	if src.MemoryUsedGb > 0 {
		dst.MemoryUsedGb = src.MemoryUsedGb
	}
	if src.DiskPercent >= 0 {
		dst.DiskPercent = src.DiskPercent
	}
	if src.DiskTotalGb > 0 {
		dst.DiskTotalGb = src.DiskTotalGb
	}
	if src.DiskUsedGb > 0 {
		dst.DiskUsedGb = src.DiskUsedGb
	}
	if src.DiskReadPercent >= 0 {
		dst.DiskReadPercent = src.DiskReadPercent
	}
	if src.DiskWritePercent >= 0 {
		dst.DiskWritePercent = src.DiskWritePercent
	}
	if src.DiskResponseMs >= 0 {
		dst.DiskResponseMs = src.DiskResponseMs
	}
	if src.UptimeSeconds > 0 {
		dst.UptimeSeconds = src.UptimeSeconds
	}
	if src.ProcessCount > 0 {
		dst.ProcessCount = src.ProcessCount
	}
	if src.PeerID != "" {
		dst.PeerID = src.PeerID
	}
	if len(src.Addrs) > 0 {
		dst.Addrs = append([]string(nil), src.Addrs...)
	}
	if src.Port > 0 {
		dst.Port = src.Port
	}
}

func (a *App) getKnownP2PPeers() int {
	if a.p2pCoord == nil {
		return 0
	}
	return len(a.p2pCoord.GetPeers())
}

func (a *App) startup(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	a.ctx = ctx
	a.cancel = cancel

	captureStdLog(&a.logs)

	if a.runtimeFlags.DebugMode {
		if a.debugHTTP == nil {
			if err := a.StartDebugHTTPServer(); err != nil {
				log.Printf("[debug-http] falha ao iniciar servidor HTTP local: %v", err)
			}
		}
		if port := a.GetDebugHTTPPort(); port > 0 {
			a.logs.append(fmt.Sprintf("[debug-http] servidor HTTP local iniciado em http://127.0.0.1:%d", port))
		}
	}

	a.safeGo(func() { a.StartP2PTelemetryLoop(ctx) })

	a.startTray()
	if a.runtimeFlags.StartMinimized {
		a.hideWindowOnStartup()
	}
	a.applyIdleMode(true)

	dataDir := GetDataDir()
	db, err := database.Open(dataDir)
	if err != nil {
		log.Printf("[startup] AVISO: falha ao abrir database: %v", err)
	} else {
		a.db = db
		log.Printf("[startup] database SQLite inicializado em %s", dataDir)

		if a.catalogClient != nil {
			a.catalogClient.SetDatabase(db)
		}
		if a.automationSvc != nil {
			a.automationSvc.SetDB(db)
		}
		if a.inventorySvc != nil {
			a.inventorySvc.SetDB(db)
		}
		agentIDForEngine := strings.TrimSpace(a.GetDebugConfig().AgentID)
		a.consolEngine = newConsolidationEngine(db, agentIDForEngine)
	}

	log.Println("[startup] runtime local (tray) ativo — todos os workers locais iniciados")

	a.startupWg.Add(1)
	a.safeGo(func() {
		defer a.startupWg.Done()

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

		if a.debugSvc != nil {
			a.debugSvc.BootstrapAgentCredentialsFromInstallerConfig(ctx)
		}

		// Após bootstrap bem-sucedido, dispara reconciliação de
		// recursos que dependem de credenciais (inventário, sync,
		// configuração do agente). Isso garante que o agent fique
		// plenamente operacional no primeiro boot.
		go func() {
			_ = a.onPostBootstrapProvisioned(ctx)
		}()

		a.safeGo(func() {
			a.ensureMeshCentralInstalled(ctx, "startup-auth", false)
		})

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
		if a.selfUpdater != nil {
			a.selfUpdater.ResumePendingInstallReport(a.ctx)
			a.selfUpdater.Run(a.ctx, 0)
		}
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

func (a *App) SendTestHeartbeat() string {
	if !a.queuedForceHeartbeat.CompareAndSwap(false, true) {
		return "erro: heartbeat manual ja em andamento"
	}
	defer a.queuedForceHeartbeat.Store(false)

	a.logs.append("[heartbeat][manual] enviando heartbeat manual...")
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
		a.logs.append("[startup] post-bootstrap: timeout aguardando provisionamento")
		return nil
	}

	a.logs.append("[startup] post-bootstrap: agente provisionado — reconciliando recursos")

	// 1. osquery + inventário inicial (não executou no goroutine #2 porque
	//    ainda não estava provisionado).
	a.ensureOsqueryInstalled(ctx)
	if !a.invCache.has() {
		a.startupLogf("[startup] post-bootstrap: executando inventario inicial")
		report, err := a.collectInventoryWithHeartbeat(ctx)
		if err != nil {
			a.startupLogf("[startup] post-bootstrap: falha ao coletar inventario: %v", err)
		} else {
			a.invCache.set(report)
			if a.inventorySvc != nil {
				a.inventorySvc.SyncInventoryOnStartup(ctx, report)
			}
		}
	}

	// 2. Refresh da configuração do agent (clientId/siteId, políticas).
	if a.syncCoord != nil {
		_ = a.refreshAgentConfiguration(ctx)
		a.syncCoord.reconcileFromManifest(ctx, "post-bootstrap")
	}

	// 3. Automação — carrega políticas iniciais.
	if a.automationSvc != nil {
		if _, err := a.automationSvc.RefreshPolicy(ctx, false); err != nil {
			a.logs.append("[startup] post-bootstrap: falha ao carregar politicas de automacao: " + err.Error())
		}
	}

	// 4. App-store e suporte.
	if _, err := a.loadEffectiveAppStorePolicy(ctx, true); err != nil {
		a.logs.append("[startup] post-bootstrap: falha ao carregar app-store: " + err.Error())
	}
	if a.supportSvc != nil && a.featureEnabled(a.GetAgentConfiguration().KnowledgeBaseEnabled) {
		if err := a.supportSvc.RefreshKnowledgeBase(); err != nil {
			a.logs.append("[startup] post-bootstrap: falha ao atualizar knowledge base: " + err.Error())
		}
	}

	a.logs.append("[startup] post-bootstrap: reconciliacao concluida")
	return nil
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

func (a *App) shutdown(ctx context.Context) {
	systray.Quit()
	a.applyIdleMode(false)

	a.StopDebugHTTPServer()

	if a.cancel != nil {
		a.cancel()
	}
	a.startupWg.Wait()

	if a.db != nil {
		if err := a.db.Close(); err != nil {
			log.Printf("[shutdown] erro ao fechar database: %v", err)
		}
	}
	a.logs.closeFile()
}

func (a *App) RequestAppClose() {
	a.closeMu.Lock()
	a.allowClose = true
	a.closeMu.Unlock()
}

func (a *App) ShouldHideOnClose() bool {
	a.closeMu.RLock()
	defer a.closeMu.RUnlock()
	return !a.allowClose
}

func (a *App) IsTrayReady() bool {
	return a.trayReady.Load()
}

func (a *App) clearMemoryCaches() {
	a.agentInfo.invalidate()
	a.appStorePolicy.Invalidate()

	a.invCache.mu.Lock()
	a.invCache.loaded = false
	a.invCache.report = models.InventoryReport{}
	a.invCache.mu.Unlock()

	log.Println("[tray] caches em memória limpos para economizar recursos")
}

func (a *App) GetStartupError() string {
	a.startupMu.RLock()
	defer a.startupMu.RUnlock()
	if a.startupErr != nil {
		return a.startupErr.Error()
	}
	return ""
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
	sameState := a.lastIdle == idle && a.idleKnown
	if sameState {
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

func (a *App) GetServiceHealth() map[string]interface{} {
	return map[string]interface{}{
		"running":      true,
		"service_only": false,
		"user_message": "Runtime local ativo (tray icon no logon).",
	}
}
