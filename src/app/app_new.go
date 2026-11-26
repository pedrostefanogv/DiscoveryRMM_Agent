package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"discovery/app/agentconfig"
	"discovery/app/apiclient"
	"discovery/app/appstore"
	"discovery/app/core/agentconn"
	"discovery/app/core/automation"
	"discovery/app/core/autoupdate"
	"discovery/app/core/buildinfo"
	"discovery/app/core/chocolatey"
	"discovery/app/core/data"
	"discovery/app/core/database"
	"discovery/app/core/inventory"
	"discovery/app/core/maintenance"
	"discovery/app/core/mcp"
	"discovery/app/core/models"
	"discovery/app/core/platform"
	"discovery/app/core/printer"
	"discovery/app/core/remotedebug"
	"discovery/app/core/remotesession"
	"discovery/app/core/selfupdate"
	"discovery/app/core/services"
	"discovery/app/core/thirdparty"
	"discovery/app/core/tpmvault"
	"discovery/app/core/windowsupdate"
	"discovery/app/core/winget"
	"discovery/app/coreagent"
	"discovery/app/customfields"
	"discovery/app/debug"
	"discovery/app/debughttp"
	"discovery/app/decommission"
	"discovery/app/installer"
	appinventory "discovery/app/inventory"
	"discovery/app/logs"
	"discovery/app/p2p"
	"discovery/app/services/chat"
	"discovery/app/services/hardwareid"
	"discovery/app/services/memory"
	"discovery/app/services/notifications"
	"discovery/app/services/psadt"
	appsupport "discovery/app/support"
	syncsvc "discovery/app/sync"
	"discovery/app/updates"
)

func NewApp(opts AppStartupOptions) *App {
	catalogClient := data.NewHTTPClient(catalogURL, catalogTimeout)
	wingetClient := winget.NewClient(wingetTimeout)
	chocolateyClient := chocolatey.NewClient(wingetTimeout)
	inventoryProvider := inventory.NewProvider(inventoryTimeout)
	printerManager := printer.NewManager(printerTimeout)

	reg := mcp.NewRegistry()

	a := &App{
		ctx:                 context.Background(),
		mcpRegistry:         reg,
		mainWindowFrameless: opts.MainWindowFrameless,
		chatEvents:          debughttp.NewChatEventBroker(),
		startupTime:         time.Now(),
		trayIcon:            opts.TrayIcon,
		trayProvisioning:    opts.TrayProvisioningIcon,
		trayOffline:         opts.TrayOfflineIcon,
		commandDedupe:       newCommandDedupeStore(),
	}
	a.RuntimeFlags = coreagent.RuntimeFlags{DebugMode: opts.DebugMode, ServiceMode: opts.ServiceMode}
	a.CoreAgent.UpdateTrigger = make(chan struct{}, 1)
	a.Logs.Buffer = logs.New()

	a.initCoreServices(catalogClient, wingetClient, chocolateyClient, inventoryProvider, printerManager)
	a.initAutomationWiring()
	a.initConnectivityAndRemote()
	a.initInventoryWiring(wingetClient, chocolateyClient, inventoryProvider)
	a.initSelfUpdater()
	a.initSupportAndDebug(reg, opts)

	return a
}

