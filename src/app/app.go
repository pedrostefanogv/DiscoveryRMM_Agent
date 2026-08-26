package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/wailsapp/wails/v3/pkg/application"

	"discovery/app/agentconfig"
	"discovery/app/apiclient"
	"discovery/app/appstore"
	"discovery/app/consolidation"
	"discovery/app/core/agentconn"
	"discovery/app/core/automation"
	"discovery/app/core/buildinfo"
	"discovery/app/core/chocolatey"
	"discovery/app/core/data"
	"discovery/app/core/database"
	"discovery/app/core/inventory"
	"discovery/app/core/mcp"
	"discovery/app/core/models"
	"discovery/app/core/platform"
	"discovery/app/core/printer"
	"discovery/app/core/processutil"
	"discovery/app/core/remotedebug"
	"discovery/app/core/remotesession"
	"discovery/app/core/safego"
	"discovery/app/core/selfupdate"
	"discovery/app/core/services"
	"discovery/app/core/winget"
	"discovery/app/customfields"
	"discovery/app/debug"
	"discovery/app/debughttp"
	"discovery/app/decommission"
	"discovery/app/installer"
	appinventory "discovery/app/inventory"
	"discovery/app/logs"
	"discovery/app/services/chat"
	"discovery/app/services/hardwareid"
	"discovery/app/services/memory"
	"discovery/app/services/notifications"
	"discovery/app/services/psadt"
	appsupport "discovery/app/support"
	syncsvc "discovery/app/sync"
	"discovery/app/tickets"
	"discovery/app/updates"
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

	mcpRegistry   *mcp.Registry
	chatSvc       *chat.Service
	psadtSvc      *psadt.Service
	automationSvc *automation.Service

	// toolsRegistration guarda o timestamp do último registro bem-sucedido de tools.
	// Usado para re-registrar se o cache do servidor expirou (TTL 5min por padrão no servidor).
	toolsRegistrationMu   sync.RWMutex
	lastToolsRegistration time.Time
	agentConn             *agentconn.Runtime
	remoteDebug           *remotedebug.Manager
	syncSvc               *syncsvc.Service
	p2pCoord              *p2pCoordinator
	updateTrigger         chan struct{}
	agentInfo             agentInfoCache
	appStorePolicy        appStorePolicyCache
	debugSvc              *debug.Service
	agentConfigSvc        *agentconfig.Service
	ticketsSvc            *tickets.Service
	updatesSvc            *updates.Service
	exporter              *updates.Exporter
	inventorySvc          *appinventory.Service
	supportSvc            *appsupport.Service

	consolEngine *consolidation.Engine

	debugHTTP  *debughttp.Server
	chatSSE    *debughttp.Server
	chatEvents *debughttp.ChatEventBroker

	// hardwareIDSvc encapsula a coleta e cache de identidade de hardware
	// (TPM EK + SMBIOS UUID).
	hardwareIDSvc *hardwareid.Service

	// memorySvc encapsula as memórias/anotações locais persistidas.
	memorySvc *memory.Service

	p2pMu                      sync.RWMutex
	p2pConfig                  P2PConfig
	p2pSeedPlanCache           cachedP2PSeedPlan
	p2pTelemetryRateLimitUntil time.Time

	agentConfigMu sync.RWMutex
	agentConfig   agentconfig.AgentConfiguration

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
	remoteSessionMgr         *remotesession.Manager
	activeRemoteSessions     atomic.Int32
	zeroTouchAttemptInFlight atomic.Bool
	zeroTouchApprovalPending atomic.Bool

	startupTime time.Time

	// notificationSvc encapsula o centro de notificações.
	notificationSvc *notifications.Service

	// apiClientSvc encapsula a detecção de features da API.
	apiClientSvc *apiclient.Service

	// customFieldsSvc encapsula o envio de campos customizados.
	customFieldsSvc *customfields.Service

	// appStoreSvc encapsula a lógica de app-store (fetch, cache e política).
	appStoreSvc *appstore.Service

	queuedForceHeartbeat atomic.Bool
	quitRequested        atomic.Bool

	selfUpdater   *selfupdate.Updater
	selfUpdaterCh chan bool

	deferredRestart *deferredRestartState

	// ── Wails v3 ──
	// Referências explícitas à aplicação/janela do Wails v3.
	// Substituem o acesso implícito via ctx do v2.
	app        *application.App
	mainWindow application.Window
	systemTray *application.SystemTray

	// Itens de status do menu do tray (atualizados dinamicamente).
	trayStatusHostname   *application.MenuItem
	trayStatusVersion    *application.MenuItem
	trayStatusConnection *application.MenuItem
}

// deferredRestartState tracks pending deferred restart state.
type deferredRestartState struct {
	mu           sync.Mutex
	deferCount   int
	maxDefers    int
	deferMinutes int
	message      string
	timer        *time.Timer
}

