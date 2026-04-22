package app

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/energye/systray"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"discovery/app/debug"
	appinventory "discovery/app/inventory"
	appsupport "discovery/app/support"
	"discovery/app/updates"
	"discovery/internal/agentconn"
	"discovery/internal/ai"
	"discovery/internal/automation"
	"discovery/internal/data"
	"discovery/internal/database"
	"discovery/internal/inventory"
	"discovery/internal/mcp"
	"discovery/internal/models"
	"discovery/internal/printer"
	"discovery/internal/processutil"
	"discovery/internal/service"
	"discovery/internal/services"
	"discovery/internal/watchdog"
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

	WindowWidth  = 1280
	WindowHeight = 860
)

// GetDataDir retorna o diretório de dados da aplicação (exportado para uso em outros pacotes).
// Prioridade (Windows): C:\ProgramData\Discovery -> LOCALAPPDATA\Discovery -> home/.discovery
func GetDataDir() string {
	return getDataDir()
}

func getDataDir() string {
	if runtime.GOOS == "windows" {
		// 1º: Usar C:\ProgramData\Discovery (compartilhado entre usuários)
		if programData := strings.TrimSpace(os.Getenv("ProgramData")); programData != "" {
			return filepath.Join(programData, "Discovery")
		}
		// 2º: Fallback para LOCALAPPDATA\Discovery (compatibilidade)
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			return filepath.Join(localAppData, "Discovery")
		}
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".discovery")
	}
	return "."
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
	watchdogSvc    *watchdog.Watchdog
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

	agentConfigMu sync.RWMutex
	agentConfig   AgentConfiguration

	startupMu         sync.RWMutex
	startupErr        error
	startupWg         sync.WaitGroup
	activityMu        sync.Mutex
	activeOps         int
	lastIdle          bool
	idleKnown         bool
	idleCapable       bool
	closeMu           sync.RWMutex
	allowClose        bool
	trayReady         atomic.Bool
	trayIcon          []byte
	meshEnsureRunning atomic.Bool

	// serviceConnectedMode é true quando o Windows Service foi detectado no startup.
	// Quando ativo, workers locais de automação e inventário são omitidos para
	// evitar duplicação com os workers do service (arquitetura service-first).
	serviceConnectedMode atomic.Bool

	notificationMu      sync.Mutex
	pendingNotifyResult map[string]chan string
	notificationByKey   map[string]string
}

