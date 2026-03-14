package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/energye/systray"
	"github.com/samber/lo"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"winget-store/internal/agentconn"
	"winget-store/internal/ai"
	"winget-store/internal/data"
	"winget-store/internal/database"
	"winget-store/internal/inventory"
	"winget-store/internal/mcp"
	"winget-store/internal/models"
	"winget-store/internal/printer"
	"winget-store/internal/processutil"
	"winget-store/internal/services"
	"winget-store/internal/watchdog"
	"winget-store/internal/winget"
)

var guidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Application-level constants for timeouts, URLs and window dimensions.
const (
	catalogURL       = "https://raw.githubusercontent.com/pedrostefanogv/winget-package-explo/refs/heads/main/public/data/packages.json"
	catalogTimeout   = 10 * time.Minute
	wingetTimeout    = 5 * time.Minute
	inventoryTimeout = 45 * time.Second
	printerTimeout   = 30 * time.Second
	chatConfigFile   = "chat_config.json"
	debugConfigFile  = "debug_config.json"

	WindowWidth  = 1280
	WindowHeight = 860
)

// getDataDir retorna o diretório de dados da aplicação
func getDataDir() string {
	if runtime.GOOS == "windows" {
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
	catalogSvc    *services.CatalogService
	catalogClient *data.HTTPClient
	appsSvc       *services.AppsService
	invSvc        *services.InventoryService
	printerSvc    *services.PrinterService

	db        *database.DB
	invCache  inventoryCache
	exportCfg exportConfig
	logs      logBuffer

	mcpRegistry *mcp.Registry
	chatSvc     *ai.Service
	agentConn   *agentconn.Runtime
	agentInfo   agentInfoCache
	watchdogSvc *watchdog.Watchdog

	debugMu     sync.RWMutex
	debugConfig DebugConfig

	startupMu   sync.RWMutex
	startupErr  error
	startupWg   sync.WaitGroup
	activityMu  sync.Mutex
	activeOps   int
	lastIdle    bool
	idleKnown   bool
	idleCapable bool
	closeMu     sync.RWMutex
	allowClose  bool
}

func NewApp() *App {
	catalogClient := data.NewHTTPClient(catalogURL, catalogTimeout)
	wingetClient := winget.NewClient(wingetTimeout)
	inventoryProvider := inventory.NewProvider(inventoryTimeout)
	printerManager := printer.NewManager(printerTimeout)

	reg := mcp.NewRegistry()
	chatSvc := ai.NewService(reg)

	// Initialize watchdog with default config
	watchdogSvc := watchdog.New(watchdog.DefaultConfig())

	a := &App{
		catalogSvc:    services.NewCatalogService(catalogClient),
		catalogClient: catalogClient,
		appsSvc:       services.NewAppsService(wingetClient),
		invSvc:        services.NewInventoryService(inventoryProvider),
		printerSvc:    services.NewPrinterService(printerManager),
		mcpRegistry:   reg,
		chatSvc:       chatSvc,
		watchdogSvc:   watchdogSvc,
	}
	inventoryProvider.SetProgressCallback(func() {
		a.pulseInventoryHeartbeat()
	})
	a.agentConn = agentconn.NewRuntime(agentconn.Options{
		LoadConfig: func() agentconn.Config {
			cfg := a.GetDebugConfig()
			return agentconn.Config{
				Scheme:    cfg.Scheme,
				Server:    cfg.Server,
				AuthToken: cfg.AuthToken,
				AgentID:   cfg.AgentID,
			}
		},
		Logf: func(format string, args ...any) {
			a.logs.append("[agent] " + fmt.Sprintf(format, args...))
		},
	})
	a.chatSvc.SetLogger(func(line string) {
		a.logs.append("[chat] " + line)
	})
	if logPath := strings.TrimSpace(os.Getenv("DISCOVERY_LOG_FILE")); logPath != "" {
		if err := a.logs.enableFilePersistence(logPath); err != nil {
			log.Printf("[startup] aviso: falha ao habilitar persistencia de logs em arquivo: %v", err)
		} else {
			a.logs.append("[startup] persistencia de logs habilitada em " + logPath)
		}
	}
	a.loadPersistedChatConfig()
	a.loadConnectionConfigFromProduction()

	// Register all Discovery tools in the MCP registry.
	mcp.RegisterDiscoveryTools(reg, a)

	// Register watchdog recovery actions for critical components
	a.registerWatchdogRecovery()

	return a
}

func (a *App) startup(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	a.ctx = ctx
	a.cancel = cancel

	// Start watchdog monitoring
	a.watchdogSvc.Start(ctx)
	log.Println("[startup] watchdog iniciado")

	a.startTray()
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
	}

	a.startupWg.Add(1)
	watchdog.SafeGoWithContext(ctx, "inventory-startup", a.watchdogSvc, watchdog.ComponentInventory, func(ctx context.Context) {
		defer a.startupWg.Done()
		done := a.beginActivity("inventario inicial")
		defer done()

		report, err := a.collectInventoryWithHeartbeat(ctx)
		if err != nil {
			log.Printf("[startup] falha ao coletar inventario em background: %v", err)
			a.startupMu.Lock()
			a.startupErr = err
			a.startupMu.Unlock()
			return
		}
		a.invCache.set(report)
		a.syncInventoryOnStartup(ctx, report)
	})

	a.startupWg.Add(1)
	watchdog.SafeGoWithContext(ctx, "agent-connection", a.watchdogSvc, watchdog.ComponentAgent, func(ctx context.Context) {
		defer a.startupWg.Done()

		// Bootstrap pós-instalação: se houver URL/KEY do instalador, resolver token/agentId.
		a.bootstrapAgentCredentialsFromInstallerConfig(ctx)

		// Periodic heartbeat for agent connection
		heartbeat := watchdog.NewPeriodicHeartbeat(a.watchdogSvc, watchdog.ComponentAgent, 25*time.Second)
		heartbeat.Start(ctx)
		defer heartbeat.Stop()

		a.agentConn.Run(ctx)
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

// clearMemoryCaches limpa caches em memória para economizar recursos quando
// o app está minimizado no tray. Os dados persistem no SQLite e serão
// recarregados quando necessário.
func (a *App) clearMemoryCaches() {
	// Limpar cache de AgentInfo em memória (mantém no SQLite)
	a.agentInfo.invalidate()

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

func debugConfigPathCandidates() []string {
	paths := make([]string, 0, 4)

	if runtime.GOOS == "windows" {
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			paths = append(paths, filepath.Join(localAppData, "Discovery", debugConfigFile))
		}
	}

	if exe, err := os.Executable(); err == nil && strings.TrimSpace(exe) != "" {
		paths = append(paths, filepath.Join(filepath.Dir(exe), debugConfigFile))
	}

	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		paths = append(paths, filepath.Join(home, ".discovery", debugConfigFile))
	}

	paths = append(paths, filepath.Join(".", debugConfigFile))
	return lo.Uniq(paths)
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