func NewApp(opts AppStartupOptions) *App {
	catalogClient := data.NewHTTPClient(catalogURL, catalogTimeout)
	wingetClient := winget.NewClient(wingetTimeout)
	chocolateyClient := chocolatey.NewClient(wingetTimeout)
	inventoryProvider := inventory.NewProvider(inventoryTimeout)
	printerManager := printer.NewManager(printerTimeout)

	reg := mcp.NewRegistry()

	a := &App{
		ctx:              context.Background(),
		runtimeFlags:     RuntimeFlags{DebugMode: opts.DebugMode},
		trayIcon:         opts.TrayIcon,
		trayProvisioning: opts.TrayProvisioningIcon,
		trayOffline:      opts.TrayOfflineIcon,
		updateTrigger:    make(chan struct{}, 1),
		catalogSvc:       services.NewCatalogService(catalogClient),
		catalogClient:    catalogClient,
		appsSvc:          services.NewAppsService(wingetClient, chocolateyClient),
		invSvc:           services.NewInventoryService(inventoryProvider),
		printerSvc:       services.NewPrinterService(printerManager),
		mcpRegistry:      reg,
		chatEvents:       debughttp.NewChatEventBroker(),
		startupTime:      time.Now(),
	}
	a.logs.Buffer = logs.New()
	installerSvc = installer.New(installer.Deps{
		NormalizeP2PConfig: normalizeP2PConfig,
	})
	decommissionSvc = decommission.New(decommission.Deps{
		LoadInstallerConfig: func() (decommission.InstallerConfig, string, error) {
			inst, path, err := loadInstallerConfig()
			if err != nil {
				return decommission.InstallerConfig{}, "", err
			}
			return decommission.InstallerConfig{
				APIScheme: inst.APIScheme,
				ApiServer: inst.ApiServer,
				ServerURL: inst.ServerURL,
				AuthToken: inst.AuthToken,
				AgentID:   inst.AgentID,
			}, path, nil
		},
		GetDataDir: GetDataDir,
	})
	a.chatSvc = chat.New(reg, chat.Deps{
		Ctx: func() context.Context { return a.ctx },
		Logf: func(line string) {
			a.logs.append(line)
		},
		GetDebugConfig: func() chat.DebugConfig {
			cfg := a.GetDebugConfig()
			return chat.DebugConfig{
				AgentID:   cfg.AgentID,
				ApiScheme: cfg.ApiScheme,
				ApiServer: cfg.ApiServer,
				AuthToken: cfg.AuthToken,
			}
		},
		GetAgentConfiguration: func() chat.AgentConfiguration {
			cfg := a.GetAgentConfiguration()
			return chat.AgentConfiguration{ChatAIEnabled: cfg.ChatAIEnabled}
		},
		BeginActivity:    a.beginActivity,
		EmitEvent:        a.EmitEvent,
		PublishChatEvent: a.PublishChatEvent,
		SafeGo:           a.safeGo,
		ChatConfigFile:   chatConfigFile,
	})
	a.psadtSvc = psadt.New(psadt.Deps{
		Logf: func(line string) {
			a.logs.append(line)
		},
		GetAgentConfiguration: func() psadt.AgentConfiguration {
			cfg := a.GetAgentConfiguration()
			return psadt.AgentConfiguration{
				PSADT: psadt.PSADTConfig{
					Enabled:         cfg.PSADT.Enabled,
					RequiredVersion: cfg.PSADT.RequiredVersion,
					InstallSource:   cfg.PSADT.InstallSource,
				},
			}
		},
		RuntimeDebugMode: func() bool {
			return a.runtimeFlags.DebugMode
		},
		DispatchNotification: func(req psadt.NotificationRequest) psadt.NotificationResponse {
			resp := a.DispatchNotification(NotificationDispatchRequest{
				NotificationID: req.NotificationID,
				Title:          req.Title,
				Message:        req.Message,
				Mode:           req.Mode,
				Severity:       req.Severity,
				EventType:      req.EventType,
				Layout:         req.Layout,
				TimeoutSeconds: req.TimeoutSeconds,
				Metadata:       req.Metadata,
			})
			return psadt.NotificationResponse{
				Accepted: resp.Accepted,
				Message:  resp.Message,
			}
		},
	})
	a.hardwareIDSvc = hardwareid.New(hardwareid.Deps{
		Logf: func(line string) {
			a.logs.append(line)
		},
	})
	a.memorySvc = memory.New(memory.Deps{
		DB: func() *database.DB {
			return a.db
		},
	})
	a.notificationSvc = notifications.New(notifications.Deps{
		Logf: func(line string) {
			a.logs.append(line)
		},
		Ctx: func() interface{ Done() <-chan struct{} } {
			return a.ctx
		},
		DB: func() *database.DB {
			return a.db
		},
		EmitEvent: a.EmitEvent,
		GetAgentConfiguration: func() notifications.AgentConfiguration {
			cfg := a.GetAgentConfiguration()
			return notifications.AgentConfiguration{
				Rollout: notifications.AgentRolloutConfig{
					EnableNotifications:           cfg.Rollout.EnableNotifications,
					BlockedNotificationEventTypes: cfg.Rollout.BlockedNotificationEventTypes,
					AllowedNotificationEventTypes: cfg.Rollout.AllowedNotificationEventTypes,
					EnableRequireConfirmation:     cfg.Rollout.EnableRequireConfirmation,
				},
				NotificationPolicies: mapNotificationPolicies(cfg.NotificationPolicies),
				NotificationBranding: notifications.AgentNotificationBrandingConfig{
					CompanyName: cfg.NotificationBranding.CompanyName,
					LogoURL:     cfg.NotificationBranding.LogoURL,
					BannerURL:   cfg.NotificationBranding.BannerURL,
				},
			}
		},
	})
	a.apiClientSvc = apiclient.New(apiclient.Deps{
		GetDebugConfig: func() apiclient.DebugConfig {
			cfg := a.GetDebugConfig()
			return apiclient.DebugConfig{
				ApiScheme: cfg.ApiScheme,
				ApiServer: cfg.ApiServer,
				AuthToken: cfg.AuthToken,
				AgentID:   cfg.AgentID,
			}
		},
		Logf: func(line string) {
			a.logs.append(line)
		},
	})
	a.customFieldsSvc = customfields.New(customfields.Deps{
		GetDebugConfig: func() customfields.DebugConfig {
			cfg := a.GetDebugConfig()
			return customfields.DebugConfig{
				ApiScheme: cfg.ApiScheme,
				ApiServer: cfg.ApiServer,
				AuthToken: cfg.AuthToken,
				AgentID:   cfg.AgentID,
			}
		},
	})
	a.appStoreSvc = appstore.New(appstore.Deps{
		GetDebugConfig: func() appstore.DebugConfig {
			cfg := a.GetDebugConfig()
			return appstore.DebugConfig{
				ApiScheme: cfg.ApiScheme,
				ApiServer: cfg.ApiServer,
				AuthToken: cfg.AuthToken,
				AgentID:   cfg.AgentID,
			}
		},
		GetAgentConfiguration: func() appstore.AgentConfiguration {
			cfg := a.GetAgentConfiguration()
			return appstore.AgentConfiguration{AppStoreEnabled: cfg.AppStoreEnabled}
		},
		FeatureEnabled: a.featureEnabled,
		Logf: func(line string) {
			a.logs.append(line)
		},
		DB: func() *database.DB {
			return a.db
		},
		Cache: &a.appStorePolicy.inner,
	})
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
			a.logs.append("[automation] notificação não aceita: " + strings.TrimSpace(resp.AgentAction))
		}
		return automation.AutomationNotificationResponse{
			Accepted:    resp.Accepted,
			Result:      resp.Result,
			AgentAction: resp.AgentAction,
			Message:     resp.Message,
		}
	})
	a.remoteDebug = remotedebug.New(remotedebug.Deps{
		Logf: a.logs.append,
		GetConfig: func() remotedebug.Config {
			cfg := a.GetDebugConfig()
			return remotedebug.Config{
				AuthToken:    cfg.AuthToken,
				AgentID:      cfg.AgentID,
				NatsServer:   cfg.NatsServer,
				NatsWsServer: cfg.NatsWsServer,
			}
		},
		GetAgentConfig: func() remotedebug.AgentConfig {
			cfg := a.GetAgentConfiguration()
			return remotedebug.AgentConfig{
				ClientID: cfg.ClientID,
				SiteID:   cfg.SiteID,
			}
		},
		SubscribeLogs: a.logs.subscribe,
		ReplayLogs:    a.logs.snapshotAndSubscribe,
	})
	a.remoteSessionMgr = remotesession.NewManager(nil) // NATS sera injetado quando conectado; Fase 1 opera via commandos apenas
	a.remoteSessionMgr.SetCallbacks(
		func(sessionID, kind string) {
			a.activeRemoteSessions.Add(1)
			a.syncRemoteSessionTray()
			a.logs.append(fmt.Sprintf("[remote-session] sessao iniciada: %s (%s) — %d ativas", sessionID, kind, a.activeRemoteSessions.Load()))
		},
		func(sessionID, reason string) {
			a.activeRemoteSessions.Add(-1)
			a.syncRemoteSessionTray()
			a.logs.append(fmt.Sprintf("[remote-session] sessao encerrada: %s (%s) — %d ativas", sessionID, reason, a.activeRemoteSessions.Load()))
		},
	)
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
				NatsServerHostInternal:   cfg.NatsServerHostInternal,
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
			if a.syncSvc != nil {
				a.syncSvc.HandlePing(ping)
			}
		},
		OnGlobalPong:                  a.handleGlobalPong,
		GetHeartbeatMetrics:           a.getHeartbeatMetrics,
		OnP2PDiscoverySnapshot:        a.handleP2PDiscoverySnapshot,
		OnP2PEvent:                    a.handleP2PEvent,
		HandleCommand:                 a.handleAgentRuntimeCommand,
		OnCommandOutput:               a.onAgentCommandOutput,
		OnNatsConnected:               a.onNatsConnected,
		OnConnectivityChange:          a.onConnectivityChange,
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
		HardwareIdentity: func() hardwareid.Info {
			if a.hardwareIDSvc == nil {
				return hardwareid.Info{}
			}
			return a.hardwareIDSvc.Get()
		},
	})
	a.agentConfigSvc = agentconfig.New(agentconfig.FetchDeps{
		GetDebugConfig: a.GetDebugConfig,
	})
	a.ticketsSvc = tickets.New(tickets.Deps{
		GetDebugConfig: a.GetDebugConfig,
	})
	a.syncSvc = syncsvc.NewService(a)
	a.p2pConfig = defaultP2PConfig()
	a.p2pCoord = newP2PCoordinator(a)
	a.chatSvc.Service().SetLogger(func(line string) {
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
		DB:                     nil,
		DebugConfig:            a.GetDebugConfig,
		Version:                Version,
		CommitHash:             buildinfo.Commit,
		ShouldDeferNonCritical: a.nonCriticalBackoffWindow,
		HardwareIdentity: func() hardwareid.Info {
			if a.hardwareIDSvc == nil {
				return hardwareid.Info{}
			}
			return a.hardwareIDSvc.Get()
		},
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
		GetToken:     func() string { return a.GetDebugConfig().AuthToken },
		GetAgentID:   func() string { return a.GetDebugConfig().AgentID },
		GetApiScheme: func() string { return a.GetDebugConfig().ApiScheme },
		GetApiServer: func() string { return a.GetDebugConfig().ApiServer },
		GetPolicy:    func() selfupdate.Policy { return selfupdate.NormalizePolicy(a.GetAgentConfiguration().AgentUpdate) },
		// Downloads unificados no P2P_Temp: tanto P2P quanto HTTP escrevem no mesmo
		// diretório, e o gossip scanner registra automaticamente artifacts com nome
		// canônico (selfupdate-<sha256>.exe) no índice P2P — sem cópia extra.
		TempDir:      a.p2pTempDir(),
		Logf:         func(format string, args ...any) { a.logs.append("[selfupdate] " + fmt.Sprintf(format, args...)) },
		InvalidateCh: a.selfUpdaterCh,
		// InstallerLogPath: caminho para o log do NSIS, usado pelo
		// ResumePendingInstallReport para correlacionar execuções.
		InstallerLogPath: platform.InstallerLogPath(),
		// OnSelfUpdateInstall: PSADT desabilitado para selfupdate.
		// O ShellExecuteEx("runas") em LaunchInstallerElevated já lança
		// o instalador como processo independente (não filho), garantindo
		// que o NSIS sobreviva ao taskkill do agente.
		// PSADT é inadequado aqui: Import-Module demora ~3min e sempre
		// falha com timeout, atrasando o update.
		OnSelfUpdateInstall: nil,
		FindPeersByReleaseID: func(ctx context.Context, artifactID string) ([]string, error) {
			if a.p2pCoord == nil {
				return nil, nil
			}
			result := a.p2pCoord.FindArtifactPeersByReleaseID(artifactID, "")
			return result.PeerAgentIDs, nil
		},
		DownloadFromPeer: func(ctx context.Context, artifactID, peerID string) (string, error) {
			if a.p2pCoord == nil {
				return "", errors.New("p2p indisponível")
			}
			view, err := a.p2pCoord.DownloadArtifactByID(ctx, artifactID, peerID)
			if err != nil {
				return "", err
			}
			return filepath.Join(a.p2pTempDir(), view.ArtifactName), nil
		},
		// OnArtifactReady é chamado apenas para downloads HTTP (P2P já está
		// indexado). Como o arquivo já está no P2P_Temp com nome canônico
		// (selfupdate-<sha256>.exe), o gossip scanner faz o registro automaticamente.
		// Aqui apenas confirmamos no log — sem cópia redundante.
		OnArtifactReady: func(ctx context.Context, path, artifactID, sha256, version string) error {
			if a.p2pCoord == nil || artifactID == "" {
				return nil
			}
			a.logs.append(fmt.Sprintf("[selfupdate] artifact disponivel no P2P: artifactID=%s sha256=%s path=%s",
				artifactID, sha256[:12], filepath.Base(path)))
			return nil
		},
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
			log.Printf("[startup] aviso: falha ao habilitar persistência de logs em arquivo: %v", err)
		} else {
			a.logs.append("[startup] persistência de logs habilitada em " + logPath)
		}
	}
	a.chatSvc.LoadPersistedConfig()
	a.debugSvc.LoadConnectionConfigFromProduction()
	a.initChatLogger()

	mcp.RegisterDiscoveryTools(reg, a)

	a.queuedForceHeartbeat.Store(false)

	if opts.DebugMode {
		a.logs.append("[startup] modo debug ativo por tecla de atalho (execução atual)")
	}

	agentconfig.NormalizePSADTConfigDefaults(&a.agentConfig.PSADT)
	agentconfig.NormalizeRolloutDefaults(&a.agentConfig.Rollout)

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

