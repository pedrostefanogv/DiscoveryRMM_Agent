package app

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"discovery/app/consolidation"
	"discovery/app/core/database"
	"discovery/app/core/platform"
	"discovery/app/core/runtimediag"
	"discovery/app/core/safego"
	appinventory "discovery/app/inventory"
)

// runtimeDiagInterval é o período do log de memória do runtime.
const runtimeDiagInterval = 30 * time.Minute

// inventoryHistoryRetentionDays / inventoryHistoryKeepLatest controlam a poda do
// histórico de inventário: mantém apenas as K linhas mais recentes de cada
// agent_id (a mais recente é a base do delta sync) e, como segundo limite,
// remove o que passar de N dias. O K é pequeno de propósito: cada linha tem
// ~840 KB e o histórico chegou a 42 MB em 20 h antes desta poda.
const (
	inventoryHistoryRetentionDays = 7
	inventoryHistoryKeepLatest    = 5
)

// logFilePersistenceEnabled informa se o processo deve persistir logs no
// arquivo do agente.
//
// Em binários de teste devolve false: os testes criam Apps reais (NewApp) e
// passavam a escrever linhas de startup e de falhas simuladas no agent.log de
// PRODUÇÃO (C:ProgramDataDiscoverylogsagent.log), poluindo o log do
// usuário. A flag "test.v" só existe quando o pacote testing está linkado
// (binário do go test); o agente em produção nunca a registra.
func logFilePersistenceEnabled() bool {
	return flag.Lookup("test.v") == nil
}

// RunCore inicia o ciclo de vida do App sem depender de tipos do Wails.
// Usado pelo modo serviço (cmd/discovery-service) e pela UI (via
// ServiceStartup). No modo serviço, startup() já pula os itens de UI.
func (a *App) RunCore(ctx context.Context) error {
	a.startup(ctx)
	// Watchdog da UI companion: só o serviço pode relançá-la — na UI
	// standalone este próprio processo É a interface.
	if a.RuntimeFlags.ServiceMode {
		a.startUIWatchdog(a.Ctx())
	}
	return nil
}

// ServiceShutdown é chamado pelo Wails v3 durante o encerramento.
func (a *App) ServiceShutdown() error {
	// Encerra o serviço nativo antes do teardown (mesma ordem que o Wails
	// aplicaria a um serviço registrado: encerramento inverso ao startup).
	a.stopNativeNotificationService()
	a.shutdown()
	return nil
}