func (a *App) initCoreServices(
	catalogClient *data.HTTPClient,
	wingetClient *winget.Client,
	chocolateyClient *chocolatey.Client,
	inventoryProvider *inventory.Provider,
	printerManager *printer.Manager,
) {
	// Campos do core (migração lote 1 — embed coreagent.CoreAgent).
	a.CoreAgent.CatalogSvc = services.NewCatalogService(catalogClient)
	a.CoreAgent.CatalogClient = catalogClient
	a.CoreAgent.AppsSvc = services.NewAppsService(wingetClient, chocolateyClient)
	a.CoreAgent.InvSvc = services.NewInventoryService(inventoryProvider)
	a.CoreAgent.PrinterSvc = services.NewPrinterService(printerManager)

	// Consentimento de captura de tela (tools MCP + overlay do chat).
	a.initScreenshotService()

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
				// UIAuthenticatedCount: conexão de probe/não autenticada NÃO é UI —
				// contar ClientCount aqui suprimia o toast nativo sem interface real.
				if a.ipcServer != nil && a.ipcServer.UIAuthenticatedCount() > 0 {
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
			cfg := a.getDebugConfigInternal()
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
			cfg := a.getDebugConfigInternal()
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
			cfg := a.getDebugConfigInternal()
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
		// Toda resposta autenticada da app-store passa por aqui: é por este
		// caminho que o 403 "Agent registration is pending (zero-touch)"
		// (o erro do print da Loja) alimenta o estado de autorização do
		// servidor e faz a UI mostrar a tela de espera.
		NoteAuthorizationRefusal: func(resp *http.Response, body []byte) {
			a.noteServerAuthorizationRefusal(resp, body, nil)
		},
	})
	a.SupportSvc = appsupport.NewService(appsupport.Options{
		Logf:        a.Logs.Append,
		Ctx:         func() context.Context { return a.ctx },
		DB:          a.CoreAgent.DB,
		AgentInfo:   &a.AgentInfo,
		DebugConfig: a.getDebugConfigInternal,
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
		// Fallback offline: clientId/siteId vêm da configuração persistida do
		// agente (agent_configuration_raw, 30 dias) quando /configuration não
		// responde. Permite consultar chamados e base de conhecimento offline.
		AgentInfoFallback: func() (appsupport.AgentInfo, bool) {
			cfg := a.GetAgentConfiguration()
			clientID := strings.TrimSpace(cfg.ClientID)
			siteID := strings.TrimSpace(cfg.SiteID)
			if clientID == "" && siteID == "" {
				return appsupport.AgentInfo{}, false
			}
			return appsupport.AgentInfo{
				AgentID:  strings.TrimSpace(a.getDebugConfigInternal().AgentID),
				ClientID: clientID,
				SiteID:   siteID,
			}, true
		},
		// Toda resposta autenticada de Suporte/KB passa por aqui: é o gancho que
		// detecta o 403 "Agent registration is pending (zero-touch)" (o erro
		// dos prints em Suporte e Base de Conhecimento) e liga o estado de
		// "aguardando liberação do servidor".
		NoteAuthorizationRefusal: func(resp *http.Response, body []byte) {
			a.noteServerAuthorizationRefusal(resp, body, nil)
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
	a.Exporter = updates.NewExporter(updates.ExportOptions{
		BeginActivity: a.beginActivity,
		Inventory: func() (models.InventoryReport, error) {
			return a.getInventoryForExport()
		},
		GetRedact: a.getRedact,
		SetRedact: a.ExportCfg.Set,
	})
}

func (a *App) initAutomationWiring() {
	a.AutomationSvc = automation.NewService(func() automation.RuntimeConfig {
		cfg := a.getDebugConfigInternal()
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
	// Janela de manutenção (F3, opt-in por task): tasks com
	// TaskRespectMaintenanceWindow=true só executam ações de pacote dentro da
	// janela efetiva (fuso IANA). Nil-safe: Effective() de config nil devolve
	// janela desligada (sem gate).
	a.AutomationSvc.SetMaintenanceWindowSource(func() maintenance.Window {
		win, _ := a.GetAgentConfiguration().MaintenanceWindow.Effective()
		return win
	})
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
	// Retenção do P2P_Temp: remove instaladores de pacotes já em estado final
	// (instalado/sem update pendente), que não serão mais usados e ocupam
	// centenas de MB (ex.: Chrome 520 MB). Só artifacts mapeáveis a pacote.
	p2p.SetArtifactRetentionChecker(func(artifactID, artifactName string) bool {
		return a.artifactShouldBeRemoved(artifactID, artifactName)
	})

	// Gate de utilidade do fetch P2P: não baixa instalador de pacote já em estado
	// final (instalado/sem update pendente) — evita os downloads desnecessários
	// em P2P_Temp. Artifacts sem pacote mapeável (selfupdate:, name:) são sempre
	// permitidos.
	p2p.SetArtifactFetchGate(func(artifactID, artifactName string) bool {
		return a.artifactFetchUseful(artifactID, artifactName)
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
		dispatchReq := NotificationDispatchRequest{
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
		}
		// Toast nativo do app (mesmo caminho do Chat IA): no modo standalone quem
		// tem o servico nativo de notificacoes do Wails e este processo, entao o
		// envio acontece aqui e o evento segue para a UI apenas atualizar o
		// progresso (o frontend nao repete o toast quando a superficie e nativa).
		// No modo servico quem envia e a UI companion via IPC
		// (ipc_app_integration.go) ou, sem UI, o fallback headless ja existente.
		// Se o envio nativo falhar, a marca sai do metadata e o usuario recebe o
		// toast da interface do agent em vez de nada.
		// AllowsNotification aplica a política por eventType e o rollout ANTES do
		// envio: sem esse portão o kill switch (enableNotifications/eventos
		// bloqueados) e a política que rebaixa o evento para "silent" (ou que
		// exige confirmação) não valeriam para o toast nativo, porque ele sai do
		// processo antes do Dispatch.
		if !a.RuntimeFlags.ServiceMode && automation.IsNativeNotificationSurface(dispatchReq.Metadata) {
			sent := a.NotificationSvc != nil &&
				a.NotificationSvc.AllowsNotification(dispatchReq) &&
				a.sendNativeTaskNotification(dispatchReq.NotificationID, dispatchReq.Title, dispatchReq.Message)
			if !sent {
				// Sem via nativa disponível ou sem permissão da política: volta
				// para a superfície da interface do agent (o Dispatch decide o
				// resto — bloqueio por rollout não emite nada).
				delete(dispatchReq.Metadata, automation.NotificationSurfaceMetadataKey)
			}
		}
		// Task com "notificar usuario" (RequiresApproval): tenta o Welcome nativo
		// do PSADT antes do toast. O portão AllowsUserPrompt aplica a política por
		// eventType e o rollout (enableRequireConfirmation/enableNotifications) —
		// sem ele o Welcome furaria o kill switch. Se o PSADT estiver
		// desligado/indisponivel o helper devolve handled=false e o fluxo cai no
		// toast require_confirmation.
		if a.NotificationSvc != nil && a.NotificationSvc.AllowsUserPrompt(dispatchReq) {
			if welcomeResp, handled := a.tryDispatchAutomationPsadtWelcome(req); handled {
				return welcomeResp
			}
		}
		resp := a.DispatchNotification(dispatchReq)
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

	// Pré-carga P2P pedida pelo policy-sync (PreloadPackages): reaproveita o mesmo
	// caminho coordenado do comando p2ppreload (ShouldPreloadPackage + stagger).
	a.AutomationSvc.SetPreloadHandler(func(packages []automation.PreloadPackage) {
		if a.packageManagerRouter == nil || a.P2PCoord == nil || len(packages) == 0 {
			return
		}
		items := make([]p2pPreloadPackage, 0, len(packages))
		for _, pkg := range packages {
			packageID := strings.TrimSpace(pkg.PackageID)
			if packageID == "" {
				continue
			}
			items = append(items, p2pPreloadPackage{
				PackageID:  packageID,
				ActionType: string(pkg.ActionType),
			})
		}
		if len(items) == 0 {
			return
		}
		ctx := a.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		go a.runP2pPreload(ctx, items)
	})

	// Automação: usa a última lista de instalados conhecida do inventário para
	// NÃO baixar instalador quando o "winget list" falhar e o pacote já estiver
	// instalado (evita transferências indevidas de centenas de MB).
	automation.SetInstalledPackageChecker(func(packageID string) (bool, bool) {
		if a.InventorySvc == nil {
			return false, false
		}
		return a.InventorySvc.KnownInstalledPackage(packageID)
	})
	// Automação de pacotes CUSTOM: consulta a detecção configurada no servidor
	// (vínculo com o inventário local ou comando de detecção) para pular a
	// instalação de um app já instalado. Sem evidência, não pula (fail-safe).
	automation.SetCustomInstalledChecker(func(installationType, packageID string) (bool, bool) {
		if a.InventorySvc == nil {
			return false, false
		}
		return a.InventorySvc.KnownCustomPackageInstalled(installationType, packageID)
	})
	automation.SetWingetDecisionLogger(func(line string) {
		a.Logs.Append("[automation][decision] " + line)
	})

	// Loja/automação: qualquer install/update/remove de pacote concluído com
	// sucesso agenda o MESMO refresh de inventário do update remoto (debounce
	// de 2 min, novo scan de updates e upload). Antes, mudanças feitas pela
	// Loja só apareciam no dashboard no sync periódico (~6h).
	a.AutomationSvc.SetPackageChangeHandler(func(action, packageID string) {
		if a.InventorySvc != nil {
			a.InventorySvc.ScheduleInventoryRefreshAfterPackageChange(action, packageID)
		}
	})
}

func (a *App) initConnectivityAndRemote() {
	a.RemoteDebug = remotedebug.New(remotedebug.Deps{
		Logf: a.Logs.Append,
		GetConfig: func() remotedebug.Config {
			cfg := a.getDebugConfigInternal()
			return remotedebug.Config{
				AuthToken:    cfg.AuthToken,
				AgentID:      cfg.AgentID,
				NatsServer:   cfg.NatsServer,
				NatsWsServer: cfg.NatsWsServer,
				// Permite derivar wss://<host>/nats/ e nats://<host>:4222 quando
				// NatsWsServer vem vazio (ver BuildPublishers).
				ApiServer: cfg.ApiServer,
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
			reportAgentInitiatedCloseIfNeeded(sessionID, reason)
		},
	)
	wireRemoteConsentManager(a.RemoteSessionMgr)

	a.AgentConn = agentconn.NewRuntime(agentconn.Options{
		LoadConfig: func() agentconn.Config {
			cfg := a.getDebugConfigInternal()
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
		HandleSupportChatEnvelope:     a.handleSupportChatEnvelope,
		OnCommandOutput:               a.onAgentCommandOutput,
		OnNatsConnected:               a.onNatsConnected,
		OnConnectivityChange:          a.onConnectivityChange,
		EnqueueCommandResultOutbox:    a.enqueueCommandResultOutbox,
		ListDueCommandResultOutbox:    a.listDueCommandResultOutbox,
		MarkSentCommandResultOutbox:   a.markSentCommandResultOutbox,
		RescheduleCommandResultOutbox: a.rescheduleCommandResultOutbox,
		LoadCommandDedupe:             a.loadCommandDedupe,
		PersistCommandDedupe:          a.persistCommandDedupe,
		DeleteCommandDedupe:           a.deleteCommandDedupe,
		DescribeDecommission:          a.describeAgentDecommission,
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
			a.EmitEvent("debug:config_updated", "config", redactDebugConfig(cfg))
		},
		HardwareIdentity: func() hardwareid.Info {
			if a.HardwareIDSvc == nil {
				return hardwareid.Info{}
			}
			return a.HardwareIDSvc.Get()
		},
		// Cofre TPM: a LEITURA no boot é incondicional (Enabled=true sempre); a
		// política herdável do servidor governa apenas GRAVAR/APAGAR, na camada
		// de serviço. TPMVaultWritable restringe escrita/apagamento ao processo
		// do serviço (a UI só lê status). Fingerprint via hardwareid (EK + SMBIOS).
		TPMVault: tpmvault.Open(tpmvault.Options{
			Logf: func(line string) {
				a.Logs.Append(line)
			},
			Fingerprint: func() (string, string) {
				if a.HardwareIDSvc == nil {
					return "", ""
				}
				hw := a.HardwareIDSvc.Get()
				return hw.TPMEK, hw.SMBIOSUUID
			},
			Enabled: true,
		}),
		TPMVaultWritable: a.RuntimeFlags.ServiceMode,
	})
	a.AgentConfigSvc = agentconfig.New(agentconfig.FetchDeps{
		GetDebugConfig: a.getDebugConfigInternal,
	})
	// Runtime do reporte de consentimento: o DebugSvc só existe a partir daqui
	// (usar antes causava nil pointer no startup do serviço).
	if a.DebugSvc != nil {
		consentCfg := a.DebugSvc.GetConfig()
		if consentCfg.IsProvisioned() {
			configureRemoteConsentRuntime(
				workerAPIBase(WorkerDebugConfig{ApiScheme: consentCfg.APIScheme(), ApiServer: consentCfg.ApiServer}),
				consentCfg.AuthToken,
				consentCfg.AgentID)
		}
	}
	a.SyncSvc = syncsvc.NewService(a)
	a.P2PConfig = defaultP2PConfig()
	a.P2PCoord = newP2PCoordinator(a)
}

func (a *App) initInventoryWiring(
	wingetClient *winget.Client,
	chocolateyClient *chocolatey.Client,
	inventoryProvider *inventory.Provider,
) {
	inventoryProvider.SetProgressCallback(func() {
		a.pulseInventoryHeartbeat()
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
		RefreshCatalog:    a.refreshCatalogFromAppStore,
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
		// Distribuição P2P do instalador de apps custom: publica o arquivo
		// baixado (vira seed do site) e tenta obtê-lo de um peer da mesma
		// versão antes de baixar da API.
		PublishCustomInstallerForP2P: a.publishCustomInstallerForP2P,
		CustomInstallerFromP2P:       a.fetchCustomInstallerFromP2P,
		DB:                           nil,
		DebugConfig:                  a.getDebugConfigInternal,
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
	// Gerenciador de patches do Windows: cliente WUA (COM) + kill switch vindo
	// da configuração resolvida do servidor. O cache SQLite é injetado depois
	// (o DB só existe quando o serviço sobe), em attachInventoryDatabase.
	// WU-19/WU-20: gerenciadores de terceiros que alimentam o MESMO inventario de
	// patches (chave choco:<id> / winget:<id>). Os clients ja existem para
	// deploy de software — nao ha segundo caminho de execucao.
	a.InventorySvc.SetThirdPartyProviders([]thirdparty.Provider{
		thirdparty.NewChocoProvider(chocolateyClient),
		thirdparty.NewWingetProvider(wingetClient),
	})
	a.InventorySvc.SetWindowsUpdateDeps(appinventory.WindowsUpdateDeps{
		Client: windowsupdate.New(windowsupdate.DefaultOptions()),
		Enabled: func() bool {
			cfg := a.GetAgentConfiguration()
			if cfg.WindowsUpdateEnabled == nil {
				return true
			}
			return *cfg.WindowsUpdateEnabled
		},
		// Origem "third_party" da política de patches (D22): false = o agente
		// não coleta nem reporta apps de terceiros (choco/winget). nil =
		// servidor antigo (mantém coleta, compat).
		ThirdPartyEnabled: func() bool {
			cfg := a.GetAgentConfiguration()
			if cfg.WindowsUpdateThirdPartyEnabled == nil {
				return true
			}
			return *cfg.WindowsUpdateThirdPartyEnabled
		},
	})
}

func (a *App) initSelfUpdater() {
	// Auto-update da Loja (F2): o planner cruza a política efetiva com os
	// pendentes e executa via InventorySvc.Upgrade (gate da Loja + fila única
	// de pacotes). Precedência da automação: um pacote dono de task ativa da
	// policy não entra no auto-update (PackageAuthorizedByPolicy).
	a.AutoUpdateSvc = autoupdate.New(autoupdate.Deps{
		LoadPolicy: func(ctx context.Context) (appstore.EffectivePolicy, error) {
			return a.AppStoreSvc.LoadEffectivePolicy(ctx, false)
		},
		PendingUpdates: func() ([]models.UpgradeItem, error) {
			return a.UpdatesSvc.GetPendingUpdates()
		},
		Upgrade: func(ctx context.Context, target string) (string, error) {
			return a.InventorySvc.Upgrade(target)
		},
		IsAutomated: func(installationType, packageID string) bool {
			return a.AutomationSvc.PackageAuthorizedByPolicy(automation.AppInstallationType(installationType), packageID, "upgrade")
		},
		Window: func() maintenance.Window {
			win, _ := a.GetAgentConfiguration().MaintenanceWindow.Effective()
			return win
		},
		Logf: a.Logs.Append,
		EmitAutoUpdated: func(count int) {
			// Best-effort, sem listener obrigatório na UI.
			a.EmitEvent("store:auto-updated", "count", count)
		},
		// Ledger de execuções de auto-update (F3): histórico LOCAL consultável
		// (decisão de menor risco — sem endpoint/tabela server-side neste
		// ciclo). Persistido no SQLite do agent e exposto via IPC/evento; falha
		// de gravação NÃO interrompe o ciclo.
		RecordExecution: func(execution autoupdate.AutoUpdateExecution) {
			db := a.CoreAgent.DB
			if db == nil {
				return
			}
			result := database.AutoUpdateResultSuccess
			if execution.Result == autoupdate.AutoUpdateResultError {
				result = database.AutoUpdateResultError
			}
			if err := db.InsertAutoUpdateExecution(database.AutoUpdateExecutionEntry{
				AgentID:          strings.TrimSpace(a.getDebugConfigInternal().AgentID),
				InstallationType: execution.InstallationType,
				PackageID:        execution.PackageID,
				Source:           execution.Source,
				Target:           execution.Target,
				TargetVersion:    execution.TargetVersion,
				Result:           result,
				ErrorMessage:     execution.ErrorMessage,
				ExecutedAt:       execution.ExecutedAt,
			}); err != nil {
				a.Logs.Append("[autoupdate pacote] ledger: " + err.Error())
			}
		},
	})
	a.SelfUpdaterCh = make(chan bool, 4)
	a.SelfUpdater = &selfupdate.Updater{
		GetToken:     func() string { return a.getDebugConfigInternal().AuthToken },
		GetAgentID:   func() string { return a.getDebugConfigInternal().AgentID },
		GetApiScheme: func() string { return a.getDebugConfigInternal().ApiScheme },
		GetApiServer: func() string { return a.getDebugConfigInternal().ApiServer },
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
			// Sessão remota ativa (terminal/tela/arquivos): ADIA o update — o
			// instalador reinicia o serviço e mata qualquer console aberto
			// (incidente 2026-10-26: self-update derrubou a sessão de terminal
			// 32s após o console subir → viewer preso em "Console Desconectado").
			// O Retry do Run loop (windowBusyRetryDelay) re-checa até a sessão
			// encerrar.
			if a.RemoteSessionMgr != nil && a.RemoteSessionMgr.CountActive() > 0 {
				return false
			}
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
}

func (a *App) initSupportAndDebug(reg *mcp.Registry, opts AppStartupOptions) {
	a.chatSvc = chat.New(reg, chat.Deps{
		ExecuteInUserSession: a.ExecuteMCPToolInUserSession,
		Ctx:                  func() context.Context { return a.ctx },
		Logf: func(line string) {
			a.Logs.Append(line)
		},
		GetDebugConfig: func() chat.DebugConfig {
			cfg := a.getDebugConfigInternal()
			return chat.DebugConfig{
				AgentID:   cfg.AgentID,
				ApiScheme: cfg.ApiScheme,
				ApiServer: cfg.ApiServer,
				AuthToken: cfg.AuthToken,
			}
		},
		GetAgentConfiguration: func() chat.AgentConfiguration {
			cfg := a.GetAgentConfiguration()
			mode, allow := cfg.MCPToolPolicy.EffectiveMCPToolPolicy()
			return chat.AgentConfiguration{
				ChatAIEnabled: cfg.ChatAIEnabled,
				MCPToolMode:   mode,
				MCPToolAllow:  allow,
			}
		},
		BeginActivity:    a.beginActivity,
		EmitEvent:        a.EmitEvent,
		PublishChatEvent: a.PublishChatEvent,
		// Notificação nativa (Wails v3) quando o turno do chat termina e a
		// aba/janela do chat não está visível para o usuário.
		OnAssistantResponseComplete: a.notifyChatResponseComplete,
		OnAssistantResponseFailed:   a.notifyChatResponseFailed,
		SafeGo:                      a.safeGo,
		ChatConfigFile:              chatConfigFile,
		// Gate de consentimento das tools com efeito no computador
		// (core/mcp.ToolConsentFor) — pergunta localizada no chat, por ação.
		RequestToolConsent: a.RequestToolConsent,
		// Política UNIFICADA de anexos do chat IA (allowlist/tamanho), consultada
		// no servidor e cacheada; a validação das imagens anexadas usa isto.
		GetAttachmentPolicy: func() chat.AttachmentPolicy {
			settings := a.cachedAIChatAttachmentSettings()
			return chat.AttachmentPolicy{
				Enabled:             settings.Enabled,
				AllowedContentTypes: settings.AllowedContentTypes,
				MaxFileSizeBytes:    settings.MaxFileSizeBytes,
			}
		},
	})
	a.psadtSvc = psadt.New(psadt.Deps{
		Logf: func(line string) {
			a.Logs.Append(line)
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
	a.chatSvc.Service().SetLogger(func(line string) {
		a.Logs.Append("[chat] " + line)
	})

	// Persistência de logs: banco unificado em %ProgramData%\Discovery\logs
	// (logs.db). O arquivo texto passa a ser apenas fallback de abertura. O
	// serviço também registra o sink do slog em RunServiceMode.
	a.initLogPersistence()
	a.chatSvc.LoadPersistedConfig()
	// Carrega o debug_config.json (o que o usuário salvou na página de Debug —
	// C:\ProgramData\Discovery) ANTES do config de produção. O loader de
	// produção (ApplyRuntimeConnectionConfig) parte da config atual e sobrescreve
	// apenas os campos de conexão quando o config.json traz credenciais; quando
	// o config.json não tem credenciais (bootstrap pendente), o que foi salvo no
	// Debug sobrevive ao restart em vez de ser perdido (bug da loja de apps:
	// "configuração de servidor API incompleta" até reiniciar o agente/ui).
	// Somente o processo de serviço (ServiceMode) lê/decifra segredos do disco;
	// a UI companion obtém a config redigida do serviço via IPC.
	if opts.ServiceMode {
		// O diretório de dados precisa ser CONFIÁVEL antes de qualquer leitura ou
		// validação de ACL dos arquivos de segredo. Instalações antigas deixaram
		// Users:(M) herdado em %ProgramData%\Discovery, e o debug_config.json era
		// rejeitado como "diretorio pai nao confiavel" e quarentenado a cada boot
		// (o que também derrubava o cloud-bootstrap P2P e o sync-manifest).
		// Best-effort: falha vira aviso e o boot continua.
		if err := platform.HardenDataDirACL(platform.DataDir()); err != nil {
			a.Logs.Append("[config] aviso: nao foi possivel endurecer a ACL do diretorio de dados: " + err.Error())
		}
		a.DebugSvc.LoadPersistedConfig()
		a.DebugSvc.LoadConnectionConfigFromProduction()
		// Alteração externa do debug_config.json (admin/editores) precisa reaplicar
		// no core sem reiniciar: registra o hook e liga o watcher (idempotente).
		a.StartDebugConfigWatcher()
	}
	a.initChatLogger()

	mcp.RegisterDiscoveryTools(reg, a)

	a.QueuedForceHeartbeat.Store(false)

	if opts.DebugMode {
		a.Logs.Append("[startup] modo debug ativo por tecla de atalho (execução atual)")
	}

	agentconfig.NormalizePSADTConfigDefaults(&a.AgentConfig.PSADT)
	agentconfig.NormalizeRolloutDefaults(&a.AgentConfig.Rollout)
}