// ── Wails v3: service lifecycle ──
// ServiceStartup é chamado pelo Wails v3 durante a inicialização da aplicação.
// Substitui o OnStartup do v2. O ctx recebido é o contexto da aplicação.
func (a *App) ServiceStartup(ctx context.Context, options application.ServiceOptions) error {
	a.startup(ctx)
	return nil
}

// ServiceShutdown é chamado pelo Wails v3 durante o encerramento.
func (a *App) ServiceShutdown() error {
	a.shutdown()
	return nil
}

// SetApplication guarda a referência da aplicação Wails v3.
//
//wails:ignore
func (a *App) SetApplication(app *application.App) {
	a.app = app
}

// SetMainWindow guarda a referência da janela principal.
//
//wails:ignore
func (a *App) SetMainWindow(window application.Window) {
	a.mainWindow = window
}

// ShowMainWindow restaura e mostra a janela principal (usado no single-instance).
//
//wails:ignore
func (a *App) ShowMainWindow() {
	if a.mainWindow == nil {
		return
	}
	a.mainWindow.UnMinimise()
	a.mainWindow.Show()
	a.mainWindow.SetAlwaysOnTop(true)
	a.mainWindow.SetAlwaysOnTop(false)
}

// EmitEvent emite um evento customizado para o frontend (v3).
//
//wails:ignore
func (a *App) EmitEvent(name string, data ...any) {
	if a.app == nil {
		return
	}
	a.app.Event.Emit(name, data...)
}