func NewApp(opts AppStartupOptions) *App {
	catalogClient := data.NewHTTPClient(catalogURL, catalogTimeout)
	wingetClient := winget.NewClient(wingetTimeout)
	inventoryProvider := inventory.NewProvider(inventoryTimeout)
	printerManager := printer.NewManager(printerTimeout)

	reg := mcp.NewRegistry()
	chatSvc := ai.NewService(reg)

	// Initialize watchdog with default config
	watchdogSvc := watchdog.New(watchdog.DefaultConfig())

	// Initialize service client for communicating with Windows Service
	serviceClient := service.NewServiceClient()

	a := &App{
		runtimeFlags:        RuntimeFlags{DebugMode: opts.DebugMode},
		trayIcon:            opts.TrayIcon,
		updateTrigger:       make(chan struct{}, 1),
		catalogSvc:          services.NewCatalogService(catalogClient),
		catalogClient:       catalogClient,
		appsSvc:             services.NewAppsService(wingetClient),
		invSvc:              services.NewInventoryService(inventoryProvider),
		printerSvc:          services.NewPrinterService(printerManager),
		mcpRegistry:         reg,
		chatSvc:             chatSvc,
		watchdogSvc:         watchdogSvc,
		serviceClient:       serviceClient,
		pendingNotifyResult: make(map[string]chan string),
		notificationByKey:   make(map[string]string),
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
				ClientID:                 agentCfg.ClientID,
				SiteID:                   agentCfg.SiteID,
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
		Watchdog:       a.watchdogSvc,
		ResolveAllowed: a.resolveAllowedPackage,
		GetCatalog:     a.getCatalogFromAppStore,
		BeginActivity:  a.beginActivity,
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
	if logPath := strings.TrimSpace(os.Getenv("DISCOVERY_LOG_FILE")); logPath != "" {
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

	// Register watchdog recovery actions for critical components
	a.registerWatchdogRecovery()

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

func (a *App) featureEnabled(flag *bool) bool {
	if flag == nil {
		return true
	}
	return *flag
}

func (a *App) startup(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	a.ctx = ctx
	a.cancel = cancel

	// Start watchdog monitoring
	a.watchdogSvc.Start(ctx)
	a.watchdogSvc.Suspend(watchdog.ComponentUI, uiRuntimeAwaitingBootstrapReason)
	log.Println("[startup] watchdog iniciado")

	go a.StartP2PTelemetryLoop(ctx)

	a.startTray()
	if a.runtimeFlags.StartMinimized {
		a.hideWindowOnStartup()
	}
	a.applyIdleMode(true)

	// Inicializar database SQLite
	dataDir := getDataDir()
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
	watchdog.SafeGoWithContext(ctx, "inventory-startup", a.watchdogSvc, watchdog.ComponentInventory, func(ctx context.Context) {
		defer a.startupWg.Done()

		// Quando o service está disponível, ele já gerencia inventário; pular coleta local.
		if a.serviceConnectedMode.Load() {
			log.Println("[startup] inventory-startup: ignorado (service disponível)")
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
	watchdog.SafeGoWithContext(ctx, "agent-connection", a.watchdogSvc, watchdog.ComponentAgent, func(ctx context.Context) {
		defer a.startupWg.Done()

		// Bootstrap pós-instalação: se houver URL/KEY do instalador, resolver token/agentId.
		if a.debugSvc != nil {
			a.debugSvc.BootstrapAgentCredentialsFromInstallerConfig(ctx)
		}

		// MeshCentral deve iniciar somente apos autenticar e carregar credenciais do agente.
		go a.ensureMeshCentralInstalled(ctx, "startup-auth", false)

		// Periodic heartbeat for agent connection
		heartbeat := watchdog.NewPeriodicHeartbeat(a.watchdogSvc, watchdog.ComponentAgent, 25*time.Second)
		heartbeat.Start(ctx)
		defer heartbeat.Stop()

		a.agentConn.Run(ctx)
	})

	a.startupWg.Add(1)
	watchdog.SafeGoWithContext(ctx, "agent-decommission-outbox", a.watchdogSvc, watchdog.ComponentAgent, func(ctx context.Context) {
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
	watchdog.SafeGoWithContext(ctx, "automation-service", a.watchdogSvc, watchdog.ComponentAutomation, func(ctx context.Context) {
		defer a.startupWg.Done()
		if a.automationSvc == nil {
			return
		}

		// Quando o service está disponível, ele gerencia automação; pular worker local.
		if a.serviceConnectedMode.Load() {
			log.Println("[startup] automation-service: ignorado (service disponível)")
			return
		}

		heartbeat := watchdog.NewPeriodicHeartbeat(a.watchdogSvc, watchdog.ComponentAutomation, 25*time.Second)
		heartbeat.Start(ctx)
		defer heartbeat.Stop()

		a.automationSvc.Run(ctx, func() {
			a.watchdogSvc.Heartbeat(watchdog.ComponentAutomation)
		})
	})

	a.startupWg.Add(1)
	watchdog.SafeGoWithContext(ctx, "sync-coordinator", a.watchdogSvc, watchdog.ComponentAgent, func(ctx context.Context) {
		defer a.startupWg.Done()
		if a.syncCoord == nil {
			return
		}
		a.syncCoord.Run(ctx)
	})

	a.startupWg.Add(1)
	watchdog.SafeGoWithContext(ctx, "p2p-coordinator", a.watchdogSvc, watchdog.ComponentAgent, func(ctx context.Context) {
		defer a.startupWg.Done()
		if a.p2pCoord == nil {
			return
		}
		a.p2pCoord.Run(ctx)
	})

	a.startupWg.Add(1)
	watchdog.SafeGoWithContext(ctx, "outbox-ttl-cleanup", a.watchdogSvc, watchdog.ComponentAgent, func(ctx context.Context) {
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

func (a *App) startupLogf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	log.Print(line)
	a.logs.append(line)
}

func (a *App) ensureOsqueryInstalled(ctx context.Context) {
	if runtime.GOOS != "windows" {
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
	watchdog.SafeGoWithContext(a.ctx, "startup-hide-window", a.watchdogSvc, watchdog.ComponentTray, func(ctx context.Context) {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()

		timeout := time.NewTimer(12 * time.Second)
		defer timeout.Stop()

		for {
			select {
			case <-ctx.Done():
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
	})
}

// shutdown is called when the application is closing; it cancels background
// work and waits for goroutines to finish.
func (a *App) shutdown(ctx context.Context) {
	systray.Quit()
	a.applyIdleMode(false)

	// Stop watchdog
	if a.watchdogSvc != nil {
		a.watchdogSvc.Stop()
		log.Println("[shutdown] watchdog parado")
	}

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

// registerWatchdogRecovery defines recovery actions for critical components.
func (a *App) registerWatchdogRecovery() {
	// Tray recovery: attempt to restart systray (limited effectiveness)
	a.watchdogSvc.RegisterRecovery(watchdog.ComponentTray, func(component watchdog.Component) error {
		log.Println("[watchdog] tentando recuperar tray (limitado - systray nao suporta restart)")
		// Systray doesn't support restart, but we can update state
		a.updateTrayIdleState(false, true)
		time.Sleep(2 * time.Second)
		a.updateTrayIdleState(true, true)
		return nil
	})

	// Agent connection recovery: signal reconnection attempt
	a.watchdogSvc.RegisterRecovery(watchdog.ComponentAgent, func(component watchdog.Component) error {
		log.Println("[watchdog] agente connection parece travado - verificando status")
		status := a.agentConn.GetStatus()
		if !status.Connected {
			log.Println("[watchdog] agente desconectado - aguardando reconexao automatica")
		}
		return nil
	})

	// AI service recovery: stop any stuck stream
	a.watchdogSvc.RegisterRecovery(watchdog.ComponentAI, func(component watchdog.Component) error {
		log.Println("[watchdog] AI service travado - cancelando stream ativo")
		stopped := a.chatSvc.StopStream()
		if stopped {
			log.Println("[watchdog] stream AI cancelado com sucesso")
			wailsRuntime.EventsEmit(a.ctx, "chat:error", "Stream interrompido automaticamente por travamento")
		}
		return nil
	})

	// Inventory recovery: clear cache to force refresh
	a.watchdogSvc.RegisterRecovery(watchdog.ComponentInventory, func(component watchdog.Component) error {
		log.Println("[watchdog] inventario travado - limpando cache")
		a.invCache.mu.Lock()
		a.invCache.loaded = false
		a.invCache.mu.Unlock()
		return nil
	})

	// Automation recovery: tentar um novo refresh de policy.
	a.watchdogSvc.RegisterRecovery(watchdog.ComponentAutomation, func(component watchdog.Component) error {
		log.Println("[watchdog] automacao degradada - tentando refresh de policy")
		if a.automationSvc == nil {
			return nil
		}
		ctx := a.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		_, err := a.automationSvc.RefreshPolicy(ctx, false)
		return err
	})

	// UI runtime recovery: use a native Windows probe when available and try to
	// re-surface the window while asking the frontend to self-recover.
	a.watchdogSvc.RegisterRecovery(watchdog.ComponentUI, func(component watchdog.Component) error {
		probe := probeUIRuntimeNative()
		if probe.Supported {
			log.Printf("[watchdog] UI runtime unhealthy - native probe: found=%t visible=%t hung=%t title=%q",
				probe.WindowFound, probe.Visible, probe.Hung, probe.Title)
		} else {
			log.Println("[watchdog] UI runtime unhealthy - probe nativo indisponivel")
		}

		if a.ctx != nil {
			wailsRuntime.WindowUnminimise(a.ctx)
			wailsRuntime.WindowShow(a.ctx)
			wailsRuntime.WindowSetAlwaysOnTop(a.ctx, true)
			wailsRuntime.WindowSetAlwaysOnTop(a.ctx, false)
			wailsRuntime.EventsEmit(a.ctx, "watchdog:ui-recover", map[string]interface{}{
				"component":       string(component),
				"nativeSupported": probe.Supported,
				"windowFound":     probe.WindowFound,
				"visible":         probe.Visible,
				"hung":            probe.Hung,
				"title":           probe.Title,
				"reloadRequested": !probe.Supported || (probe.WindowFound && !probe.Hung),
			})
		}

		if probe.Supported && (!probe.WindowFound || probe.Hung) {
			if !probe.WindowFound {
				return fmt.Errorf("janela principal do processo nao encontrada")
			}
			return fmt.Errorf("janela principal marcada como hung pelo Windows")
		}
		return nil
	})

	// On unhealthy callback: emit event to frontend
	a.watchdogSvc.OnUnhealthy(func(check watchdog.HealthCheck) {
		if a.ctx != nil {
			wailsRuntime.EventsEmit(a.ctx, "watchdog:unhealthy", map[string]interface{}{
				"component":   string(check.Component),
				"status":      string(check.Status),
				"message":     check.Message,
				"recoverable": check.Recoverable,
			})
		}
	})
}

// GetWatchdogHealth returns the current health status of all monitored components.
func (a *App) GetWatchdogHealth() []map[string]interface{} {
	if a.watchdogSvc == nil {
		return []map[string]interface{}{}
	}

	checks := a.watchdogSvc.GetHealth()
	result := make([]map[string]interface{}, len(checks))

	for i, check := range checks {
		result[i] = map[string]interface{}{
			"component":   string(check.Component),
			"status":      string(check.Status),
			"message":     check.Message,
			"lastBeat":    check.LastBeat.Format(time.RFC3339),
			"checkedAt":   check.CheckedAt.Format(time.RFC3339),
			"recoverable": check.Recoverable,
		}
	}

	return result
}

// GetServiceHealth retorna o status de saúde do Windows Service (processo headless)
// Conecta ao named pipe do serviço e recupera dados de saúde dos componentes
func normalizeServiceHealthPayload(data map[string]interface{}) map[string]interface{} {
	if data == nil {
		return map[string]interface{}{}
	}

	normalized := make(map[string]interface{}, len(data))
	for key, value := range data {
		normalized[key] = value
	}

	rawComponents, ok := normalized["components"].([]interface{})
	if !ok {
		return normalized
	}

	components := make([]map[string]interface{}, 0, len(rawComponents))
	for _, raw := range rawComponents {
		componentMap, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}

		item := make(map[string]interface{}, len(componentMap))
		for key, value := range componentMap {
			switch key {
			case "Component":
				item["component"] = value
			case "Status":
				item["status"] = value
			case "LastBeat":
				item["lastBeat"] = value
			case "Message":
				item["message"] = value
			case "CheckedAt":
				item["checkedAt"] = value
			case "Recoverable":
				item["recoverable"] = value
			default:
				item[key] = value
			}
		}
		components = append(components, item)
	}

	normalized["components"] = components
	return normalized
}

func serviceOnlyUnavailablePayload(detail string) map[string]interface{} {
	guidance := "Nao foi possivel comunicar com o servico Discovery. Reinicie o computador e tente novamente. Se o problema persistir, contate o suporte."

	return map[string]interface{}{
		"error":        detail,
		"running":      false,
		"service_only": true,
		"user_message": guidance,
	}
}

func (a *App) GetServiceHealth() map[string]interface{} {
	if a.serviceClient == nil {
		return serviceOnlyUnavailablePayload("service client not initialized")
	}

	// Tentar conectar ao serviço se não conectado
	if !a.serviceClient.IsConnected() {
		if err := a.serviceClient.Connect(a.ctx); err != nil {
			return serviceOnlyUnavailablePayload(fmt.Sprintf("failed to connect to service: %v", err))
		}
	}

	// Fazer requisição ao serviço
	resp, err := a.serviceClient.GetServiceHealth(a.ctx)
	if err != nil {
		return serviceOnlyUnavailablePayload(fmt.Sprintf("failed to get service health: %v", err))
	}

	// Retornar resposta do serviço (já é um map)
	if resp != nil && resp.Data != nil {
		return normalizeServiceHealthPayload(resp.Data)
	}

	return serviceOnlyUnavailablePayload("empty response from service")
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
