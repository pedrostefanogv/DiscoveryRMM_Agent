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
	"discovery/app/coreagent"
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

// WebView2UserDataPath retorna o diretório de dados do WebView2 da UI.
// Fixo em %ProgramData%\Discovery\WebView2 (Windows) para evitar o erro
// "Microsoft Edge WebView2 não pode ler e gravar o diretório de dados":
// o default do Wails (%APPDATA%\<exe>\EBWebView) quebra quando a UI roda
// elevada/SYSTEM (Task Scheduler, restart pós-update), pois %APPDATA%
// resolve para o systemprofile. ProgramData é gravável em qualquer contexto.
func WebView2UserDataPath() string {
	if runtime.GOOS != "windows" {
		return ""
	}
	return filepath.Join(platform.DataDir(), "WebView2")
}

type App struct {
	// coreagent.CoreAgent embutido (migração física lote 1 — Fase A):
	// serviços de domínio/infra do core com tipos em pacotes externos.
	// A promoção de campos mantém `a.AgentConn`, `a.NotificationSvc`, etc.
	// funcionando — refactor mecânico sem mudança de comportamento. O acesso
	// explícito ao embed é `a.CoreAgent`; `Core` é um alias curto para o
	// mesmo struct (usado onde um método/promoted field colide, ex. DB()).
	// Alias declarado após o struct: ver func (a *App) core() abaixo — não,
	// Go não tem alias de embed; o acesso é via a.CoreAgent.<Campo>.
	coreagent.CoreAgent

	ctx                  context.Context
	cancel               context.CancelFunc
	packageManagerRouter *automationPackageManagerRouter

	mcpRegistry *mcp.Registry
	chatSvc     *chat.Service
	psadtSvc    *psadt.Service

	// toolsRegistration guarda o timestamp do último registro bem-sucedido de tools.
	// Usado para re-registrar se o cache do servidor expirou (TTL 5min por padrão no servidor).
	toolsRegistrationMu   sync.RWMutex
	lastToolsRegistration time.Time

	debugHTTP  *debughttp.Server
	chatSSE    *debughttp.Server
	chatEvents *debughttp.ChatEventBroker

	closeMu                  sync.RWMutex
	allowClose               bool
	trayReady                atomic.Bool
	trayIconState            atomic.Int32
	trayIcon                 []byte
	trayProvisioning         []byte
	trayOffline              []byte
	activeRemoteSessions     atomic.Int32
	zeroTouchAttemptInFlight atomic.Bool
	zeroTouchApprovalPending atomic.Bool

	// Companion mode: último estado de onboarding recebido do serviço via
	// snapshot IPC (agent:status_snapshot). O sync (zero-touch) roda no
	// SERVIÇO — a flag zeroTouchApprovalPending local da UI nunca muda no
	// modo companion, e GetOnboardingStatus responde 'normal' mesmo quando o
	// agente está provisionado aguardando aprovação. Com o snapshot, a UI
	// reflete o estado real (e a overlay sai quando a aprovação chega).
	companionOnboardingMu sync.RWMutex
	companionOnboarding   map[string]interface{}

	startupTime time.Time

	// deferredRestart fica no App (não core): estado do power-command de
	// restart adiado — usa campos minúsculos intensivamente e é acionado
	// pela sessão de UI (powerCommandPayload). Revisão da migração lote 2.
	deferredRestart *deferredRestartState

	// M4: fases de startup em andamento — o shutdown loga quantas ficaram
	// pendentes ao fechar o SQLite após o timeout.
	startupPhasePending atomic.Int32

	// ── Wails v3 ──
	// Referências explícitas à aplicação/janela do Wails v3.
	// Substituem o acesso implícito via ctx do v2.
	app        *application.App
	mainWindow application.Window
	systemTray *application.SystemTray

	// ── IPC serviço ↔ UI (PLANO_AGENT_SERVICE_SYSTEM.md, Fase 2) ──
	// No serviço: ipcServer distribui eventos para as UIs conectadas.
	// Na UI: ipcClient conecta ao serviço (modo companion) com reconexão.
	ipcServer *IPCServer
	ipcClient *IPCClient

	// Último snapshot de conectividade recebido do serviço via IPC
	// (agente:status_snapshot). Em companion mode o agentConn local não roda,
	// então o tray/status usam este snapshot para refletir o estado real do
	// core que roda no serviço.
	companionStatusMu sync.Mutex
	companionStatus   *AgentStatus

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
		mcpRegistry:      reg,
		chatEvents:       debughttp.NewChatEventBroker(),
		startupTime:      time.Now(),
		trayIcon:         opts.TrayIcon,
		trayProvisioning: opts.TrayProvisioningIcon,
		trayOffline:      opts.TrayOfflineIcon,
	}
	a.RuntimeFlags = coreagent.RuntimeFlags{DebugMode: opts.DebugMode, ServiceMode: opts.ServiceMode}
	// Campos do core (migração lote 1 — embed coreagent.CoreAgent).
	a.CoreAgent.UpdateTrigger = make(chan struct{}, 1)
	a.CoreAgent.CatalogSvc = services.NewCatalogService(catalogClient)
	a.CoreAgent.CatalogClient = catalogClient
	a.CoreAgent.AppsSvc = services.NewAppsService(wingetClient, chocolateyClient)
	a.CoreAgent.InvSvc = services.NewInventoryService(inventoryProvider)
	a.CoreAgent.PrinterSvc = services.NewPrinterService(printerManager)
	a.Logs.Buffer = logs.New()
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
			a.Logs.Append(line)
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
			a.Logs.Append(line)
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
			return a.RuntimeFlags.DebugMode
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
	a.HardwareIDSvc = hardwareid.New(hardwareid.Deps{
		Logf: func(line string) {
			a.Logs.Append(line)
		},
	})
	a.MemorySvc = memory.New(memory.Deps{
		DB: func() *database.DB { return a.CoreAgent.DB },
	})
	a.NotificationSvc = notifications.New(notifications.Deps{
		Logf: func(line string) {
			a.Logs.Append(line)
		},
		// Ctx reflete a presença de uma UI para renderizar notificações
		// (revisão 2 — bug B4): no serviço, a.ctx nunca é nil, então o
		// caminho headless (toast nativo) nunca dispararia. Retorna nil
		// quando não há UI Wails nem UI companion conectada via IPC —
		// nesses casos o Dispatch cai no fallback nativo (Fase C2/D4).
		Ctx: func() interface{ Done() <-chan struct{} } {
			if a.RuntimeFlags.ServiceMode {
				if a.ipcServer != nil && a.ipcServer.ClientCount() > 0 {
					return a.ctx
				}
				return nil
			}
			return a.ctx
		},
		DB:        func() *database.DB { return a.CoreAgent.DB },
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
		// Toast nativo do Windows quando o serviço não tem UI conectada
		// (PLANO_SEPARACAO_SERVICO_UI.md, Fase C2 — decisão D4). No modo UI
		// (standalone/companion) o fallback fica nil: a UI renderiza via Wails.
		NativeFallback: func(req notifications.DispatchRequest) {
			if a.RuntimeFlags.ServiceMode {
				dispatchNativeToastWhenHeadless(req)
			}
		},
	})
	a.ApiClientSvc = apiclient.New(apiclient.Deps{
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
			a.Logs.Append(line)
		},
	})
	a.CustomFieldsSvc = customfields.New(customfields.Deps{
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
	a.AppStoreSvc = appstore.New(appstore.Deps{
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
			a.Logs.Append(line)
		},
		DB:    func() *database.DB { return a.CoreAgent.DB },
		Cache: a.AppStorePolicy.CachePointer(),
	})
	a.AutomationSvc = automation.NewService(func() automation.RuntimeConfig {
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
		a.Logs.Append("[automation] " + line)
	})
	a.packageManagerRouter = newAutomationPackageManagerRouter(a, a.AppsSvc)
	a.AutomationSvc.SetPackageManager(a.packageManagerRouter)
	// Warmup P2P (uma vez por processo): atrasa o primeiro policy-sync (e os
	// triggers immediate/checkin de startup) até o discovery inicial do P2P
	// concluir OU o teto de 120s — evita que tasks de instalação baixem da
	// internet enquanto os peers da LAN ainda estão sendo descobertos.
	a.AutomationSvc.SetStartupReadinessWaiter(func(ctx context.Context) {
		if a.P2PCoord == nil {
			return
		}
		cfg := a.GetP2PConfig()
		if !cfg.Enabled {
			return
		}
		readyCh := a.P2PCoord.ReadyCh()
		select {
		case <-readyCh:
			return
		default:
		}
		a.Logs.Append("[automation] aguardando discovery inicial do P2P antes do primeiro policy-sync (teto 120s)")
		timer := time.NewTimer(120 * time.Second)
		defer timer.Stop()
		select {
		case <-readyCh:
			a.Logs.Append("[automation] discovery P2P concluído, prosseguindo com policy-sync")
		case <-timer.C:
			a.Logs.Append("[automation] teto de 120s aguardando discovery P2P atingido, prosseguindo com policy-sync")
		case <-ctx.Done():
		}
	})
	// Resolvedor de versão P2P para a decisão versionada do executor (anti-loop).
	automation.SetP2PVersionResolver(func(packageID string) string {
		return a.packageManagerRouter.resolveP2PPackageVersion(packageID)
	})
	a.AutomationSvc.SetPackageAuthorization(func(ctx context.Context, installationType automation.AppInstallationType, packageID, operation string) error {
		return a.authorizeAutomationPackage(ctx, string(installationType), packageID, operation)
	})
	a.AutomationSvc.SetPSADTPolicyResolver(func() automation.PSADTPolicy {
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
	a.AutomationSvc.SetNotificationDispatcher(func(req automation.AutomationNotificationRequest) automation.AutomationNotificationResponse {
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
			a.Logs.Append("[automation] notificação não aceita: " + strings.TrimSpace(resp.AgentAction))
		}
		return automation.AutomationNotificationResponse{
			Accepted:    resp.Accepted,
			Result:      resp.Result,
			AgentAction: resp.AgentAction,
			Message:     resp.Message,
		}
	})
	a.RemoteDebug = remotedebug.New(remotedebug.Deps{
		Logf: a.Logs.Append,
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
		SubscribeLogs: a.Logs.Subscribe,
		ReplayLogs:    a.Logs.SnapshotAndSubscribe,
	})
	a.RemoteSessionMgr = remotesession.NewManager(nil) // NATS sera injetado quando conectado; Fase 1 opera via commandos apenas
	a.RemoteSessionMgr.SetCallbacks(
		func(sessionID, kind string) {
			a.activeRemoteSessions.Add(1)
			a.syncRemoteSessionTray()
			a.Logs.Append(fmt.Sprintf("[remote-session] sessao iniciada: %s (%s) — %d ativas", sessionID, kind, a.activeRemoteSessions.Load()))
		},
		func(sessionID, reason string) {
			a.activeRemoteSessions.Add(-1)
			a.syncRemoteSessionTray()
			a.Logs.Append(fmt.Sprintf("[remote-session] sessao encerrada: %s (%s) — %d ativas", sessionID, reason, a.activeRemoteSessions.Load()))
		},
	)
	inventoryProvider.SetProgressCallback(func() {
		a.pulseInventoryHeartbeat()
	})
	a.AgentConn = agentconn.NewRuntime(agentconn.Options{
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
			a.Logs.Append("[agent] " + fmt.Sprintf(format, args...))
		},
		OnSyncPing: func(ping agentconn.SyncPing) {
			if a.SyncSvc != nil {
				a.SyncSvc.HandlePing(ping)
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
	a.DebugSvc = debug.NewService(debug.Options{
		Logf: func(line string) {
			a.Logs.Append(line)
		},
		AgentConn:          a.AgentConn,
		AgentInfo:          &a.AgentInfo,
		DB:                 a.CoreAgent.DB,
		NormalizeP2PConfig: normalizeP2PConfig,
		ApplyP2PConfig:     a.applyP2PConfig,
		DefaultP2PConfig:   defaultP2PConfig,
		Version:            Version,
		// Propaga mudanças de config de debug para as UIs companion (serviço →
		// IPC "debug:config_updated") e para o frontend da própria UI quando o
		// DebugSvc roda no processo da UI. Sem isso a cópia em memória da UI
		// companion ficava stale ("só resolve fechando e abrindo o agent/ui").
		OnConfigChanged: func(cfg debug.Config) {
			a.EmitEvent("debug:config_updated", "config", cfg)
		},
		HardwareIdentity: func() hardwareid.Info {
			if a.HardwareIDSvc == nil {
				return hardwareid.Info{}
			}
			return a.HardwareIDSvc.Get()
		},
	})
	a.AgentConfigSvc = agentconfig.New(agentconfig.FetchDeps{
		GetDebugConfig: a.GetDebugConfig,
	})
	a.TicketsSvc = tickets.New(tickets.Deps{
		GetDebugConfig: a.GetDebugConfig,
	})
	a.SyncSvc = syncsvc.NewService(a)
	a.P2PConfig = defaultP2PConfig()
	a.P2PCoord = newP2PCoordinator(a)
	a.chatSvc.Service().SetLogger(func(line string) {
		a.Logs.Append("[chat] " + line)
	})
	a.InventorySvc = appinventory.NewService(appinventory.Options{
		Apps:           a.packageManagerRouter,
		Inventory:      a.InvSvc,
		Cache:          &a.InvCache,
		ResolveAllowed: a.resolveAllowedPackage,
		ResolveAllowedByType: func(ctx context.Context, installationType, packageID string) (appstore.Item, error) {
			return a.findAllowedPackage(ctx, installationType, packageID)
		},
		GetCatalog:        a.getCatalogFromAppStore,
		PendingUpdates:    a.pendingUpdatesForInventory,
		InstalledPackages: a.installedPackagesForInventory,
		BeginActivity:     a.beginActivity,
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
		Logf: a.Logs.Append,
		Ctx: func() context.Context {
			return a.ctx
		},
		DB:          nil,
		DebugConfig: a.GetDebugConfig,
		// Fonte ÚNICA de versão/commit para relato ao servidor: buildinfo
		// (injetado por ldflags em TODOS os binários oficiais, incluindo o
		// discovery-service). app.Version só é injetado no binário de UI —
		// usá-lo aqui fazia o serviço reportar "dev" no hardware report em
		// builds oficiais. CommitForReport cai para o VCS stamping em builds
		// locais sem ldflags e devolve "" (não "unknown") quando indisponível.
		Version:                strings.TrimSpace(buildinfo.Version),
		CommitHash:             buildinfo.CommitForReport(),
		ShouldDeferNonCritical: a.nonCriticalBackoffWindow,
		HardwareIdentity: func() hardwareid.Info {
			if a.HardwareIDSvc == nil {
				return hardwareid.Info{}
			}
			return a.HardwareIDSvc.Get()
		},
	})
	a.SupportSvc = appsupport.NewService(appsupport.Options{
		Logf:        a.Logs.Append,
		Ctx:         func() context.Context { return a.ctx },
		DB:          a.CoreAgent.DB,
		AgentInfo:   &a.AgentInfo,
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
	a.UpdatesSvc = updates.NewService(updates.Options{
		Apps:          a.AppsSvc,
		BeginActivity: a.beginActivity,
		Logf:          a.Logs.Append,
		Ctx: func() context.Context {
			return a.ctx
		},
	})
	a.SelfUpdaterCh = make(chan bool, 4)
	a.SelfUpdater = &selfupdate.Updater{
		GetToken:     func() string { return a.GetDebugConfig().AuthToken },
		GetAgentID:   func() string { return a.GetDebugConfig().AgentID },
		GetApiScheme: func() string { return a.GetDebugConfig().ApiScheme },
		GetApiServer: func() string { return a.GetDebugConfig().ApiServer },
		GetPolicy:    func() selfupdate.Policy { return selfupdate.NormalizePolicy(a.GetAgentConfiguration().AgentUpdate) },
		// Downloads unificados no P2P_Temp: tanto P2P quanto HTTP escrevem no mesmo
		// diretório, e o gossip scanner registra automaticamente artifacts com nome
		// canônico (selfupdate-<sha256>.exe) no índice P2P — sem cópia extra.
		TempDir:      a.p2pTempDir(),
		Logf:         func(format string, args ...any) { a.Logs.Append("[selfupdate] " + fmt.Sprintf(format, args...)) },
		InvalidateCh: a.SelfUpdaterCh,
		// InstallerLogPath: caminho para o log do NSIS, usado pelo
		// ResumePendingInstallReport para correlacionar execuções.
		InstallerLogPath: platform.InstallerLogPath(),
		// CanInstallNow: só permite lançar o instalador quando a janela do
		// agente NÃO está visível em tela (minimizada ou oculta no tray).
		// Evita fechar/reabrir o agente enquanto o usuário o está usando.
		// No modo serviço não há janela — sempre pode instalar (Fase A:
		// core não consulta a UI para decidir update).
		CanInstallNow: func() bool {
			if a.RuntimeFlags.ServiceMode {
				return true
			}
			if a.mainWindow == nil {
				return true
			}
			return !a.mainWindow.IsVisible() || a.mainWindow.IsMinimised()
		},
		// OnSelfUpdateInstall: PSADT desabilitado para selfupdate.
		// O ShellExecuteEx("runas") em LaunchInstallerElevated já lança
		// o instalador como processo independente (não filho), garantindo
		// que o NSIS sobreviva ao taskkill do agente.
		// PSADT é inadequado aqui: Import-Module demora ~3min e sempre
		// falha com timeout, atrasando o update.
		OnSelfUpdateInstall: nil,
		FindPeersByReleaseID: func(ctx context.Context, artifactID string) ([]string, error) {
			if a.P2PCoord == nil {
				return nil, nil
			}
			// expectedSHA256 extraído do artifactID "selfupdate:<sha256>" para
			// validação cross-peer (rejeita peers com artifact stale).
			expectedSHA := ""
			if sha, ok := strings.CutPrefix(artifactID, "selfupdate:"); ok {
				expectedSHA = strings.ToLower(strings.TrimSpace(sha))
			}
			result := a.P2PCoord.FindArtifactPeersByReleaseID(artifactID, expectedSHA)
			return result.PeerAgentIDs, nil
		},
		DownloadFromPeer: func(ctx context.Context, artifactID, peerID string) (string, error) {
			if a.P2PCoord == nil {
				return "", errors.New("p2p indisponível")
			}
			// Swarm download quando >1 peer tem o artifact (chunks de múltiplos
			// peers); peer único cai no download direto do peer informado.
			avail := a.P2PCoord.FindArtifactPeersByReleaseID(artifactID, "")
			if avail.PeerCount > 1 {
				view, err := a.P2PCoord.DownloadArtifactByIDSwarm(ctx, artifactID)
				if err != nil {
					// Swarm falhou — tenta peer único como fallback.
					view, err = a.P2PCoord.DownloadArtifactByID(ctx, artifactID, peerID)
				}
				if err != nil {
					return "", err
				}
				return filepath.Join(a.p2pTempDir(), view.ArtifactName), nil
			}
			view, err := a.P2PCoord.DownloadArtifactByID(ctx, artifactID, peerID)
			if err != nil {
				return "", err
			}
			return filepath.Join(a.p2pTempDir(), view.ArtifactName), nil
		},
		// OnArtifactReady é chamado apenas para downloads HTTP (P2P já está
		// indexado). O arquivo já está no P2P_Temp com nome canônico
		// (selfupdate-<sha256>.exe); aqui registramos o artifactID canônico
		// ("selfupdate:<sha256>") via sidecar .meta para que o gossip anuncie
		// o ID que outros agentes procuram — sem recopiar o arquivo.
		OnArtifactReady: func(ctx context.Context, path, artifactID, sha256, version string) error {
			if a.P2PCoord == nil || artifactID == "" {
				return nil
			}
			if _, err := a.P2PCoord.RegisterArtifactIDForFile(path, artifactID); err != nil {
				a.Logs.Append(fmt.Sprintf("[selfupdate] aviso: falha ao registrar artifactID no P2P: %v", err))
				return err
			}
			a.Logs.Append(fmt.Sprintf("[selfupdate] artifact disponivel no P2P: artifactID=%s sha256=%s path=%s",
				artifactID, sha256[:12], filepath.Base(path)))
			return nil
		},
	}
	a.Exporter = updates.NewExporter(updates.ExportOptions{
		BeginActivity: a.beginActivity,
		Inventory: func() (models.InventoryReport, error) {
			return a.getInventoryForExport()
		},
		GetRedact: a.getRedact,
		SetRedact: a.ExportCfg.Set,
	})
	// Persistência de logs: serviço usa agent-service.log (Fase 0.2 do plano);
	// UI usa agent.log padrão. O log file do modo serviço também é configurado
	// via logger.SetFileOutput em RunServiceMode (para o stdlib log).
	logPath := platform.LogFilePath()
	if a.RuntimeFlags.ServiceMode {
		logPath = platform.ServiceLogFilePath()
	}
	if logPath != "" {
		if err := a.Logs.EnableFilePersistence(logPath); err != nil {
			log.Printf("[startup] aviso: falha ao habilitar persistência de logs em arquivo: %v", err)
		} else {
			a.Logs.Append("[startup] persistência de logs habilitada em " + logPath)
		}
	}
	a.chatSvc.LoadPersistedConfig()
	// Carrega o debug_config.json (o que o usuário salvou na página de Debug —
	// C:\ProgramData\Discovery) ANTES do config de produção. O loader de
	// produção (ApplyRuntimeConnectionConfig) parte da config atual e sobrescreve
	// apenas os campos de conexão quando o config.json traz credenciais; quando
	// o config.json não tem credenciais (bootstrap pendente), o que foi salvo no
	// Debug sobrevive ao restart em vez de ser perdido (bug da loja de apps:
	// "configuração de servidor API incompleta" até reiniciar o agente/ui).
	a.DebugSvc.LoadPersistedConfig()
	a.DebugSvc.LoadConnectionConfigFromProduction()
	a.initChatLogger()

	mcp.RegisterDiscoveryTools(reg, a)

	a.QueuedForceHeartbeat.Store(false)

	if opts.DebugMode {
		a.Logs.Append("[startup] modo debug ativo por tecla de atalho (execução atual)")
	}

	agentconfig.NormalizePSADTConfigDefaults(&a.AgentConfig.PSADT)
	agentconfig.NormalizeRolloutDefaults(&a.AgentConfig.Rollout)

	return a
}

func (a *App) GetRuntimeFlags() RuntimeFlags {
	return RuntimeFlags{DebugMode: a.RuntimeFlags.DebugMode, StartMinimized: a.RuntimeFlags.StartMinimized, ServiceMode: a.RuntimeFlags.ServiceMode}
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
	return a.RunCore(ctx)
}

// RunCore inicia o ciclo de vida do App sem depender de tipos do Wails.
// Usado pelo modo serviço (cmd/discovery-service) e pela UI (via
// ServiceStartup). No modo serviço, startup() já pula os itens de UI.
func (a *App) RunCore(ctx context.Context) error {
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
// Também garante que a janela fique dentro da área de trabalho visível
// (proteção contra DPI scaling 125%/150% que esconde o chrome da janela).
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
	a.FitWindowToWorkArea()
}

// EmitEvent emite um evento customizado para o frontend (v3).
//
// No modo serviço (SYSTEM) não existe frontend — o evento é repassado às
// UIs companion conectadas via IPC named pipe (PLANO_AGENT_SERVICE_SYSTEM.md,
// Fase 2). Notificações "notification:new", connectivity, chat:question etc.
// chegam à UI renderizada na sessão do usuário sem acoplamento extra.
//
//wails:ignore
func (a *App) EmitEvent(name string, data ...any) {
	if a.app == nil {
		// Sem app Wails (serviço ou headless): repassa via IPC se houver UI.
		a.broadcastIPCEvent(name, data...)
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
	if a.QuitRequested.Swap(true) {
		return
	}
	if a.app == nil {
		return
	}
	a.app.Quit()
}

func (a *App) GetAgentConfiguration() agentconfig.AgentConfiguration {
	a.AgentConfigMu.RLock()
	cfg := a.AgentConfig
	a.AgentConfigMu.RUnlock()
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
	// Deriva do contexto de ciclo de vida quando disponível para que a coleta
	// aborte imediatamente durante o shutdown (não segura o encerramento).
	hbCtx := context.Background()
	if a.ctx != nil {
		hbCtx = a.ctx
	}
	ctx, cancel := context.WithTimeout(hbCtx, 5*time.Second)
	defer cancel()
	if m := inventory.CollectHeartbeatMetrics(ctx); m != nil {
		mergeHeartbeatMetrics(&metrics, m)
		metrics.P2pPeers = a.getKnownP2PPeers()
	}

	// Enriquecer com dados de endereçamento P2P (libp2p peer ID, addrs, port)
	if a.P2PCoord != nil {
		metrics.PeerID, metrics.Addrs, metrics.Port = a.P2PCoord.GetP2PAddressingInfo()
	}

	// UI online (PLANO_SEPARACAO_SERVICO_UI.md, Fase C2): no modo serviço,
	// informa ao servidor se há UI companion conectada via IPC. Fora do modo
	// serviço (UI standalone) o campo permanece nil (omitido no JSON).
	if a.RuntimeFlags.ServiceMode && a.ipcServer != nil {
		online := a.ipcServer.ClientCount() > 0
		metrics.UIOnline = &online
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
	if a.P2PCoord == nil {
		return 0
	}
	return len(a.P2PCoord.GetPeers())
}

// onNatsConnected é chamado pelo agentconn após a conexão NATS ser estabelecida.
// Injeta a conexão NATS no remoteSessionMgr para habilitar o streaming de frames.
func (a *App) onNatsConnected(nc *nats.Conn, cfg agentconn.Config) {
	if a.RemoteSessionMgr == nil {
		return
	}
	a.RemoteSessionMgr.SetNatsConn(nc, cfg.ClientID, cfg.SiteID, cfg.AgentID)
	a.Logs.Append(fmt.Sprintf("[remote-session] NATS conectado — streaming habilitado (tenant=%s, site=%s, agent=%s)",
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
	// Idade do último pong global: correlaciona oscilações do indicador com
	// lacunas de entrega do servidor (watchdog de global pong / WSS).
	pongInfo := " pongAge=never"
	if a.SyncSvc != nil {
		if lastPongAt, _, _, _ := a.SyncSvc.GlobalPongStatus(); !lastPongAt.IsZero() {
			pongInfo = fmt.Sprintf(" pongAge=%s", time.Since(lastPongAt).Round(time.Second))
		}
	}
	a.Logs.Append(fmt.Sprintf("[connectivity] mudanca de estado para %s (transport=%s%s)", state, transport, pongInfo))
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

	captureStdLog(a.Logs.Buffer)

	// Diagnóstico de elevação/integridade — importante para o controle remoto
	// (SendInput via UIPI) e gerenciamento de serviços (SCM). Colocado após
	// captureStdLog para que o log seja capturado no buffer da UI/support.
	// Com o manifest requireAdministrator, espera-se elevated=true integrity=High.
	if ev := platform.ElevationReport(); ev != "" {
		log.Printf("[startup] elevação: %s", ev)
	}

	if a.RuntimeFlags.DebugMode {
		if a.debugHTTP == nil {
			if err := a.StartDebugHTTPServer(); err != nil {
				log.Printf("[debug-http] falha ao iniciar servidor HTTP local: %v", err)
			}
		}
		if port := a.GetDebugHTTPPort(); port > 0 {
			a.Logs.Append(fmt.Sprintf("[debug-http] servidor HTTP local iniciado em http://127.0.0.1:%d", port))
		}
	}

	// ── Modo serviço (PLANO_AGENT_SERVICE_SYSTEM.md, Fase 1) ──
	// O serviço SYSTEM não tem sessão de usuário: pula SSE de chat, tray,
	// janela e idle-mode. O core (DB, inventory, agentConn, automation, sync,
	// P2P, self-update) roda igual ao standalone via staged startup abaixo.
	if a.RuntimeFlags.ServiceMode {
		log.Println("[service] startup do core (sem UI)")
		a.ipcServer = StartIPCServer(a.handleIPCMessage)
		// Toast nativo (Fase C2/D4): cliques nos botões de ação do toast
		// (quando o WinRT entrega o callback) injetam a resposta no
		// notificationSvc — fecha o ciclo require_confirmation sem UI.
		setToastActivationHook(func(notificationID, result string) {
			a.NotificationSvc.Respond(notificationID, result)
		})
		a.runCoreStartup(ctx)
		return
	}

	// ── Companion mode (PLANO_AGENT_SERVICE_SYSTEM.md, Fase 2) ──
	// Se o serviço DiscoveryAgent está ativo, a UI conecta via IPC e roda
	// apenas UI/tray/chat/notificações.
	//
	// M5: a UI NÃO roda core NEM tem fallback standalone — o core vive
	// EXCLUSIVAMENTE no serviço (discovery-service.exe). Se o serviço não
	// estiver presente (boot/update), a UI conecta o cliente IPC (RunConnectLoop
	// aguarda o pipe com backoff) e o estado fica "serviço ausente" até o
	// serviço subir. Nenhum segundo core, nenhum DB local na UI.
	a.startIPCClient()

	if err := a.EnsureChatSSEServer(); err != nil {
		log.Printf("[chat-sse] falha ao iniciar servidor SSE dedicado: %v", err)
	} else if port := a.GetChatSSEPort(); port > 0 {
		log.Printf("[chat-sse] servidor SSE dedicado ativo em http://127.0.0.1:%d/api/chat-events", port)
	} else {
		log.Printf("[chat-sse] AVISO: servidor SSE dedicado retornou porta 0 — chat nativo pode falhar!")
	}

	a.startTray()
	if a.RuntimeFlags.StartMinimized {
		a.hideWindowOnStartup()
	}
	a.applyIdleMode(true)

	// M5: a UI NÃO abre DB e NÃO roda staged startup — dados que os bridges da
	// UI consultam (config, inventário, pending counts, Memory Notes) chegam
	// via IPC RPC (companion_rpc.go), servidos pelo core do serviço.
	a.applyStartupThrottleConfig()
}

// runCoreStartup é o startup do modo serviço: DB + staged startup, sem os
// itens acoplados à sessão do usuário (SSE chat, tray, janela, idle mode).
func (a *App) runCoreStartup(ctx context.Context) {
	dataDir := GetDataDir()
	db, err := database.Open(dataDir)
	if err != nil {
		log.Printf("[service] AVISO: falha ao abrir database: %v", err)
	} else {
		a.CoreAgent.DB = db
		log.Printf("[service] database SQLite inicializado em %s", dataDir)

		if a.CatalogClient != nil {
			a.CatalogClient.SetDatabase(db)
		}
		if a.AutomationSvc != nil {
			a.AutomationSvc.SetDB(db)
		}
		if a.InventorySvc != nil {
			a.InventorySvc.SetDB(db)
			_ = a.InventorySvc.Startup(ctx)
		}
		if a.SupportSvc != nil {
			a.SupportSvc.SetDB(db)
		}
		agentIDForEngine := strings.TrimSpace(a.GetDebugConfig().AgentID)
		a.ConsolEngine = consolidation.New(db, agentIDForEngine)
	}

	log.Println("[service] core ativo — staged startup iniciando")

	a.runStagedStartup(ctx)
	a.applyStartupThrottleConfig()
}

// runStagedStartup contém as fases com delays do startup (inventory, agentConn,
// automation/sync/P2P, self-update/cleanup) — compartilhado entre UI standalone
// e serviço SYSTEM (revisão 2026-09-04: os delays acompanham o core).
func (a *App) runStagedStartup(ctx context.Context) {
	const (
		startupPhaseInventory   = 2 * time.Second
		startupPhaseAgentConn   = 8 * time.Second
		startupPhaseBackground  = 10 * time.Second
		startupPhaseMaintenance = 12 * time.Second
	)

	// Phase 1: Inventory collection (heaviest operation — delayed 2s).
	a.StartupWg.Add(1)
	// M4: contador para o shutdown saber quantas fases ficaram pendentes.
	a.startupPhasePending.Add(1)
	a.safeGo(func() {
		defer a.startupPhasePending.Add(-1)
		defer a.StartupWg.Done()

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
			a.StartupMu.Lock()
			a.StartupErr = err
			a.StartupMu.Unlock()
			return
		}
		a.InvCache.Set(report)
		if a.InventorySvc != nil {
			a.InventorySvc.SyncInventoryOnStartup(ctx, report)
		}
	})

	// Phase 2: Agent connection (bootstrap + heartbeat).
	a.StartupWg.Add(1)
	// M4: contador para o shutdown saber quantas fases ficaram pendentes.
	a.startupPhasePending.Add(1)
	a.safeGo(func() {
		defer a.startupPhasePending.Add(-1)
		defer a.StartupWg.Done()

		select {
		case <-ctx.Done():
			return
		case <-time.After(startupPhaseAgentConn):
		}

		if a.DebugSvc != nil {
			a.DebugSvc.BootstrapAgentCredentialsFromInstallerConfig(ctx)
		}

		// Após bootstrap bem-sucedido, dispara reconciliação de
		// recursos que dependem de credenciais (inventário, sync,
		// configuração do agente). Isso garante que o agent fique
		// plenamente operacional no primeiro boot.
		// B4: usa safeGo — panic aqui derrubava o processo sem recovery.
		a.safeGo(func() {
			_ = a.onPostBootstrapProvisioned(ctx)
		})

		a.AgentConn.Run(ctx)
	})

	// Phase 3: Automation, sync coordinator, P2P bootstrap.
	a.StartupWg.Add(1)
	// M4: contador para o shutdown saber quantas fases ficaram pendentes.
	a.startupPhasePending.Add(1)
	a.safeGo(func() {
		defer a.startupPhasePending.Add(-1)
		defer a.StartupWg.Done()

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

		if a.AutomationSvc != nil {
			a.safeGo(func() {
				a.AutomationSvc.Run(ctx, func() {})
			})
		}

		// Bootstrap automático do módulo PSADT (zero-touch).
		// Se habilitado na config e o módulo não estiver instalado, instala
		// em background sem bloquear o startup do agente.
		a.bootstrapPSADTModuleIfNeeded()

		if a.SyncSvc != nil {
			// Inicializa o ciclo de vida do sync.Service antes de iniciar o loop.
			_ = a.SyncSvc.Startup(ctx)
			a.safeGo(func() {
				a.SyncSvc.Run(ctx)
			})
		}

		if a.P2PCoord != nil {
			// Inicializa o ciclo de vida do p2p.Coordinator antes de iniciar o loop.
			_ = a.P2PCoord.Startup(ctx)
			if !isAgentConfigured() && a.zeroTouchConfigRegistrationAllowed() {
				a.safeGo(func() {
					a.RunOnboardingLoop(ctx)
				})
			}
			a.safeGo(func() {
				a.P2PCoord.Run(ctx)
			})
		}
	})

	// Phase 4: Self-updater + DB cleanup ticker (lowest priority).
	a.StartupWg.Add(1)
	// M4: contador para o shutdown saber quantas fases ficaram pendentes.
	a.startupPhasePending.Add(1)
	a.safeGo(func() {
		defer a.startupPhasePending.Add(-1)
		defer a.StartupWg.Done()

		select {
		case <-ctx.Done():
			return
		case <-time.After(startupPhaseMaintenance):
		}

		if a.SelfUpdater != nil {
			a.safeGo(func() {
				a.SelfUpdater.ResumePendingInstallReport(a.ctx)
				a.SelfUpdater.Run(a.ctx, 0)
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
				if a.CoreAgent.DB == nil {
					continue
				}
				n1, err1 := a.CoreAgent.DB.CleanupExpiredCommandResultOutbox(time.Now(), cleanupBatchSize)
				n2, err2 := a.CoreAgent.DB.CleanupExpiredP2PTelemetryOutbox(time.Now(), cleanupBatchSize)
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
	a.InvCache.Set(report)

	if a.InventorySvc != nil {
		a.Logs.Append("[inventory] coleta periodica concluida; sincronizando com servidor")
		a.InventorySvc.SyncInventoryOnStartup(ctx, report)
	}

	// TriggerOnAgentCheckIn: dispara a cada ciclo de inventario completo (~6h).
	if a.AutomationSvc != nil {
		a.AutomationSvc.TriggerAgentCheckInTasks(ctx)
	}
}

func (a *App) SendTestHeartbeat() string {
	if !a.QueuedForceHeartbeat.CompareAndSwap(false, true) {
		return "erro: heartbeat manual ja em andamento"
	}
	defer a.QueuedForceHeartbeat.Store(false)

	a.Logs.Append("[heartbeat][manual] enviando heartbeat manual...")
	if a.AgentConn == nil {
		a.Logs.Append("[heartbeat][manual] falha ao enviar heartbeat manual: agent runtime nao inicializado")
		return "erro: agent runtime nao inicializado"
	}
	if a.AgentConn.ForceHeartbeat() {
		a.Logs.Append("[heartbeat][manual] heartbeat manual enviado com sucesso")
		return "heartbeat manual enviado com sucesso"
	}
	a.Logs.Append("[heartbeat][manual] falha ao enviar heartbeat manual: timeout ou nenhuma conexão ativa")
	return "falha ao enviar heartbeat manual: timeout ou nenhuma conexão ativa"
}

func (a *App) startupLogf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	log.Print(line)
	a.Logs.Append(line)
}

func (a *App) safeGo(fn func()) {
	safego.Go(fn, func(line string) {
		a.Logs.Append(line)
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
		a.Logs.Append("[startup] post-bootstrap: timeout aguardando provisionamento")
		return nil
	}

	a.Logs.Append("[startup] post-bootstrap: agente provisionado — reconciliando recursos")

	// 1. osquery + inventário inicial (não executou no goroutine #2 porque
	//    ainda não estava provisionado).
	a.ensureOsqueryInstalled()
	if !a.InvCache.Has() {
		a.startupLogf("[startup] post-bootstrap: executando inventario inicial")
		report, err := a.collectInventoryWithHeartbeat(ctx)
		if err != nil {
			a.startupLogf("[startup] post-bootstrap: falha ao coletar inventario: %v", err)
		} else {
			a.InvCache.Set(report)
			if a.InventorySvc != nil {
				a.InventorySvc.SyncInventoryOnStartup(ctx, report)
			}
		}
	}

	// 2. Refresh da configuração do agent (clientId/siteId, políticas).
	if a.SyncSvc != nil {
		_ = a.refreshAgentConfiguration(ctx)
		a.SyncSvc.ReconcileFromManifest(ctx, "post-bootstrap")
	}

	// 3. Automação — carrega políticas iniciais.
	if a.AutomationSvc != nil {
		if _, err := a.AutomationSvc.RefreshPolicy(ctx, false); err != nil {
			a.Logs.Append("[startup] post-bootstrap: falha ao carregar politicas de automacao: " + err.Error())
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
			a.Logs.Append("[startup] post-bootstrap: falha ao carregar app-store: " + err.Error())
		}
		if a.SupportSvc != nil && a.featureEnabled(a.GetAgentConfiguration().KnowledgeBaseEnabled) {
			if err := a.SupportSvc.RefreshKnowledgeBase(); err != nil {
				a.Logs.Append("[startup] post-bootstrap: falha ao atualizar knowledge base: " + err.Error())
			}
		}
		// Registra tools MCP do agent na API para o fluxo multi-round do chat
		if err := a.RegisterAgentToolsOnServer(); err != nil {
			a.Logs.Append("[startup] post-bootstrap: falha ao registrar agent tools: " + err.Error())
		}
	})

	a.Logs.Append("[startup] post-bootstrap: reconciliacao concluida")
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
	if a.AppsSvc == nil {
		return
	}

	// Apenas registra o status sem instalar.
	status := inventory.GetOsqueryStatus()
	if status.Installed {
		a.startupLogf("[startup] osquery presente (fallback opcional): %s", status.Path)
	}
}

func (a *App) hideWindowOnStartup() {
	// B4: safeGo em vez de goroutine crua — recovery de panic no watcher do tray.
	a.safeGo(func() {
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
	})
}

func (a *App) shutdown() {
	// Cancela o contexto de ciclo de vida PRIMEIRO: os loops de longa duração
	// (agentconn/NATS, sync, P2P, automation) começam a desmontar em paralelo
	// com o cleanup abaixo. Sem isso, quando o agente está online o shutdown
	// serializa os timeouts dos servidores HTTP/SSE (até 10s) antes mesmo de
	// sinalizar o cancelamento — era a principal causa da demora ao sair via
	// tray com o agente conectado.
	if a.cancel != nil {
		a.cancel()
	}

	a.applyIdleMode(false)

	if !a.RuntimeFlags.ServiceMode {
		a.StopDebugHTTPServer()
		a.StopChatSSEServer()
	}

	// Encerra IPC (serviço ou cliente companion).
	if a.ipcServer != nil {
		a.ipcServer.Close()
	}
	if a.ipcClient != nil {
		a.ipcClient.Close()
	}
	// NOTA: remoteDebug e remoteSessionMgr agora são Services Wails v3
	// separados (adapters thin em remote_services.go). Seus ciclos de vida
	// (Startup/Shutdown) são gerenciados pelo Wails, que encerra os services
	// na ordem inversa de registro — ou seja, antes do App. Por isso não são
	// encerrados aqui.

	// Desliga o domínio Sync (cancela contexto, aguarda goroutines).
	if a.SyncSvc != nil {
		_ = a.SyncSvc.Shutdown()
	}

	// Desliga o domínio P2P (cancela contexto do Coordinator).
	if a.P2PCoord != nil {
		_ = a.P2PCoord.Shutdown()
	}

	// Desliga o domínio Inventory (para timer de refresh pós-instalação).
	if a.InventorySvc != nil {
		_ = a.InventorySvc.Shutdown()
	}

	// NOTA: a.cancel() já foi chamado no topo do shutdown para desmontar os
	// loops em paralelo com o cleanup.

	// Aguarda as goroutines de startup com timeout. Quando o agente está
	// offline, o loop de reconexão do agentconn pode ficar preso numa
	// chamada de conexão (nats.Connect com timeout de 5s) que não respeita
	// o cancelamento do contexto. Sem limite, o shutdown e o "Sair" do tray
	// travariam permanentemente — dando a impressão de que o agente "não
	// fecha e não abre mais". Um timeout razoável garante que o processo
	// sempre encerre, mesmo que alguma goroutine não responda a tempo.
	// 3s é suficiente: com o cancelamento antecipado (topo do shutdown),
	// os loops já foram sinalizados bem antes de chegarmos aqui.
	startupDone := make(chan struct{})
	go func() {
		a.StartupWg.Wait()
		close(startupDone)
	}()
	select {
	case <-startupDone:
	case <-time.After(5 * time.Second):
		// M4: 5s (antes 3s) + contagem de fases pendentes — dá visibilidade de
		// QUANTAS goroutines de startup não terminaram quando o SQLite é fechado.
		log.Printf("[shutdown] timeout aguardando goroutines de startup (pendentes=%d); forçando encerramento",
			a.startupPhasePending.Load())
	}

	if a.CoreAgent.DB != nil {
		if a.startupPhasePending.Load() > 0 {
			log.Printf("[shutdown] AVISO: fechando database com %d fase(s) de startup pendente(s) — queries em andamento falharão graciosamente (WAL preserva integridade)",
				a.startupPhasePending.Load())
		}
		if err := a.CoreAgent.DB.Close(); err != nil {
			log.Printf("[shutdown] erro ao fechar database: %v", err)
		}
	}
	a.Logs.CloseFile()
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
		a.Logs.Append(fmt.Sprintf("[agent] %s-defer [FORCE] maxDefers=%d atingido — restart forçado", action, ds.maxDefers))
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
	// M3: o callback do time.AfterFunc roda em goroutine própria — captura os
	// contadores AQUI sob lock; ler ds.deferCount/ds.maxDefers dentro do
	// callback (fora do lock) era data race com scheduleDeferredRestart/cancel.
	deferCount := ds.deferCount
	maxDefers := ds.maxDefers

	// Cancela timer anterior se existir
	if ds.timer != nil {
		ds.timer.Stop()
	}

	ds.timer = time.AfterFunc(time.Duration(deferMinutes)*time.Minute, func() {
		a.Logs.Append(fmt.Sprintf("[agent] %s-defer [RETRY] defer=%d/%d — re-exibindo prompt", action, deferCount, maxDefers))
		result := a.showDeferrableRestartPrompt(action, delaySeconds, msg, deferMinutes)
		if result == "restart_now" || result == "fallback" {
			a.executeSystemPowerAction(context.Background(), action, delaySeconds, false, msg)
		} else {
			// "defer" — re-agenda
			a.scheduleDeferredRestart(action, pp)
		}
	})

	ds.mu.Unlock()

	a.Logs.Append(fmt.Sprintf("[agent] %s-defer [SCHED] adiado para daqui %dmin (defer=%d/%d)", action, deferMinutes, deferCount, maxDefers))
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
	a.AgentInfo.Invalidate()
	a.AppStorePolicy.Invalidate()

	a.InvCache.Reset()

	log.Println("[tray] caches em memória limpos para economizar recursos")
}

func (a *App) GetStartupError() string {
	a.StartupMu.RLock()
	defer a.StartupMu.RUnlock()
	if a.StartupErr != nil {
		return a.StartupErr.Error()
	}
	return ""
}

func (a *App) beginActivity(activity string) func() {
	a.ActivityMu.Lock()
	a.ActiveOps++
	shouldLeaveIdle := a.ActiveOps == 1
	a.ActivityMu.Unlock()

	if shouldLeaveIdle {
		supported := a.applyIdleMode(false)
		if supported {
			a.Logs.Append("[efficiency] modo eficiencia desativado: " + activity)
		}
	}

	return func() {
		a.ActivityMu.Lock()
		if a.ActiveOps > 0 {
			a.ActiveOps--
		}
		shouldEnterIdle := a.ActiveOps == 0
		a.ActivityMu.Unlock()

		if shouldEnterIdle {
			supported := a.applyIdleMode(true)
			if supported {
				a.Logs.Append("[efficiency] modo eficiencia ativado (aguardo)")
			}
		}
	}
}

func (a *App) applyIdleMode(idle bool) bool {
	if !efficiencyModeEnabled {
		a.ActivityMu.Lock()
		a.IdleKnown = true
		a.IdleCapable = false
		a.LastIdle = false
		a.ActivityMu.Unlock()
		a.updateTrayIdleState(false, false)
		return false
	}

	a.ActivityMu.Lock()
	sameState := a.LastIdle == idle && a.IdleKnown
	if sameState {
		supported := a.IdleCapable
		a.ActivityMu.Unlock()
		return supported
	}
	a.LastIdle = idle
	a.ActivityMu.Unlock()

	supported, err := processutil.SetEfficiencyMode(idle)
	a.ActivityMu.Lock()
	a.IdleKnown = true
	a.IdleCapable = supported
	a.ActivityMu.Unlock()

	if err != nil {
		a.Logs.Append("[efficiency] erro ao alterar modo: " + err.Error())
	}

	if idle {
		if trimErr := processutil.TrimCurrentProcessWorkingSet(); trimErr != nil {
			a.Logs.Append("[efficiency] erro ao reduzir memoria: " + trimErr.Error())
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