// HideMainWindow esconde a janela principal (close-to-tray).
//
//wails:ignore
func (a *App) HideMainWindow() {
	if a.mainWindow == nil {
		return
	}
	a.mainWindow.Hide()
}

// MinimiseMainWindow minimiza a janela principal.
//
//wails:ignore
func (a *App) MinimiseMainWindow() {
	if a.mainWindow == nil {
		return
	}
	a.mainWindow.Minimise()
}

// QuitApp encerra a aplicação (v3).
//
//wails:ignore
func (a *App) QuitApp() {
	if a.quitRequested.Swap(true) {
		return
	}
	if a.app == nil {
		return
	}
	a.app.Quit()
}

func (a *App) GetAgentConfiguration() agentconfig.AgentConfiguration {
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

const defaultHeartbeatIntervalSeconds = 15
const minHeartbeatIntervalSeconds = 10

func heartbeatIntervalFromAgentConfig(agentCfg agentconfig.AgentConfiguration) int {
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
		Hostname:              hostname,
		CpuPercent:            -1,
		MemoryPercent:         -1,
		DiskPercent:           -1,
		DiskReadPercent:       -1,
		DiskWritePercent:      -1,
		DiskResponseMs:        -1,
		CpuTemperatureCelsius: -1,
		UptimeSeconds:         int64(time.Since(a.startupTime).Seconds()),
		P2pPeers:              a.getKnownP2PPeers(),
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
		metrics.PeerID, metrics.Addrs, metrics.Port = a.p2pCoord.GetP2PAddressingInfo()
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
	if src.CpuTemperatureCelsius >= 0 {
		dst.CpuTemperatureCelsius = src.CpuTemperatureCelsius
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

// onNatsConnected é chamado pelo agentconn após a conexão NATS ser estabelecida.
// Injeta a conexão NATS no remoteSessionMgr para habilitar o streaming de frames.
func (a *App) onNatsConnected(nc *nats.Conn, cfg agentconn.Config) {
	if a.remoteSessionMgr == nil {
		return
	}
	a.remoteSessionMgr.SetNatsConn(nc, cfg.ClientID, cfg.SiteID, cfg.AgentID)
	a.logs.append(fmt.Sprintf("[remote-session] NATS conectado — streaming habilitado (tenant=%s, site=%s, agent=%s)",
		cfg.ClientID, cfg.SiteID, cfg.AgentID))
}

// onConnectivityChange é chamado pelo agentconn quando o estado online/offline
// muda. Atualiza o tray imediatamente e emite um evento para o frontend, para
// que a bolinha da barra, a página de Status e a consulta de versão reajam na
// hora — sem depender do polling.
func (a *App) onConnectivityChange(connected bool, transport string) {
	state := "offline"
	if connected {
		state = "online"
	}
	a.logs.append(fmt.Sprintf("[connectivity] mudanca de estado para %s (transport=%s)", state, transport))
	a.EmitEvent("agent:connectivity", map[string]any{
		"connected": connected,
		"transport": transport,
	})
	// Atualiza o ícone e o tooltip do tray imediatamente (safeTrayAction cobre
	// o caso de o tray ainda não ter sido iniciado).
	a.syncTrayVisualState()
	a.updateTrayTooltip()
	a.updateTrayMenu()
}

func (a *App) startup(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	a.ctx = ctx
	a.cancel = cancel

	captureStdLog(&a.logs)

	// Diagnóstico de elevação/integridade — importante para o controle remoto
	// (SendInput via UIPI) e gerenciamento de serviços (SCM). Colocado após
	// captureStdLog para que o log seja capturado no buffer da UI/support.
	// Com o manifest requireAdministrator, espera-se elevated=true integrity=High.
	if ev := platform.ElevationReport(); ev != "" {
		log.Printf("[startup] elevação: %s", ev)
	}

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

	// O servidor SSE dedicado de chat é SEMPRE iniciado (mesmo fora do modo
	// debug) para que o webview nativo possa receber eventos de streaming
	// de forma confiável via SSE quando a entrega nativa do Wails v3 falha.
	if err := a.EnsureChatSSEServer(); err != nil {
		log.Printf("[chat-sse] falha ao iniciar servidor SSE dedicado: %v", err)
	} else if port := a.GetChatSSEPort(); port > 0 {
		log.Printf("[chat-sse] servidor SSE dedicado ativo em http://127.0.0.1:%d/api/chat-events", port)
	} else {
		log.Printf("[chat-sse] AVISO: servidor SSE dedicado retornou porta 0 — chat nativo pode falhar!")
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
			// Inicializa o ciclo de vida do inventory.Service após o DB estar disponível.
			_ = a.inventorySvc.Startup(ctx)
		}
		agentIDForEngine := strings.TrimSpace(a.GetDebugConfig().AgentID)
		a.consolEngine = consolidation.New(db, agentIDForEngine)
	}

	log.Println("[startup] runtime local (tray) ativo — todos os workers locais iniciados")

	// ── Phase 1: Staged startup ────────────────────────────────────────
	// To avoid saturating modest CPUs, heavyweight operations are staggered
	// instead of launching all goroutines simultaneously.
	//   Phase 0 (immediate):  tray, DB, debug HTTP, P2P telemetry
	//   Phase 1 (+2s):        inventory collection (osqueryi) + sync
	//   Phase 2 (+8s):        agentConn bootstrap + heartbeat
	//   Phase 3 (+10s):       automation, syncSvc, P2P bootstrap
	//   Phase 4 (+12s):       self-update, cleanup ticker
	// ────────────────────────────────────────────────────────────────────

	const (
		startupPhaseInventory   = 2 * time.Second
		startupPhaseAgentConn   = 8 * time.Second
		startupPhaseBackground  = 10 * time.Second
		startupPhaseMaintenance = 12 * time.Second
	)

	// Phase 1: Inventory collection (heaviest operation — delayed 2s).
	a.startupWg.Add(1)
	a.safeGo(func() {
		defer a.startupWg.Done()

		select {
		case <-ctx.Done():
			return
		case <-time.After(startupPhaseInventory):
		}

		if !a.isInventoryProvisioned() {
			log.Println("[startup] inventory-startup: ignorado (agente não provisionado)")
			return
		}

		done := a.beginActivity("inventario inicial")
		defer done()

		a.ensureOsqueryInstalled()

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

	// Phase 2: Agent connection (bootstrap + heartbeat).
	a.startupWg.Add(1)
	a.safeGo(func() {
		defer a.startupWg.Done()

		select {
		case <-ctx.Done():
			return
		case <-time.After(startupPhaseAgentConn):
		}

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

		a.agentConn.Run(ctx)
	})

	// Phase 3: Automation, sync coordinator, P2P bootstrap.
	a.startupWg.Add(1)
	a.safeGo(func() {
		defer a.startupWg.Done()

		select {
		case <-ctx.Done():
			return
		case <-time.After(startupPhaseBackground):
		}

		// Decommission outbox drainer (runs periodically forever).
		a.safeGo(func() {
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

		if a.automationSvc != nil {
			a.safeGo(func() {
				a.automationSvc.Run(ctx, func() {})
			})
		}

		if a.syncSvc != nil {
			// Inicializa o ciclo de vida do sync.Service antes de iniciar o loop.
			_ = a.syncSvc.Startup(ctx)
			a.safeGo(func() {
				a.syncSvc.Run(ctx)
			})
		}

		if a.p2pCoord != nil {
			// Inicializa o ciclo de vida do p2p.Coordinator antes de iniciar o loop.
			_ = a.p2pCoord.Startup(ctx)
			if !isAgentConfigured() && a.zeroTouchConfigRegistrationAllowed() {
				a.safeGo(func() {
					a.RunOnboardingLoop(ctx)
				})
			}
			a.safeGo(func() {
				a.p2pCoord.Run(ctx)
			})
		}
	})

	// Phase 4: Self-updater + DB cleanup ticker (lowest priority).
	a.startupWg.Add(1)
	a.safeGo(func() {
		defer a.startupWg.Done()

		select {
		case <-ctx.Done():
			return
		case <-time.After(startupPhaseMaintenance):
		}

		if a.selfUpdater != nil {
			a.safeGo(func() {
				a.selfUpdater.ResumePendingInstallReport(a.ctx)
				a.selfUpdater.Run(a.ctx, 0)
			})
		}

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

	// Periodic inventory collection (6h loop — after throttle window expires).
	a.safeGo(func() {
		const periodicInventoryInterval = 6 * time.Hour
		// Wait for throttle window (120s) + buffer (60s) to avoid
		// double-collecting during the initial startup window.
		const initialDelay = 180 * time.Second

		select {
		case <-ctx.Done():
			return
		case <-time.After(initialDelay):
		}

		log.Printf("[inventory] iniciando loop de coleta periodica a cada %s", periodicInventoryInterval)
		ticker := time.NewTicker(periodicInventoryInterval)
		defer ticker.Stop()

		// Fire immediately after initial delay.
		a.runPeriodicInventorySync(ctx)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.runPeriodicInventorySync(ctx)
			}
		}
	})

	// Apply startup throttle config from agent configuration (if already loaded).
	a.applyStartupThrottleConfig()
}

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
	a.invCache.set(report)

	if a.inventorySvc != nil {
		a.logs.append("[inventory] coleta periodica concluida; sincronizando com servidor")
		a.inventorySvc.SyncInventoryOnStartup(ctx, report)
	}
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
	a.logs.append("[heartbeat][manual] falha ao enviar heartbeat manual: timeout ou nenhuma conexão ativa")
	return "falha ao enviar heartbeat manual: timeout ou nenhuma conexão ativa"
}

func (a *App) startupLogf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	log.Print(line)
	a.logs.append(line)
}

func (a *App) safeGo(fn func()) {
	safego.Go(fn, func(line string) {
		a.logs.append(line)
	})
}

func (a *App) isInventoryProvisioned() bool {
	if a == nil {
		return false
	}
	return a.GetDebugConfig().IsProvisioned()
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
		a.logs.append("[startup] post-bootstrap: timeout aguardando provisionamento")
		return nil
	}

	a.logs.append("[startup] post-bootstrap: agente provisionado — reconciliando recursos")

	// 1. osquery + inventário inicial (não executou no goroutine #2 porque
	//    ainda não estava provisionado).
	a.ensureOsqueryInstalled()
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
	if a.syncSvc != nil {
		_ = a.refreshAgentConfiguration(ctx)
		a.syncSvc.ReconcileFromManifest(ctx, "post-bootstrap")
	}

	// 3. Automação — carrega políticas iniciais.
	if a.automationSvc != nil {
		if _, err := a.automationSvc.RefreshPolicy(ctx, false); err != nil {
			a.logs.append("[startup] post-bootstrap: falha ao carregar politicas de automacao: " + err.Error())
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
			a.logs.append("[startup] post-bootstrap: falha ao carregar app-store: " + err.Error())
		}
		if a.supportSvc != nil && a.featureEnabled(a.GetAgentConfiguration().KnowledgeBaseEnabled) {
			if err := a.supportSvc.RefreshKnowledgeBase(); err != nil {
				a.logs.append("[startup] post-bootstrap: falha ao atualizar knowledge base: " + err.Error())
			}
		}
		// Registra tools MCP do agent na API para o fluxo multi-round do chat
		if err := a.RegisterAgentToolsOnServer(); err != nil {
			a.logs.append("[startup] post-bootstrap: falha ao registrar agent tools: " + err.Error())
		}
	})

	a.logs.append("[startup] post-bootstrap: reconciliacao concluida")
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
	if a.appsSvc == nil {
		return
	}

	// Apenas registra o status sem instalar.
	status := inventory.GetOsqueryStatus()
	if status.Installed {
		a.startupLogf("[startup] osquery presente (fallback opcional): %s", status.Path)
	}
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
				a.MinimiseMainWindow()
				a.HideMainWindow()
				log.Println("[startup] janela iniciada minimizada no tray")
				return
			}
		}
	}()
}