func (a *App) startup(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	a.ctx = ctx
	a.cancel = cancel

	captureStdLog(a.Logs.Buffer)

	// Diagnóstico de memória do runtime (heap/sys/GC/goroutines). O agente não
	// tinha NENMA instrumentação de heap; sem isso não há como acompanhar o
	// consumo em campo nem validar as otimizações de RAM. Uma linha a cada 30
	// minutos (e uma imediata) é irrelevante em custo.
	a.safeGo(func() {
		runtimediag.Log(ctx, runtimeDiagInterval, func(line string) {
			a.Logs.Append(line)
			if a.RuntimeFlags.DebugMode {
				log.Println(line)
			}
		})
	})

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
	// ── Remote debug: lifecycle no processo que recebe os comandos ──
	//
	// BUG 2026-09-24 (debug remoto nunca envia logs): o lifecycle deste domínio
	// era iniciado APENAS pelo adapter Wails v3 (remote_services.go), que só é
	// registrado no main.go do binário da UI. No binário do serviço
	// (discovery-service.exe → RunServiceMode → RunCore → runCoreStartup) não
	// existe aplicação Wails, então remotedebug.Manager.started permanecia
	// false. Todo comando "remotedebug" era recusado no primeiro check de
	// startSession — ANTES do primeiro logf — resultando em:
	//   [cmd] recebido: cmdType="remotedebug" ...
	//   result NATS publicado ... exitCode=1
	// e nenhuma linha [remote-debug] no agent-service.log. O servidor criava a
	// sessão, o agente respondia erro e o console ficava em "Aguardando
	// entradas de log..." para sempre.
	//
	// O serviço é quem consome os comandos do NATS, logo é aqui que o domínio
	// precisa estar vivo. Startup é idempotente (a primeira chamada vence).
	if a.RemoteDebug != nil {
		if err := a.RemoteDebug.Startup(ctx); err != nil {
			log.Printf("[service] FALHA ao iniciar remote debug: %v", err)
		} else {
			log.Println("[service] remote debug pronto (lifecycle iniciado)")
		}
	}

	dataDir := GetDataDir()
	db, err := database.Open(dataDir)
	if err != nil {
		log.Printf("[service] AVISO: falha ao abrir database: %v", err)
	} else {
		a.CoreAgent.DB = db
		// WAL limitado a 4 MB e checkpoint/optimize periódicos (30 min).
		db.StartMaintenance(database.DefaultMaintenanceInterval)
		log.Printf("[service] database SQLite inicializado em %s", dataDir)

		if a.CatalogClient != nil {
			a.CatalogClient.SetDatabase(db)
		}
		if a.AutomationSvc != nil {
			a.AutomationSvc.SetDB(db)
		}
		if a.InventorySvc != nil {
			a.InventorySvc.SetDB(db)
			// Cache local do scan de Windows Update (diff de payload + sonda de
			// reinício). *database.DB implementa inventory.WindowsUpdateCache.
			a.InventorySvc.SetWindowsUpdateDeps(appinventory.WindowsUpdateDeps{Cache: db})
			_ = a.InventorySvc.Startup(ctx)
		}
		if a.SupportSvc != nil {
			a.SupportSvc.SetDB(db)
		}
		agentIDForEngine := strings.TrimSpace(a.getDebugConfigInternal().AgentID)
		a.ConsolEngine = consolidation.New(db, agentIDForEngine)
	}

	// Retoma um descomissionamento interrompido (pedido gravado no SQLite mas
	// uninstaller ainda não lançado — ex.: serviço caiu durante o atraso de
	// resposta ao comando) antes de religar o core.
	a.resumeAgentDecommissionIfPending()

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

	// No modo serviço o agentConn/NATS é antecipado: o agente precisa ficar
	// alcançável (comandos remotos) o quanto antes a partir do boot. O
	// inventário continua atrasado para não saturar CPUs modestas.
	agentConnDelay := startupPhaseAgentConn
	if a.RuntimeFlags.ServiceMode {
		agentConnDelay = serviceAgentConnDelay()
		log.Printf("[startup] modo serviço: agentConn/NATS antecipado para %s", agentConnDelay)
	}

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
		case <-time.After(agentConnDelay):
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

		log.Printf("[startup] agentConn/NATS iniciando (serviceMode=%t, atraso=%s)", a.RuntimeFlags.ServiceMode, agentConnDelay)
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

		// Auto-update da Loja (F2): SÓ em ServiceMode — na UI companion quem
		// executa é o serviço. Warm-up de 5 min + ciclos de 6h com jitter ≤ 30 min.
		if a.AutoUpdateSvc != nil && a.RuntimeFlags.ServiceMode {
			a.safeGo(func() {
				a.AutoUpdateSvc.Run(ctx)
			})
		}

		// Bootstrap do PSADT removido: o agente usa notificações nativas e
		// deploys via winget/msiexec — não depende mais do módulo PSADT.

		// Health-check da API HTTP: NATS (nativo/wss) OU API fora ⇒ agent
		// offline. Roda no core (serviço), que é quem tem o agentConn e o
		// ApiClient; a UI recebe o estado pelo snapshot/evento via IPC.
		a.safeGo(func() {
			a.runAPIHealthProbe(ctx)
		})

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
			// Modo efetivo da verificação mTLS de PEERS: default "compat" (aceita
			// peer ainda sem certificado, sem marcá-lo como verificado). Logar aqui
			// evita um fallback silencioso — um DISCOVERY_P2P_MTLS_MODE inválido
			// (ex.: "true"/"1") cai em compat sem aviso.
			a.Logs.Append(fmt.Sprintf("[p2p] verificacao mTLS de peers=%s", a.GetP2PConfig().MtlsModeEffective()))
			a.LoadCachedAgentMtlsIdentity()
			a.safeGo(func() {
				a.StartMtlsEnrollmentLoop(ctx)
			})
			if !isAgentConfigured() && a.zeroTouchConfigRegistrationAllowed() {
				a.safeGo(func() {
					a.RunOnboardingLoop(ctx)
				})
			}
			a.safeGo(func() {
				a.P2PCoord.Run(ctx)
			})
			// Telemetria P2P: envia um snapshot a cada p2pTelemetryInterval e
			// drena o outbox offline. Este chamador existia no startup da UI e
			// foi removido na refatoracao M5 (commit a6ddadd), quando o core
			// passou a viver apenas no servico. Sem ele, nenhuma telemetria P2P
			// chega ao servidor e as metricas do dashboard ficam zeradas.
			a.safeGo(func() {
				a.StartP2PTelemetryLoop(ctx)
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

		// runCleanup roda a limpeza de outboxes e a poda do histórico de
		// inventário. Executa UMA vez já no boot (máquinas antigas podem ter
		// centenas de MB acumulados) e depois a cada 6 h.
		runCleanup := func() {
			if a.CoreAgent.DB == nil {
				return
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

			// Inventário: o histórico crescia ~840 KB a CADA sync e não tinha
			// nenhuma poda (40 MB na máquina de referência). A poda por CONTAGEM é
			// a que morde de verdade (a idade sozinha deixava tudo, porque todas as
			// linhas eram recentes); a por idade fica como segundo limite.
			removed := int64(0)
			if n, err := a.CoreAgent.DB.PruneInventoryHistory(inventoryHistoryKeepLatest); err != nil {
				log.Printf("[inventory][cleanup] erro na poda por contagem: %v", err)
			} else {
				removed += n
			}
			if n, err := a.CoreAgent.DB.CleanOldInventoryKeepingLatest(
				inventoryHistoryRetentionDays, inventoryHistoryKeepLatest,
			); err != nil {
				log.Printf("[inventory][cleanup] erro na poda por idade: %v", err)
			} else {
				removed += n
			}
			if removed > 0 {
				log.Printf("[inventory][cleanup] snapshots removidos: %d", removed)
				if err := a.CoreAgent.DB.ShrinkMemory(); err != nil {
					log.Printf("[inventory][cleanup] shrink_memory: %v", err)
				}
				// Devolve o espaço ao arquivo na hora: a poda inicial pode remover
				// dezenas de MB (medido: 42 MB de histórico) e sem VACUUM o arquivo
				// ficaria no tamanho do pico até a manutenção periódica de 30 min.
				if compacted, err := a.CoreAgent.DB.CompactIfBloated(0.25); err != nil {
					log.Printf("[inventory][cleanup] compactação: %v", err)
				} else if compacted {
					log.Println("[inventory][cleanup] VACUUM executado após a poda")
				}
			}
		}

		runCleanup()

		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runCleanup()
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

	// ── Gerenciador de Patches do Windows (WUA) ─────────────────────────
	// Scan completo periódico: atraso inicial com jitter (evita tempestade de
	// scans de WUA na frota logo após o boot) e ciclo configurável (default 6h).
	// É a REDE DE SEGURANÇA: o caminho rápido é a sonda de estado abaixo.
	a.safeGo(func() {
		initialDelay := windowsUpdateInitialDelay(ctx, a)
		select {
		case <-ctx.Done():
			return
		case <-time.After(initialDelay):
		}
		for {
			a.runWindowsUpdateScan(ctx)
			interval := a.windowsUpdateScanInterval()
			// Jitter em TODOS os ciclos (não só no primeiro): frota provisionada
			// pela mesma imagem fazia rajada de scans a cada 6h.
			next := windowsUpdateScanIntervalWithJitter(strings.TrimSpace(a.getDebugConfigInternal().AgentID), interval)
			log.Printf("[windows-update] proximo scan completo em %s (intervalo base %s)",
				next.Round(time.Second), interval)
			select {
			case <-ctx.Done():
				return
			case <-time.After(next):
			}
		}
	})

	// Sonda BARATA de estado do Windows Update (~5 min, configurável). Lê
	// registro/log/SCM em poucos ms e, quando a assinatura muda — download
	// concluído, reboot pendente, KB instalada, serviço caiu — dispara scan
	// completo + upload na hora. Sem isso o servidor ficava até 6h com dado
	// velho ("Baixando - 0%", "Instalação pendente").
	a.safeGo(func() {
		initialDelay := 2 * time.Minute
		select {
		case <-ctx.Done():
			return
		case <-time.After(initialDelay):
		}
		a.runWindowsUpdateStateProbe(ctx)
		for {
			interval := a.windowsUpdateStateInterval()
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
			a.runWindowsUpdateStateProbe(ctx)
		}
	})

	// Sonda de apps de terceiros (~3h, jitter): compara o inventário com o
	// último upload e dispara scan+upload quando um app passa a ter atualização.
	// Sem isso o app só apareceria no scan completo de 6h.
	a.safeGo(func() {
		initialDelay := 25*time.Minute + windowsUpdateAgentJitter(strings.TrimSpace(a.getDebugConfigInternal().AgentID), 10*time.Minute)
		select {
		case <-ctx.Done():
			return
		case <-time.After(initialDelay):
		}
		for {
			a.runThirdPartyInventoryProbe(ctx)
			next := thirdPartyProbeInterval(strings.TrimSpace(a.getDebugConfigInternal().AgentID))
			select {
			case <-ctx.Done():
				return
			case <-time.After(next):
			}
		}
	})

	// Sonda de reinício pendente: barata (somente registro), reportada só em
	// mudança. Alimenta o badge "Reinício pendente" em /agents.
	// O atraso inicial é diferente do scan completo (10-15 min + jitter) e da
	// sonda de estado (2 min): se coincidirem, o WUClient/TrustedInstaller é
	// consultado três vezes em poucos segundos.
	a.safeGo(func() {
		const pendingRebootInterval = 10 * time.Minute
		initialDelay := 7 * time.Minute
		select {
		case <-ctx.Done():
			return
		case <-time.After(initialDelay):
		}
		ticker := time.NewTicker(pendingRebootInterval)
		defer ticker.Stop()
		a.reportPendingReboot(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.reportPendingReboot(ctx)
			}
		}
	})

	// Apply startup throttle config from agent configuration (if already loaded).
	a.applyStartupThrottleConfig()
}

// serviceAgentConnDelay resolve o atraso da fase agentConn/NATS no modo
// serviço. Default 1s (agente alcançável o quanto antes no boot), com override
// por DISCOVERY_SERVICE_AGENTCONN_DELAY_MS (0..60000 ms).
func serviceAgentConnDelay() time.Duration {
	const defaultMS = 1000
	const maxMS = 60000

	raw := strings.TrimSpace(os.Getenv("DISCOVERY_SERVICE_AGENTCONN_DELAY_MS"))
	if raw == "" {
		return defaultMS * time.Millisecond
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		log.Printf("[startup] DISCOVERY_SERVICE_AGENTCONN_DELAY_MS inválido (%q) — usando %dms", raw, defaultMS)
		return defaultMS * time.Millisecond
	}
	if n > maxMS {
		n = maxMS
	}
	return time.Duration(n) * time.Millisecond
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

	// Cancela qualquer power silencioso pendente (timer interno) para não
	// disparar shutdown.exe durante/depois do teardown.
	a.cancelSilentPowerAction()

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
	// NOTA: no modo UI (companion) remoteDebug e remoteSessionMgr são Services
	// Wails v3 separados (adapters thin em remote_services.go) e o Wails encerra
	// os services na ordem inversa de registro — antes do App.
	//
	// No modo serviço NÃO há aplicação Wails: ninguém encerraria o remote debug,
	// então o encerramento é feito aqui. Shutdown é idempotente, portanto no
	// modo UI esta chamada é um no-op (o Wails já encerrou o service).
	if a.RemoteDebug != nil {
		_ = a.RemoteDebug.Shutdown()
	}

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
	// Fecha os bancos de log por último: o Close drena a fila (flush).
	if a.chatSvc != nil {
		a.chatSvc.Service().CloseChatLogger()
	}
	closeProcessLogStore()
}

func (a *App) GetStartupError() string {
	a.StartupMu.RLock()
	defer a.StartupMu.RUnlock()
	if a.StartupErr != nil {
		return a.StartupErr.Error()
	}
	return ""
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