func (a *App) shutdown() {
	a.applyIdleMode(false)

	a.StopDebugHTTPServer()
	a.StopChatSSEServer()

	// NOTA: remoteDebug e remoteSessionMgr agora são Services Wails v3
	// separados (adapters thin em remote_services.go). Seus ciclos de vida
	// (Startup/Shutdown) são gerenciados pelo Wails, que encerra os services
	// na ordem inversa de registro — ou seja, antes do App. Por isso não são
	// encerrados aqui.

	// Desliga o domínio Sync (cancela contexto, aguarda goroutines).
	if a.syncSvc != nil {
		_ = a.syncSvc.Shutdown()
	}

	// Desliga o domínio P2P (cancela contexto do Coordinator).
	if a.p2pCoord != nil {
		_ = a.p2pCoord.Shutdown()
	}

	// Desliga o domínio Inventory (para timer de refresh pós-instalação).
	if a.inventorySvc != nil {
		_ = a.inventorySvc.Shutdown()
	}

	if a.cancel != nil {
		a.cancel()
	}

	// Aguarda as goroutines de startup com timeout. Quando o agente está
	// offline, o loop de reconexão do agentconn pode ficar preso numa
	// chamada de conexão (nats.Connect com timeout de 5s) que não respeita
	// o cancelamento do contexto. Sem limite, o shutdown e o "Sair" do tray
	// travariam permanentemente — dando a impressão de que o agente "não
	// fecha e não abre mais". Um timeout razoável garante que o processo
	// sempre encerre, mesmo que alguma goroutine não responda a tempo.
	startupDone := make(chan struct{})
	go func() {
		a.startupWg.Wait()
		close(startupDone)
	}()
	select {
	case <-startupDone:
	case <-time.After(8 * time.Second):
		log.Printf("[shutdown] timeout aguardando goroutines de startup; forçando encerramento")
	}

	if a.db != nil {
		if err := a.db.Close(); err != nil {
			log.Printf("[shutdown] erro ao fechar database: %v", err)
		}
	}
	a.logs.closeFile()
}

// cancelDeferredRestart cancela qualquer restart adiado pendente.
func (a *App) cancelDeferredRestart() {
	if a.deferredRestart == nil {
		return
	}
	a.deferredRestart.mu.Lock()
	defer a.deferredRestart.mu.Unlock()
	if a.deferredRestart.timer != nil {
		a.deferredRestart.timer.Stop()
		a.deferredRestart.timer = nil
	}
	a.deferredRestart.deferCount = 0
}

// scheduleDeferredRestart agenda a re-exibição do prompt de restart após
// deferMinutes. Se maxDefers for atingido, força o restart imediatamente.
func (a *App) scheduleDeferredRestart(action string, pp powerCommandPayload) {
	if a.deferredRestart == nil {
		a.deferredRestart = &deferredRestartState{
			maxDefers:    3,
			deferMinutes: 60,
			message:      pp.Message,
		}
	}
	ds := a.deferredRestart
	ds.mu.Lock()

	if pp.MaxDefers > 0 {
		ds.maxDefers = pp.MaxDefers
	}
	if pp.DeferMinutes > 0 {
		ds.deferMinutes = pp.DeferMinutes
	}
	if pp.Message != "" {
		ds.message = pp.Message
	}

	ds.deferCount++

	if ds.deferCount >= ds.maxDefers {
		ds.mu.Unlock()
		a.logs.append(fmt.Sprintf("[agent] %s-defer [FORCE] maxDefers=%d atingido — restart forçado", action, ds.maxDefers))
		go func() {
			a.executeSystemPowerAction(context.Background(), action, 0, true, ds.message)
		}()
		return
	}

	delaySeconds := pp.DelaySeconds
	if delaySeconds <= 0 {
		delaySeconds = 300
	}
	deferMinutes := ds.deferMinutes
	msg := ds.message

	// Cancela timer anterior se existir
	if ds.timer != nil {
		ds.timer.Stop()
	}

	ds.timer = time.AfterFunc(time.Duration(deferMinutes)*time.Minute, func() {
		a.logs.append(fmt.Sprintf("[agent] %s-defer [RETRY] defer=%d/%d — re-exibindo prompt", action, ds.deferCount, ds.maxDefers))
		result := a.showDeferrableRestartPrompt(action, delaySeconds, msg, deferMinutes)
		if result == "restart_now" || result == "fallback" {
			a.executeSystemPowerAction(context.Background(), action, delaySeconds, false, msg)
		} else {
			// "defer" — re-agenda
			a.scheduleDeferredRestart(action, pp)
		}
	})

	ds.mu.Unlock()

	a.logs.append(fmt.Sprintf("[agent] %s-defer [SCHED] adiado para daqui %dmin (defer=%d/%d)", action, deferMinutes, ds.deferCount, ds.maxDefers))
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
		"user_message": "Runtime local ativo (tray icon no logon).",
	}
}
