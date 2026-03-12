package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
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
	"winget-store/internal/export"
	"winget-store/internal/inventory"
	"winget-store/internal/mcp"
	"winget-store/internal/models"
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
	chatConfigFile   = "chat_config.json"
	debugConfigFile  = "debug_config.json"

	WindowWidth  = 1280
	WindowHeight = 860
)

// inventoryCache manages thread-safe caching of the last inventory report.
type inventoryCache struct {
	mu     sync.RWMutex
	report models.InventoryReport
	loaded bool
}

func (c *inventoryCache) get() (models.InventoryReport, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.report, c.loaded
}

func (c *inventoryCache) set(r models.InventoryReport) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.report = r
	c.loaded = true
}

// exportConfig holds the current export options.
type exportConfig struct {
	mu     sync.RWMutex
	redact bool
}

func (e *exportConfig) get() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.redact
}

func (e *exportConfig) set(v bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.redact = v
}

// AgentInfo holds key identifiers resolved from the server for the connected agent.
type AgentInfo struct {
	AgentID  string `json:"agentId"`
	ClientID string `json:"clientId"`
	SiteID   string `json:"siteId"`
	Hostname string `json:"hostname"`
	Name     string `json:"displayName"`
}

// APIWorkflowState is the workflow state embedded in a ticket response.
type APIWorkflowState struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Color        string `json:"color"`
	IsInitial    bool   `json:"isInitial"`
	IsFinal      bool   `json:"isFinal"`
	DisplayOrder int    `json:"displayOrder"`
}

func (w *APIWorkflowState) UnmarshalJSON(data []byte) error {
	type alias APIWorkflowState
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	var out alias
	out.ID = strings.TrimSpace(fmt.Sprint(raw["id"]))
	out.Name = strings.TrimSpace(fmt.Sprint(raw["name"]))
	out.Color = strings.TrimSpace(fmt.Sprint(raw["color"]))
	out.IsInitial = toBool(raw["isInitial"], raw["initial"])
	out.IsFinal = toBool(raw["isFinal"], raw["final"], raw["isTerminal"])
	out.DisplayOrder = toInt(raw["displayOrder"], raw["order"], raw["sortOrder"], raw["position"])
	*w = APIWorkflowState(out)
	return nil
}

// TicketPriority normalizes priority values from API responses.
// The backend may return integer (1..4) or enum strings (Low..Critical).
type TicketPriority int

func (p *TicketPriority) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*p = TicketPriority(0)
		return nil
	}

	var n int
	if err := json.Unmarshal(data, &n); err == nil {
		*p = TicketPriority(normalizePriority(n))
		return nil
	}

	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*p = TicketPriority(priorityLabelToInt(s))
		return nil
	}

	return fmt.Errorf("prioridade inválida")
}

// APITicket is the ticket representation returned by the remote API.
type APITicket struct {
	ID            string            `json:"id"`
	Title         string            `json:"title"`
	Description   string            `json:"description"`
	Priority      TicketPriority    `json:"priority"`
	Category      *string           `json:"category,omitempty"`
	AgentID       *string           `json:"agentId,omitempty"`
	ClientID      string            `json:"clientId"`
	SiteID        *string           `json:"siteId,omitempty"`
	CreatedAt     string            `json:"createdAt"`
	WorkflowState *APIWorkflowState `json:"workflowState,omitempty"`
	Rating        *int              `json:"rating,omitempty"`
	RatedAt       *string           `json:"ratedAt,omitempty"`
	RatedBy       *string           `json:"ratedBy,omitempty"`
}

// TicketComment is a comment on a ticket.
type TicketComment struct {
	ID         string `json:"id"`
	Author     string `json:"author"`
	Content    string `json:"content"`
	IsInternal bool   `json:"isInternal"`
	CreatedAt  string `json:"createdAt"`
}

// CreateTicketInput is the frontend-facing request to create a ticket.
type CreateTicketInput struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Priority    int    `json:"priority"` // 1=Baixa 2=Média 3=Alta 4=Crítica
	Category    string `json:"category"`
}

// CloseTicketInput is the frontend-facing request to close a ticket.
type CloseTicketInput struct {
	Rating          *int   `json:"rating,omitempty"`
	Comment         string `json:"comment,omitempty"`
	WorkflowStateID string `json:"workflowStateId,omitempty"`
}

// KnowledgeArticle represents a knowledge base article for support guidance.
type KnowledgeArticle struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Category    string   `json:"category"`
	Summary     string   `json:"summary"`
	Content     string   `json:"content"`
	Tags        []string `json:"tags"`
	Author      string   `json:"author"`
	Scope       string   `json:"scope"`
	PublishedAt string   `json:"publishedAt"`
	Difficulty  string   `json:"difficulty"`
	ReadTimeMin int      `json:"readTimeMin"`
	UpdatedAt   string   `json:"updatedAt"`
}

// agentInfoCache caches the agent identifiers resolved from /api/agent-auth/me.
type agentInfoCache struct {
	mu     sync.RWMutex
	info   AgentInfo
	loaded bool
}

func (c *agentInfoCache) get() (AgentInfo, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.info, c.loaded
}

func (c *agentInfoCache) set(info AgentInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.info = info
	c.loaded = true
}

func (c *agentInfoCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loaded = false
}

// logBuffer stores command output lines for the embedded terminal view.
type logBuffer struct {
	mu    sync.RWMutex
	lines []string
}

func (l *logBuffer) append(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, line)
	if len(l.lines) > 5000 {
		l.lines = l.lines[len(l.lines)-5000:]
	}
}

func (l *logBuffer) getAll() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]string, len(l.lines))
	copy(out, l.lines)
	return out
}

func (l *logBuffer) clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = nil
}

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

	reg := mcp.NewRegistry()
	chatSvc := ai.NewService(reg)

	// Initialize watchdog with default config
	watchdogSvc := watchdog.New(watchdog.DefaultConfig())

	a := &App{
		catalogSvc:    services.NewCatalogService(catalogClient),
		catalogClient: catalogClient,
		appsSvc:       services.NewAppsService(wingetClient),
		invSvc:        services.NewInventoryService(inventoryProvider),
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
	a.loadPersistedChatConfig()
	a.loadPersistedDebugConfig()

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

func (a *App) GetCatalog() (models.Catalog, error) {
	return a.catalogSvc.GetCatalog(a.ctx)
}

func (a *App) Install(id string) (string, error) {
	done := a.beginActivity("instalacao")
	defer done()
	a.logs.append("[install " + id + "] " + time.Now().Format("15:04:05"))
	out, err := a.appsSvc.Install(a.ctx, id)
	a.logs.append(out)
	return out, err
}

func (a *App) Uninstall(id string) (string, error) {
	done := a.beginActivity("desinstalacao")
	defer done()
	a.logs.append("[uninstall " + id + "] " + time.Now().Format("15:04:05"))
	out, err := a.appsSvc.Uninstall(a.ctx, id)
	a.logs.append(out)
	return out, err
}

func (a *App) Upgrade(id string) (string, error) {
	done := a.beginActivity("atualizacao")
	defer done()
	a.logs.append("[upgrade " + id + "] " + time.Now().Format("15:04:05"))
	out, err := a.appsSvc.Upgrade(a.ctx, id)
	a.logs.append(out)
	return out, err
}

func (a *App) UpgradeAll() (string, error) {
	done := a.beginActivity("atualizacao em lote")
	defer done()
	a.logs.append("[upgrade --all] " + time.Now().Format("15:04:05"))
	out, err := a.appsSvc.UpgradeAll(a.ctx)
	a.logs.append(out)
	return out, err
}

func (a *App) ListInstalled() (string, error) {
	done := a.beginActivity("listagem de instalados")
	defer done()
	out, err := a.appsSvc.ListInstalled(a.ctx)
	a.logs.append("[list] " + time.Now().Format("15:04:05"))
	a.logs.append(out)
	return out, err
}

func (a *App) pulseInventoryHeartbeat() {
	if a.watchdogSvc != nil {
		a.watchdogSvc.Heartbeat(watchdog.ComponentInventory)
	}
}

func (a *App) collectInventoryWithHeartbeat(ctx context.Context) (models.InventoryReport, error) {
	if a.watchdogSvc == nil {
		return a.invSvc.GetInventory(ctx)
	}

	heartbeat := watchdog.NewPeriodicHeartbeat(a.watchdogSvc, watchdog.ComponentInventory, 20*time.Second)
	heartbeat.Start(ctx)
	defer heartbeat.Stop()

	return a.invSvc.GetInventory(ctx)
}

func (a *App) GetInventory() (models.InventoryReport, error) {
	done := a.beginActivity("coleta de inventario")
	defer done()
	if cached, ok := a.invCache.get(); ok {
		return cached, nil
	}

	report, err := a.collectInventoryWithHeartbeat(a.ctx)
	if err != nil {
		return models.InventoryReport{}, err
	}
	a.invCache.set(report)
	return report, nil
}

func (a *App) RefreshInventory() (models.InventoryReport, error) {
	done := a.beginActivity("atualizacao de inventario")
	defer done()
	report, err := a.collectInventoryWithHeartbeat(a.ctx)
	if err != nil {
		return models.InventoryReport{}, err
	}
	a.invCache.set(report)
	return report, nil
}

func (a *App) GetOsqueryStatus() (models.OsqueryStatus, error) {
	return inventory.GetOsqueryStatus(), nil
}

func (a *App) InstallOsquery() (string, error) {
	status := inventory.GetOsqueryStatus()
	if status.Installed {
		if status.Path != "" {
			return "osquery ja instalado em " + status.Path, nil
		}
		return "osquery ja instalado", nil
	}

	return a.appsSvc.Install(a.ctx, status.SuggestedPackageID)
}

// GetPendingUpdates runs `winget upgrade` and parses the output into structured items.
func (a *App) GetPendingUpdates() ([]models.UpgradeItem, error) {
	done := a.beginActivity("checagem de atualizacoes")
	defer done()
	raw, _ := a.appsSvc.ListUpgradable(a.ctx)
	a.logs.append("[winget upgrade] " + time.Now().Format("15:04:05"))
	a.logs.append(raw)
	items := parseUpgradeOutput(raw)
	return items, nil
}

// GetLogs returns the accumulated command log lines.
func (a *App) GetLogs() []string {
	return a.logs.getAll()
}

// ClearLogs empties the log buffer.
func (a *App) ClearLogs() {
	a.logs.clear()
}

// parseUpgradeOutput parses the tabular output of `winget upgrade`.
func parseUpgradeOutput(raw string) []models.UpgradeItem {
	// winget emits progress spinners using bare \r (no \n) to overwrite the same
	// terminal line. This means the spinner content and the actual table header end
	// up in the same \n-delimited segment. Simulate terminal CR-overwrite: for each
	// \n-terminated line keep only the last \r-delimited non-empty segment.
	rawLines := strings.Split(raw, "\n")
	lines := make([]string, 0, len(rawLines))
	for _, l := range rawLines {
		parts := strings.Split(l, "\r")
		last := ""
		for j := len(parts) - 1; j >= 0; j-- {
			if strings.TrimSpace(parts[j]) != "" {
				last = parts[j]
				break
			}
		}
		lines = append(lines, last)
	}

	var items []models.UpgradeItem
	headerIdx := -1

	// Find the header line (contains "Name" and "Id" and "Version")
	for i, line := range lines {
		lower := strings.ToLower(line)
		if (strings.Contains(lower, "name") || strings.Contains(lower, "nome")) &&
			(strings.Contains(lower, "id")) &&
			(strings.Contains(lower, "version") || strings.Contains(lower, "vers")) {
			headerIdx = i
			break
		}
	}
	if headerIdx < 0 || headerIdx+1 >= len(lines) {
		return items
	}

	// Find the separator line (dashes)
	dataStart := headerIdx + 1
	if dataStart < len(lines) && strings.Count(lines[dataStart], "-") > 10 {
		dataStart++
	}

	// Parse column positions from header
	header := lines[headerIdx]
	idCol := findColumnStart(header, "Id")
	if idCol < 0 {
		idCol = findColumnStart(header, "ID")
	}
	verCol := findColumnStart(header, "Version")
	if verCol < 0 {
		verCol = findColumnStart(header, "Vers")
	}
	availCol := findColumnStart(header, "Available")
	if availCol < 0 {
		availCol = findColumnStart(header, "Dispon")
	}
	srcCol := findColumnStart(header, "Source")
	if srcCol < 0 {
		srcCol = findColumnStart(header, "Fonte")
	}

	for _, line := range lines[dataStart:] {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		// Skip summary lines like "X upgrades available"
		lower := strings.ToLower(line)
		if strings.Contains(lower, "upgrade") || strings.Contains(lower, "atualiza") {
			continue
		}

		item := models.UpgradeItem{}
		if idCol > 0 {
			item.Name = strings.TrimSpace(safeSubstring(line, 0, idCol))
		}
		if idCol >= 0 && verCol > idCol {
			item.ID = strings.TrimSpace(safeSubstring(line, idCol, verCol))
		}
		if verCol >= 0 && availCol > verCol {
			item.CurrentVersion = strings.TrimSpace(safeSubstring(line, verCol, availCol))
		}
		if availCol >= 0 {
			if srcCol > availCol {
				item.AvailableVersion = strings.TrimSpace(safeSubstring(line, availCol, srcCol))
			} else {
				item.AvailableVersion = strings.TrimSpace(safeSubstring(line, availCol, len(line)))
			}
		}
		if srcCol >= 0 {
			item.Source = strings.TrimSpace(safeSubstring(line, srcCol, len(line)))
		}

		if item.ID != "" {
			items = append(items, item)
		}
	}
	return items
}

func findColumnStart(header, keyword string) int {
	idx := strings.Index(header, keyword)
	if idx < 0 {
		idx = strings.Index(strings.ToLower(header), strings.ToLower(keyword))
	}
	return idx
}

func safeSubstring(s string, start, end int) string {
	runes := []rune(s)
	if start >= len(runes) {
		return ""
	}
	if end > len(runes) {
		end = len(runes)
	}
	if start < 0 {
		start = 0
	}
	return string(runes[start:end])
}
func (a *App) SetExportRedaction(redact bool) {
	a.exportCfg.set(redact)
}

func (a *App) getRedact() bool {
	return a.exportCfg.get()
}

func (a *App) ExportInventoryMarkdown() (string, error) {
	done := a.beginActivity("exportacao markdown")
	defer done()
	report, err := a.getInventoryForExport()
	if err != nil {
		return "", err
	}

	content := export.BuildMarkdown(report, a.getRedact())
	stamp := time.Now().Format("20060102-150405")
	fileName := "inventory-" + stamp + ".md"

	path, err := writeWithFallback(fileName, func(outPath string) error {
		return os.WriteFile(outPath, []byte(content), 0o644)
	})
	if err != nil {
		return "", err
	}

	return path, nil
}

func (a *App) ExportInventoryPDF() (string, error) {
	done := a.beginActivity("exportacao pdf")
	defer done()
	report, err := a.getInventoryForExport()
	if err != nil {
		return "", err
	}

	stamp := time.Now().Format("20060102-150405")
	fileName := "inventory-" + stamp + ".pdf"

	path, err := writeWithFallback(fileName, func(outPath string) error {
		return export.WritePDF(report, outPath, a.getRedact())
	})
	if err != nil {
		return "", err
	}

	return path, nil
}

func (a *App) getInventoryForExport() (models.InventoryReport, error) {
	if cached, ok := a.invCache.get(); ok {
		return cached, nil
	}

	report, err := a.invSvc.GetInventory(a.ctx)
	if err != nil {
		return models.InventoryReport{}, err
	}
	a.invCache.set(report)
	return report, nil
}

func writeWithFallback(fileName string, writer func(outPath string) error) (string, error) {
	candidates := exportDirCandidates()
	errs := make([]string, 0, len(candidates))

	for _, dir := range candidates {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			errs = append(errs, dir+": "+err.Error())
			continue
		}
		outPath := filepath.Join(dir, fileName)
		if err := writer(outPath); err != nil {
			errs = append(errs, dir+": "+err.Error())
			continue
		}
		return outPath, nil
	}

	if len(errs) == 0 {
		return "", fmt.Errorf("nenhuma pasta de exportacao disponivel")
	}
	return "", fmt.Errorf("falha ao exportar; tentativas: %s", strings.Join(errs, " | "))
}

func exportDirCandidates() []string {
	paths := make([]string, 0, 5)

	if exe, err := os.Executable(); err == nil && strings.TrimSpace(exe) != "" {
		paths = append(paths, filepath.Join(filepath.Dir(exe), "DiscoveryExports"))
	}

	if runtime.GOOS == "windows" {
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			paths = append(paths, filepath.Join(localAppData, "Discovery", "Exports"))
		}
	}

	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		paths = append(paths, filepath.Join(home, "Documents", "DiscoveryExports"))
		paths = append(paths, filepath.Join(home, "DiscoveryExports"))
	}

	paths = append(paths, filepath.Join(".", "DiscoveryExports"))
	return lo.Uniq(paths)
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

func (a *App) loadPersistedDebugConfig() {
	for _, path := range debugConfigPathCandidates() {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var cfg DebugConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			a.logs.append("[debug] falha ao ler configuracao persistida: " + err.Error())
			return
		}

		if !isValidDebugScheme(cfg.Scheme) {
			a.logs.append("[debug] configuracao persistida ignorada: scheme invalido")
			return
		}

		a.debugMu.Lock()
		a.debugConfig = cfg
		a.debugMu.Unlock()
		a.logs.append("[debug] configuracao carregada de " + path)
		return
	}
}

func (a *App) persistDebugConfig(cfg DebugConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("falha ao serializar configuracao de debug: %w", err)
	}

	var errs []string
	for _, path := range debugConfigPathCandidates() {
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			errs = append(errs, dir+": "+err.Error())
			continue
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			errs = append(errs, path+": "+err.Error())
			continue
		}
		a.logs.append("[debug] configuracao salva em " + path)
		return nil
	}

	if len(errs) == 0 {
		return fmt.Errorf("nenhum caminho valido para salvar configuracao de debug")
	}
	return fmt.Errorf("falha ao salvar configuracao de debug: %s", strings.Join(errs, " | "))
}

// DebugConfig holds server connection settings for the debug page.
type DebugConfig struct {
	// API Server (HTTP/HTTPS) - para tickets, inventário, etc
	ApiScheme string `json:"apiScheme"` // "http" or "https"
	ApiServer string `json:"apiServer"` // hostname:port or IP
	AuthToken string `json:"authToken"` // bearer token

	// NATS Server - para comandos de agente
	NatsServer string `json:"natsServer"` // hostname:port for NATS
	AgentID    string `json:"agentId"`    // agent identifier

	// Deprecated: mantidos para compatibilidade com agentConn
	Scheme string `json:"scheme,omitempty"` // "http", "https" or "nats"
	Server string `json:"server,omitempty"` // hostname:port or IP
}

// GetDebugConfig returns the current debug configuration.
func (a *App) GetDebugConfig() DebugConfig {
	a.debugMu.RLock()
	defer a.debugMu.RUnlock()
	return a.debugConfig
}

// AgentStatus is the frontend-facing agent connection snapshot.
type AgentStatus struct {
	Connected bool   `json:"connected"`
	AgentID   string `json:"agentId"`
	Server    string `json:"server"`
	LastEvent string `json:"lastEvent"`
}

// RealtimeStatus represents server-side realtime transport health.
type RealtimeStatus struct {
	NATSConnected          bool      `json:"natsConnected"`
	SignalRConnectedAgents int       `json:"signalrConnectedAgents"`
	CheckedAtUTC           time.Time `json:"checkedAtUtc"`
}

// GetAgentStatus returns the current agent connectivity status.
func (a *App) GetAgentStatus() AgentStatus {
	if a.agentConn == nil {
		return AgentStatus{}
	}
	s := a.agentConn.GetStatus()
	return AgentStatus{
		Connected: s.Connected,
		AgentID:   s.AgentID,
		Server:    s.Server,
		LastEvent: s.LastEvent,
	}
}

// SetDebugConfig validates, stores and persists the debug connection settings.
func (a *App) SetDebugConfig(cfg DebugConfig) error {
	// Trim and normalize
	cfg.ApiScheme = strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
	cfg.ApiServer = strings.TrimSpace(cfg.ApiServer)
	cfg.NatsServer = strings.TrimSpace(cfg.NatsServer)
	cfg.AgentID = strings.TrimSpace(cfg.AgentID)
	cfg.AuthToken = strings.TrimSpace(cfg.AuthToken)

	// Validação do servidor API
	if cfg.ApiServer != "" {
		if cfg.ApiScheme != "http" && cfg.ApiScheme != "https" {
			return fmt.Errorf("apiScheme invalido: use 'http' ou 'https'")
		}
	}

	// Validação do servidor NATS
	if cfg.NatsServer != "" {
		if !guidPattern.MatchString(cfg.AgentID) {
			return fmt.Errorf("agentId invalido para NATS: informe um GUID valido")
		}
	}

	// Pelo menos um servidor deve estar configurado
	if cfg.ApiServer == "" && cfg.NatsServer == "" {
		return fmt.Errorf("configure pelo menos um servidor (API ou NATS)")
	}

	// Popula campos legados para compatibilidade com agentConn
	// Prioriza NATS se ambos estiverem configurados (agente precisa de comandos)
	if cfg.NatsServer != "" {
		cfg.Scheme = "nats"
		cfg.Server = cfg.NatsServer
	} else if cfg.ApiServer != "" {
		cfg.Scheme = cfg.ApiScheme
		cfg.Server = cfg.ApiServer
	}

	a.logs.append(fmt.Sprintf("[debug] atualizando configuracao: api=%s://%s nats=%s agentId=%s",
		cfg.ApiScheme, cfg.ApiServer, cfg.NatsServer, cfg.AgentID))

	a.debugMu.Lock()
	a.debugConfig = cfg
	a.debugMu.Unlock()

	// Invalidar cache em memória
	a.agentInfo.invalidate()

	// Invalidar cache em SQLite
	if a.db != nil {
		if err := a.db.CacheDelete("agent_info"); err != nil {
			log.Printf("[debug] aviso: falha ao limpar cache SQLite: %v", err)
		}
	}

	if err := a.persistDebugConfig(cfg); err != nil {
		a.logs.append("[debug] falha ao persistir configuracao: " + err.Error())
		return err
	}
	if a.agentConn != nil {
		a.logs.append("[debug] solicitando reload da conexao do agente")
		a.agentConn.Reload()
	}
	a.logs.append("[debug] configuracao aplicada com sucesso")
	return nil
}

// TestDebugConnection tests connectivity to configured servers and returns diagnostic info.
func (a *App) TestDebugConnection(cfg DebugConfig) (string, error) {
	cfg.ApiScheme = strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
	cfg.ApiServer = strings.TrimSpace(cfg.ApiServer)
	cfg.NatsServer = strings.TrimSpace(cfg.NatsServer)
	cfg.AgentID = strings.TrimSpace(cfg.AgentID)
	cfg.AuthToken = strings.TrimSpace(cfg.AuthToken)

	var results []string

	// Testa servidor API se configurado
	if cfg.ApiServer != "" {
		a.logs.append(fmt.Sprintf("[debug-test] testando API: %s://%s", cfg.ApiScheme, cfg.ApiServer))
		target := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/agent-auth/me"
		client := &http.Client{Timeout: 10 * time.Second}
		req, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			return "", fmt.Errorf("URL invalida para API: %w", err)
		}
		if cfg.AuthToken != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
		}
		if cfg.AgentID != "" {
			req.Header.Set("X-Agent-ID", cfg.AgentID)
		}

		resp, err := client.Do(req)
		if err != nil {
			wrapped := fmt.Errorf("falha ao conectar na API %s: %w", target, err)
			a.logs.append("[debug-test] " + wrapped.Error())
			return "", wrapped
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			wrapped := fmt.Errorf("erro ao ler resposta da API (%s): %w", resp.Status, err)
			a.logs.append("[debug-test] " + wrapped.Error())
			return "", wrapped
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			wrapped := fmt.Errorf("API HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
			a.logs.append("[debug-test] " + wrapped.Error())
			return "", wrapped
		}

		// Pretty-print JSON if possible
		var pretty interface{}
		if json.Unmarshal(body, &pretty) == nil {
			if formatted, err := json.MarshalIndent(pretty, "", "  "); err == nil {
				results = append(results, "=== Servidor API ===\n"+string(formatted))
			} else {
				results = append(results, "=== Servidor API ===\n"+string(body))
			}
		} else {
			results = append(results, "=== Servidor API ===\n"+string(body))
		}
		a.logs.append("[debug-test] teste API concluido com sucesso")
	}

	// Testa servidor NATS se configurado
	if cfg.NatsServer != "" {
		if !guidPattern.MatchString(cfg.AgentID) {
			err := fmt.Errorf("agentId invalido para NATS: informe um GUID valido")
			a.logs.append("[debug-test] " + err.Error())
			return "", err
		}
		a.logs.append(fmt.Sprintf("[debug-test] testando NATS: %s", cfg.NatsServer))
		out, err := agentconn.FetchNATSInfo(cfg.NatsServer, 10*time.Second)
		if err != nil {
			wrapped := fmt.Errorf("falha ao conectar no NATS %s: %w", cfg.NatsServer, err)
			a.logs.append("[debug-test] " + wrapped.Error())
			return "", wrapped
		}
		results = append(results, "=== Servidor NATS ===\n"+out)
		a.logs.append("[debug-test] teste NATS concluido com sucesso")
	}

	if len(results) == 0 {
		return "", fmt.Errorf("nenhum servidor configurado para testar")
	}

	return strings.Join(results, "\n\n"), nil
}

// GetRealtimeStatus queries /api/realtime/status from the configured HTTP server.
func (a *App) GetRealtimeStatus() (RealtimeStatus, error) {
	cfg := a.GetDebugConfig()
	cfg.ApiScheme = strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
	cfg.ApiServer = strings.TrimSpace(cfg.ApiServer)
	if cfg.ApiServer == "" {
		return RealtimeStatus{}, fmt.Errorf("servidor API nao configurado")
	}
	if cfg.ApiScheme != "http" && cfg.ApiScheme != "https" {
		return RealtimeStatus{}, fmt.Errorf("apiScheme invalido: use http ou https")
	}

	target := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/realtime/status"
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return RealtimeStatus{}, fmt.Errorf("URL invalida: %w", err)
	}
	if strings.TrimSpace(cfg.AuthToken) != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	}
	if strings.TrimSpace(cfg.AgentID) != "" {
		req.Header.Set("X-Agent-ID", cfg.AgentID)
	}

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return RealtimeStatus{}, fmt.Errorf("falha ao conectar em %s: %w", target, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return RealtimeStatus{}, fmt.Errorf("erro ao ler resposta (%s): %w", resp.Status, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return RealtimeStatus{}, fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var out RealtimeStatus
	if err := json.Unmarshal(body, &out); err != nil {
		return RealtimeStatus{}, fmt.Errorf("resposta invalida de /api/realtime/status: %w", err)
	}
	return out, nil
}

type agentHardwareEnvelope struct {
	Hostname               string                  `json:"hostname"`
	DisplayName            string                  `json:"displayName"`
	Status                 string                  `json:"status"`
	OperatingSystem        string                  `json:"operatingSystem"`
	OSVersion              string                  `json:"osVersion"`
	AgentVersion           string                  `json:"agentVersion"`
	LastIPAddress          string                  `json:"lastIpAddress"`
	MACAddress             string                  `json:"macAddress"`
	Hardware               agentHardwareInfo       `json:"hardware"`
	Components             agentHardwareComponents `json:"components"`
	InventoryRaw           json.RawMessage         `json:"inventoryRaw"`
	InventorySchemaVersion string                  `json:"inventorySchemaVersion"`
	InventoryCollectedAt   string                  `json:"inventoryCollectedAt"`
}

type agentHardwareComponents struct {
	Disks           []agentDiskInfo           `json:"disks"`
	NetworkAdapters []agentNetworkAdapterInfo `json:"networkAdapters"`
	MemoryModules   []agentMemoryModuleInfo   `json:"memoryModules"`
}

type agentHardwareInfo struct {
	InventoryRaw            json.RawMessage `json:"inventoryRaw"`
	InventorySchemaVersion  string          `json:"inventorySchemaVersion"`
	InventoryCollectedAt    string          `json:"inventoryCollectedAt"`
	Manufacturer            string          `json:"manufacturer"`
	Model                   string          `json:"model"`
	SerialNumber            string          `json:"serialNumber"`
	MotherboardManufacturer string          `json:"motherboardManufacturer"`
	MotherboardModel        string          `json:"motherboardModel"`
	MotherboardSerialNumber string          `json:"motherboardSerialNumber"`
	Processor               string          `json:"processor"`
	ProcessorCores          int             `json:"processorCores"`
	ProcessorThreads        int             `json:"processorThreads"`
	ProcessorArchitecture   string          `json:"processorArchitecture"`
	TotalMemoryBytes        int64           `json:"totalMemoryBytes"`
	BIOSVersion             string          `json:"biosVersion"`
	BIOSManufacturer        string          `json:"biosManufacturer"`
	OSName                  string          `json:"osName"`
	OSVersion               string          `json:"osVersion"`
	OSBuild                 string          `json:"osBuild"`
	OSArchitecture          string          `json:"osArchitecture"`
	CollectedAt             string          `json:"collectedAt"`
	UpdatedAt               string          `json:"updatedAt"`
}

type agentDiskInfo struct {
	DriveLetter    string `json:"driveLetter"`
	Label          string `json:"label"`
	FileSystem     string `json:"fileSystem"`
	TotalSizeBytes int64  `json:"totalSizeBytes"`
	FreeSpaceBytes int64  `json:"freeSpaceBytes"`
	MediaType      string `json:"mediaType"`
	CollectedAt    string `json:"collectedAt"`
}

type agentNetworkAdapterInfo struct {
	Name          string `json:"name"`
	MACAddress    string `json:"macAddress"`
	IPAddress     string `json:"ipAddress"`
	SubnetMask    string `json:"subnetMask"`
	Gateway       string `json:"gateway"`
	DNSServers    string `json:"dnsServers"`
	IsDhcpEnabled bool   `json:"isDhcpEnabled"`
	AdapterType   string `json:"adapterType"`
	Speed         string `json:"speed"`
	CollectedAt   string `json:"collectedAt"`
}

type agentMemoryModuleInfo struct {
	Slot          string `json:"slot"`
	CapacityBytes int64  `json:"capacityBytes"`
	SpeedMhz      int    `json:"speedMhz"`
	MemoryType    string `json:"memoryType"`
	Manufacturer  string `json:"manufacturer"`
	PartNumber    string `json:"partNumber"`
	SerialNumber  string `json:"serialNumber"`
	CollectedAt   string `json:"collectedAt"`
}

type agentSoftwareEnvelope struct {
	CollectedAt string              `json:"collectedAt"`
	Software    []agentSoftwareItem `json:"software"`
}

type agentSoftwareItem struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Publisher string `json:"publisher"`
	InstallID string `json:"installId"`
	Serial    string `json:"serial"`
	Source    string `json:"source"`
}

func (a *App) syncInventoryOnStartup(ctx context.Context, report models.InventoryReport) {
	a.pulseInventoryHeartbeat()
	cfg := a.GetDebugConfig()
	cfg.ApiServer = strings.TrimSpace(cfg.ApiServer)
	cfg.ApiScheme = strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
	if cfg.ApiServer == "" || strings.TrimSpace(cfg.AuthToken) == "" || strings.TrimSpace(cfg.AgentID) == "" {
		a.logs.append("[agent-sync] ignorado: faltam apiServer/token/agentId no Debug")
		return
	}
	if cfg.ApiScheme != "http" && cfg.ApiScheme != "https" {
		a.logs.append("[agent-sync] ignorado: apiScheme invalido (use http ou https)")
		return
	}

	// Serializar payloads
	hardwarePayload := buildAgentHardwareEnvelope(report)
	hardwareBody, err := json.Marshal(hardwarePayload)
	if err != nil {
		a.logs.append("[agent-sync] falha ao serializar inventario: " + err.Error())
		return
	}

	softwarePayload := buildAgentSoftwareEnvelope(report)
	softwareBody, err := json.Marshal(softwarePayload)
	if err != nil {
		a.logs.append("[agent-sync] falha ao serializar softwares: " + err.Error())
		return
	}

	// Verificar se deve sincronizar com a API
	if a.db != nil {
		shouldSync, reason, err := a.db.ShouldSyncInventory(cfg.AgentID, hardwareBody, softwareBody)
		if err != nil {
			a.logs.append("[agent-sync] erro ao verificar diff: " + err.Error())
			// Continua e tenta sincronizar em caso de erro
		} else if !shouldSync {
			a.logs.append(fmt.Sprintf("[agent-sync] SYNC IGNORADO: %s", reason))
			// Salvar snapshot local mesmo sem enviar para API
			if err := a.db.SaveInventorySnapshot(cfg.AgentID, hardwareBody, softwareBody); err != nil {
				a.logs.append("[agent-sync] aviso: falha ao salvar snapshot local: " + err.Error())
			}
			return
		} else {
			a.logs.append(fmt.Sprintf("[agent-sync] SYNC NECESSARIO: %s", reason))
		}
	}

	a.logs.append(fmt.Sprintf(
		"[agent-sync] hardware payload: collectedAt=%s disks=%d networkAdapters=%d memoryModules=%d hostname=%s",
		hardwarePayload.InventoryCollectedAt,
		len(hardwarePayload.Components.Disks),
		len(hardwarePayload.Components.NetworkAdapters),
		len(hardwarePayload.Components.MemoryModules),
		hardwarePayload.Hostname,
	))

	// Enviar hardware
	hardwareEndpoint := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/agent-auth/me/hardware"
	hardwareSuccess := false
	a.pulseInventoryHeartbeat()
	if err := a.sendAgentInventoryRequest(ctx, hardwareEndpoint, cfg, http.MethodPost, hardwareBody); err != nil {
		a.logs.append("[agent-sync] POST hardware falhou: " + err.Error())
		a.pulseInventoryHeartbeat()
		if err := a.sendAgentInventoryRequest(ctx, hardwareEndpoint, cfg, http.MethodPut, hardwareBody); err != nil {
			a.logs.append("[agent-sync] PUT hardware falhou: " + err.Error())
		} else {
			a.logs.append("[agent-sync] inventario de hardware atualizado via PUT")
			hardwareSuccess = true
		}
	} else {
		a.logs.append("[agent-sync] inventario de hardware enviado via POST")
		hardwareSuccess = true
	}

	a.logs.append(fmt.Sprintf(
		"[agent-sync] software payload: collectedAt=%s softwareCount=%d",
		softwarePayload.CollectedAt,
		len(softwarePayload.Software),
	))

	// Enviar software
	softwareEndpoint := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/agent-auth/me/software"
	a.logs.append("[agent-sync] endpoint software: " + softwareEndpoint)
	softwareSuccess := false
	a.pulseInventoryHeartbeat()
	if err := a.sendAgentInventoryRequest(ctx, softwareEndpoint, cfg, http.MethodPost, softwareBody); err != nil {
		a.logs.append("[agent-sync] POST software falhou: " + err.Error())
		a.pulseInventoryHeartbeat()
		if err := a.sendAgentInventoryRequest(ctx, softwareEndpoint, cfg, http.MethodPut, softwareBody); err != nil {
			a.logs.append("[agent-sync] PUT software falhou: " + err.Error())
		} else {
			a.logs.append("[agent-sync] inventario de software atualizado via PUT")
			softwareSuccess = true
		}
	} else {
		a.logs.append("[agent-sync] inventario de software enviado via POST")
		softwareSuccess = true
	}

	// Se sync foi bem-sucedido, atualizar controle e salvar snapshot
	if hardwareSuccess && softwareSuccess && a.db != nil {
		if err := a.db.SaveInventorySnapshot(cfg.AgentID, hardwareBody, softwareBody); err != nil {
			a.logs.append("[agent-sync] aviso: falha ao salvar snapshot: " + err.Error())
		}
		if err := a.db.UpdateLastSyncTime("inventory_sync:"+cfg.AgentID, "success"); err != nil {
			a.logs.append("[agent-sync] aviso: falha ao atualizar timestamp de sync: " + err.Error())
		} else {
			a.logs.append("[agent-sync] snapshot salvo e timestamp atualizado")
		}
	}
}

func (a *App) sendAgentInventoryRequest(parent context.Context, endpoint string, cfg DebugConfig, method string, body []byte) error {
	a.pulseInventoryHeartbeat()
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()

	a.logs.append("[agent-sync] request: " + method + " " + endpoint)
	a.logs.append("[agent-sync] request headers: Authorization=Bearer " + cfg.AuthToken + "; X-Agent-ID=" + cfg.AgentID + "; Content-Type=application/json")
	a.logs.append("[agent-sync] request body: " + string(body))

	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	req.Header.Set("X-Agent-ID", cfg.AgentID)

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		a.pulseInventoryHeartbeat()
		return err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	a.pulseInventoryHeartbeat()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	a.pulseInventoryHeartbeat()
	return nil
}

func buildAgentSoftwareEnvelope(report models.InventoryReport) agentSoftwareEnvelope {
	collected := strings.TrimSpace(report.CollectedAt)
	if collected == "" {
		collected = time.Now().UTC().Format(time.RFC3339)
	}

	software := make([]agentSoftwareItem, 0, len(report.Software))
	for _, s := range report.Software {
		name := trimToMaxLen(strings.TrimSpace(s.Name), 300)
		if name == "" {
			continue
		}
		source := trimToMaxLen(strings.TrimSpace(s.Source), 120)
		if source == "" {
			source = "osquery/programs"
		}
		software = append(software, agentSoftwareItem{
			Name:      name,
			Version:   trimToMaxLen(strings.TrimSpace(s.Version), 120),
			Publisher: trimToMaxLen(strings.TrimSpace(s.Publisher), 300),
			InstallID: trimToMaxLen(strings.TrimSpace(s.InstallID), 1000),
			Serial:    trimToMaxLen(strings.TrimSpace(s.Serial), 1000),
			Source:    source,
		})
	}

	return agentSoftwareEnvelope{
		CollectedAt: collected,
		Software:    software,
	}
}

func buildAgentHardwareEnvelope(report models.InventoryReport) agentHardwareEnvelope {
	collected := strings.TrimSpace(report.CollectedAt)
	if collected == "" {
		collected = time.Now().UTC().Format(time.RFC3339)
	}
	updated := time.Now().UTC().Format(time.RFC3339)

	memTotalBytes := int64(report.Hardware.MemoryGB * 1024 * 1024 * 1024)
	if memTotalBytes < 0 {
		memTotalBytes = 0
	}

	disks := make([]agentDiskInfo, 0, len(report.Disks))
	for _, d := range report.Disks {
		driveLetter := trimToMaxLen(normalizeDriveLetter(d.Device), 10)
		if driveLetter == "" {
			continue
		}
		total := int64(d.SizeGB * 1024 * 1024 * 1024)
		if total < 0 {
			total = 0
		}
		free := int64(d.FreeGB * 1024 * 1024 * 1024)
		if free < 0 || !d.FreeKnown {
			free = 0
		}
		disks = append(disks, agentDiskInfo{
			DriveLetter:    driveLetter,
			Label:          trimToMaxLen(strings.TrimSpace(d.Label), 200),
			FileSystem:     trimToMaxLen(strings.TrimSpace(d.FileSystem), 50),
			TotalSizeBytes: total,
			FreeSpaceBytes: free,
			MediaType:      trimToMaxLen(strings.TrimSpace(d.Type), 50),
			CollectedAt:    collected,
		})
	}

	adapters := make([]agentNetworkAdapterInfo, 0, len(report.Networks))
	for _, n := range report.Networks {
		name := trimToMaxLen(firstNonEmptyString(strings.TrimSpace(n.FriendlyName), strings.TrimSpace(n.Interface)), 200)
		if name == "" {
			continue
		}
		adapters = append(adapters, agentNetworkAdapterInfo{
			Name:          name,
			MACAddress:    trimToMaxLen(strings.TrimSpace(n.MAC), 32),
			IPAddress:     trimToMaxLen(firstNonEmptyString(strings.TrimSpace(n.IPv4), strings.TrimSpace(n.IPv6)), 45),
			SubnetMask:    "",
			Gateway:       trimToMaxLen(strings.TrimSpace(n.Gateway), 45),
			DNSServers:    trimToMaxLen(normalizeDNSServers(n.DNSServers), 500),
			IsDhcpEnabled: n.DHCPEnabled,
			AdapterType:   trimToMaxLen(strings.TrimSpace(n.Type), 50),
			Speed:         trimToMaxLen(formatLinkSpeed(n.LinkSpeedMbps), 50),
			CollectedAt:   collected,
		})
	}

	modules := make([]agentMemoryModuleInfo, 0, len(report.MemoryModules))
	for _, m := range report.MemoryModules {
		capacity := int64(m.SizeGB * 1024 * 1024 * 1024)
		if capacity <= 0 {
			capacity = int64(m.SizeMB) * 1024 * 1024
		}
		if capacity < 0 {
			capacity = 0
		}
		modules = append(modules, agentMemoryModuleInfo{
			Slot:          trimToMaxLen(strings.TrimSpace(m.Slot), 50),
			CapacityBytes: capacity,
			SpeedMhz:      m.SpeedMHz,
			MemoryType:    trimToMaxLen(strings.TrimSpace(m.Type), 50),
			Manufacturer:  trimToMaxLen(strings.TrimSpace(m.Manufacturer), 200),
			PartNumber:    trimToMaxLen(strings.TrimSpace(m.PartNumber), 100),
			SerialNumber:  trimToMaxLen(strings.TrimSpace(m.Serial), 100),
			CollectedAt:   collected,
		})
	}
	rawJSON := buildCleanInventoryRaw(report, disks, adapters, modules)
	lastIP := ""
	primaryMAC := ""
	for _, n := range adapters {
		if lastIP == "" {
			lastIP = strings.TrimSpace(n.IPAddress)
		}
		if primaryMAC == "" {
			primaryMAC = strings.TrimSpace(n.MACAddress)
		}
		if lastIP != "" && primaryMAC != "" {
			break
		}
	}

	hostname := trimToMaxLen(strings.TrimSpace(report.Hardware.Hostname), 100)
	if len(hostname) < 2 {
		hostname = "unknown-host"
	}
	osName := trimToMaxLen(strings.TrimSpace(report.OS.Name), 100)
	osVersion := trimToMaxLen(strings.TrimSpace(report.OS.Version), 100)

	envelope := agentHardwareEnvelope{
		Hostname:        hostname,
		DisplayName:     trimToMaxLen(hostname, 100),
		Status:          "Online",
		OperatingSystem: osName,
		OSVersion:       osVersion,
		AgentVersion:    trimToMaxLen(strings.TrimSpace(Version), 100),
		LastIPAddress:   trimToMaxLen(lastIP, 45),
		MACAddress:      trimToMaxLen(primaryMAC, 17),
		Hardware: agentHardwareInfo{
			InventoryRaw:            rawJSON,
			InventorySchemaVersion:  "discovery.inventory.v1",
			InventoryCollectedAt:    collected,
			Manufacturer:            trimToMaxLen(strings.TrimSpace(report.Hardware.Manufacturer), 200),
			Model:                   trimToMaxLen(strings.TrimSpace(report.Hardware.Model), 200),
			SerialNumber:            trimToMaxLen(strings.TrimSpace(report.Hardware.MotherboardSerial), 100),
			MotherboardManufacturer: trimToMaxLen(strings.TrimSpace(report.Hardware.MotherboardManufacturer), 200),
			MotherboardModel:        trimToMaxLen(strings.TrimSpace(report.Hardware.MotherboardModel), 200),
			MotherboardSerialNumber: trimToMaxLen(strings.TrimSpace(report.Hardware.MotherboardSerial), 100),
			Processor:               trimToMaxLen(strings.TrimSpace(report.Hardware.CPU), 200),
			ProcessorCores:          report.Hardware.Cores,
			ProcessorThreads:        report.Hardware.LogicalCores,
			ProcessorArchitecture:   trimToMaxLen(strings.TrimSpace(report.OS.Architecture), 50),
			TotalMemoryBytes:        memTotalBytes,
			BIOSVersion:             trimToMaxLen(strings.TrimSpace(report.Hardware.BIOSVersion), 100),
			BIOSManufacturer:        trimToMaxLen(strings.TrimSpace(report.Hardware.BIOSVendor), 200),
			OSName:                  osName,
			OSVersion:               osVersion,
			OSBuild:                 trimToMaxLen(strings.TrimSpace(report.OS.Build), 100),
			OSArchitecture:          trimToMaxLen(strings.TrimSpace(report.OS.Architecture), 50),
			CollectedAt:             collected,
			UpdatedAt:               updated,
		},
		Components: agentHardwareComponents{
			Disks:           disks,
			NetworkAdapters: adapters,
			MemoryModules:   modules,
		},
		InventoryRaw:           rawJSON,
		InventorySchemaVersion: "discovery.inventory.v1",
		InventoryCollectedAt:   collected,
	}
	return envelope
}

func buildCleanInventoryRaw(
	report models.InventoryReport,
	disks []agentDiskInfo,
	networkAdapters []agentNetworkAdapterInfo,
	memoryModules []agentMemoryModuleInfo,
) json.RawMessage {
	clean := map[string]any{
		"collectedAt": report.CollectedAt,
		"source":      report.Source,
		"hardware": map[string]any{
			"hostname":                report.Hardware.Hostname,
			"manufacturer":            report.Hardware.Manufacturer,
			"model":                   report.Hardware.Model,
			"cpu":                     report.Hardware.CPU,
			"cores":                   report.Hardware.Cores,
			"logicalCores":            report.Hardware.LogicalCores,
			"memoryGB":                report.Hardware.MemoryGB,
			"motherboardManufacturer": report.Hardware.MotherboardManufacturer,
			"motherboardModel":        report.Hardware.MotherboardModel,
			"motherboardSerial":       report.Hardware.MotherboardSerial,
			"biosVendor":              report.Hardware.BIOSVendor,
			"biosVersion":             report.Hardware.BIOSVersion,
		},
		"os": report.OS,
		"disks": mapSlice(disks, func(d agentDiskInfo) map[string]any {
			return map[string]any{
				"driveLetter":    d.DriveLetter,
				"label":          d.Label,
				"fileSystem":     d.FileSystem,
				"totalSizeBytes": d.TotalSizeBytes,
				"freeSpaceBytes": d.FreeSpaceBytes,
				"mediaType":      d.MediaType,
			}
		}),
		"networkAdapters": mapSlice(networkAdapters, func(n agentNetworkAdapterInfo) map[string]any {
			return map[string]any{
				"name":          n.Name,
				"macAddress":    n.MACAddress,
				"ipAddress":     n.IPAddress,
				"gateway":       n.Gateway,
				"dnsServers":    n.DNSServers,
				"isDhcpEnabled": n.IsDhcpEnabled,
				"adapterType":   n.AdapterType,
				"speed":         n.Speed,
			}
		}),
		"memoryModules": mapSlice(memoryModules, func(m agentMemoryModuleInfo) map[string]any {
			return map[string]any{
				"slot":          m.Slot,
				"capacityBytes": m.CapacityBytes,
				"speedMhz":      m.SpeedMhz,
				"memoryType":    m.MemoryType,
				"manufacturer":  m.Manufacturer,
				"partNumber":    m.PartNumber,
				"serialNumber":  m.SerialNumber,
			}
		}),
	}
	b, err := json.Marshal(clean)
	if err != nil {
		return json.RawMessage("{}")
	}
	return json.RawMessage(b)
}

func mapSlice[T any, R any](in []T, fn func(T) R) []R {
	out := make([]R, 0, len(in))
	for _, item := range in {
		out = append(out, fn(item))
	}
	return out
}

func normalizeDNSServers(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '|' || r == ' '
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, ",")
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func formatLinkSpeed(linkSpeedMbps int) string {
	if linkSpeedMbps <= 0 {
		return ""
	}
	return fmt.Sprintf("%d", linkSpeedMbps)
}

func normalizeDriveLetter(device string) string {
	device = strings.TrimSpace(device)
	if len(device) >= 2 && device[1] == ':' {
		return strings.ToUpper(device[:1]) + ":"
	}
	return device
}

func trimToMaxLen(value string, max int) string {
	value = strings.TrimSpace(value)
	if max <= 0 || len(value) <= max {
		return value
	}
	return strings.TrimSpace(value[:max])
}

func isValidDebugScheme(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	return s == "http" || s == "https" || s == "nats"
}

func chatConfigPathCandidates() []string {
	paths := make([]string, 0, 4)

	if runtime.GOOS == "windows" {
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			paths = append(paths, filepath.Join(localAppData, "Discovery", chatConfigFile))
		}
	}

	if exe, err := os.Executable(); err == nil && strings.TrimSpace(exe) != "" {
		paths = append(paths, filepath.Join(filepath.Dir(exe), chatConfigFile))
	}

	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		paths = append(paths, filepath.Join(home, ".discovery", chatConfigFile))
	}

	paths = append(paths, filepath.Join(".", chatConfigFile))
	return lo.Uniq(paths)
}

func (a *App) loadPersistedChatConfig() {
	for _, path := range chatConfigPathCandidates() {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var cfg ChatConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			a.logs.append("[chat] falha ao ler configuracao persistida: " + err.Error())
			return
		}

		if strings.TrimSpace(cfg.Endpoint) == "" || strings.TrimSpace(cfg.APIKey) == "" || strings.TrimSpace(cfg.Model) == "" {
			a.logs.append("[chat] configuracao persistida ignorada: campos obrigatorios ausentes")
			return
		}

		a.chatSvc.SetConfig(ai.Config{
			Endpoint:     cfg.Endpoint,
			APIKey:       cfg.APIKey,
			Model:        cfg.Model,
			SystemPrompt: cfg.SystemPrompt,
		})
		a.logs.append("[chat] configuracao carregada de " + path)
		return
	}
}

func (a *App) persistChatConfig(cfg ChatConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("falha ao serializar configuracao do chat: %w", err)
	}

	var errs []string
	for _, path := range chatConfigPathCandidates() {
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			errs = append(errs, dir+": "+err.Error())
			continue
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			errs = append(errs, path+": "+err.Error())
			continue
		}
		a.logs.append("[chat] configuracao salva em " + path)
		return nil
	}

	if len(errs) == 0 {
		return fmt.Errorf("nenhum caminho valido para salvar configuracao do chat")
	}
	return fmt.Errorf("falha ao salvar configuracao do chat: %s", strings.Join(errs, " | "))
}

// -----------------------------------------------------------------------
// AppBridge implementation (used by MCP tool registry)
// -----------------------------------------------------------------------

func (a *App) GetInventoryJSON() (json.RawMessage, error) {
	report, err := a.GetInventory()
	if err != nil {
		return nil, err
	}
	return json.Marshal(report)
}

func (a *App) SearchCatalog(query string) (json.RawMessage, error) {
	catalog, err := a.catalogSvc.GetCatalog(a.ctx)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(query)
	var matches []models.AppItem
	for _, item := range catalog.Packages {
		if strings.Contains(strings.ToLower(item.Name), q) ||
			strings.Contains(strings.ToLower(item.ID), q) ||
			strings.Contains(strings.ToLower(item.Publisher), q) {
			matches = append(matches, item)
			if len(matches) >= 20 {
				break
			}
		}
	}
	return json.Marshal(matches)
}

func (a *App) InstallPackage(id string) (string, error)   { return a.Install(id) }
func (a *App) UninstallPackage(id string) (string, error) { return a.Uninstall(id) }
func (a *App) UpgradePackage(id string) (string, error)   { return a.Upgrade(id) }
func (a *App) UpgradeAllPackages() (string, error)        { return a.UpgradeAll() }

func (a *App) GetPendingUpdatesJSON() (json.RawMessage, error) {
	items, err := a.GetPendingUpdates()
	if err != nil {
		return nil, err
	}
	return json.Marshal(items)
}

func (a *App) ExportMarkdown() (string, error) { return a.ExportInventoryMarkdown() }
func (a *App) ExportPDF() (string, error)      { return a.ExportInventoryPDF() }

func (a *App) GetOsqueryStatusJSON() (json.RawMessage, error) {
	status, _ := a.GetOsqueryStatus()
	return json.Marshal(status)
}

func (a *App) GetLogsText() string {
	return strings.Join(a.logs.getAll(), "\n")
}

// -----------------------------------------------------------------------
// Chat AI methods (exposed to frontend via Wails)
// -----------------------------------------------------------------------

// ChatConfig is the frontend-facing AI configuration.
type ChatConfig struct {
	Endpoint     string `json:"endpoint"`
	APIKey       string `json:"apiKey"`
	Model        string `json:"model"`
	SystemPrompt string `json:"systemPrompt"`
}

// ChatMessage is a single message for the frontend.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// SetChatConfig updates and persists the LLM API settings.
func (a *App) SetChatConfig(cfg ChatConfig) error {
	if strings.TrimSpace(cfg.Endpoint) == "" || strings.TrimSpace(cfg.APIKey) == "" || strings.TrimSpace(cfg.Model) == "" {
		return fmt.Errorf("configuracao de IA incompleta: defina endpoint, apiKey e model")
	}

	a.chatSvc.SetConfig(ai.Config{
		Endpoint:     cfg.Endpoint,
		APIKey:       cfg.APIKey,
		Model:        cfg.Model,
		SystemPrompt: cfg.SystemPrompt,
	})

	if err := a.persistChatConfig(cfg); err != nil {
		return err
	}
	return nil
}

// TestChatConfig checks whether the informed LLM settings are valid without saving them.
func (a *App) TestChatConfig(cfg ChatConfig) (string, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	return a.chatSvc.TestConfig(ctx, ai.Config{
		Endpoint:     cfg.Endpoint,
		APIKey:       cfg.APIKey,
		Model:        cfg.Model,
		SystemPrompt: cfg.SystemPrompt,
	})
}

// GetChatConfig returns the current config (API key masked).
func (a *App) GetChatConfig() ChatConfig {
	c := a.chatSvc.GetConfig()
	return ChatConfig{
		Endpoint:     c.Endpoint,
		APIKey:       c.APIKey,
		Model:        c.Model,
		SystemPrompt: c.SystemPrompt,
	}
}

// SendChatMessage sends a user message and returns the assistant response.
func (a *App) SendChatMessage(message string) (string, error) {
	done := a.beginActivity("chat IA")
	defer done()
	return a.chatSvc.Send(a.ctx, message)
}

// StartChatStream sends a chat message and streams the response via Wails events.
// Returns immediately; the response arrives token by token via events:
//
//	chat:token   — partial text token (string)
//	chat:thinking — progress status during tool calls (string)
//	chat:done    — stream finished (no data)
//	chat:error   — error message (string)
func (a *App) StartChatStream(message string) {
	done := a.beginActivity("chat IA")

	// Create stream monitor to detect stalled streams
	streamMonitor := watchdog.NewStreamMonitor(
		"ai-chat-stream",
		90*time.Second, // Alert if no activity for 90s
		func() {
			// Stream stalled - force stop
			log.Println("[watchdog] AI stream travado - forcando interrupcao")
			a.chatSvc.StopStream()
			if a.ctx != nil {
				wailsRuntime.EventsEmit(a.ctx, "chat:error", "Stream interrompido automaticamente por inatividade")
			}
		},
	)

	go func() {
		defer done()

		// Start monitoring
		streamMonitor.Start(a.ctx)
		defer streamMonitor.Stop()

		// Send initial heartbeat for AI component
		if a.watchdogSvc != nil {
			a.watchdogSvc.Heartbeat(watchdog.ComponentAI)
		}

		_, err := a.chatSvc.SendStream(
			a.ctx,
			message,
			func(token string) {
				streamMonitor.Activity() // Record activity on each token
				if a.watchdogSvc != nil {
					a.watchdogSvc.Heartbeat(watchdog.ComponentAI)
				}
				wailsRuntime.EventsEmit(a.ctx, "chat:token", token)
			},
			func(status string) {
				streamMonitor.Activity() // Record activity on status updates
				if a.watchdogSvc != nil {
					a.watchdogSvc.Heartbeat(watchdog.ComponentAI)
				}
				wailsRuntime.EventsEmit(a.ctx, "chat:thinking", status)
			},
		)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				wailsRuntime.EventsEmit(a.ctx, "chat:stopped")
			} else {
				wailsRuntime.EventsEmit(a.ctx, "chat:error", err.Error())
			}
		} else {
			wailsRuntime.EventsEmit(a.ctx, "chat:done")
		}
	}()
}

// StopChatStream interrupts the active streamed AI response, if running.
func (a *App) StopChatStream() bool {
	return a.chatSvc.StopStream()
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

// ClearChatHistory resets the conversation.
func (a *App) ClearChatHistory() {
	a.chatSvc.ClearHistory()
}

// GetChatHistory returns the conversation for display.
func (a *App) GetChatHistory() []ChatMessage {
	history := a.chatSvc.GetHistory()
	msgs := make([]ChatMessage, 0, len(history))
	for _, m := range history {
		if m.Role == "tool" || (m.Role == "assistant" && m.Content == "" && len(m.ToolCalls) > 0) {
			continue // hide internal tool calls from display
		}
		msgs = append(msgs, ChatMessage{Role: m.Role, Content: m.Content})
	}
	return msgs
}

// GetAvailableTools returns the list of MCP tools for display.
func (a *App) GetAvailableTools() []map[string]string {
	tools := a.mcpRegistry.Tools()
	result := make([]map[string]string, len(tools))
	for i, t := range tools {
		result[i] = map[string]string{
			"name":        t.Name,
			"description": t.Description,
		}
	}
	return result
}

// GetMCPRegistry returns the registry (used by main.go for MCP server mode).
func (a *App) GetMCPRegistry() *mcp.Registry {
	return a.mcpRegistry
}

// -----------------------------------------------------------------------
// Support tickets — real API integration
// -----------------------------------------------------------------------

func (a *App) supportLogf(format string, args ...any) {
	a.logs.append("[support] " + fmt.Sprintf(format, args...))
}

func shortBodyForLog(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 400 {
		return s[:400] + "..."
	}
	return s
}

func normalizePriority(v int) int {
	if v < 1 || v > 4 {
		return 2
	}
	return v
}

func priorityIntToLabel(v int) string {
	switch normalizePriority(v) {
	case 1:
		return "Low"
	case 3:
		return "High"
	case 4:
		return "Critical"
	default:
		return "Medium"
	}
}

func priorityLabelToInt(label string) int {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "1", "low", "baixa":
		return 1
	case "3", "high", "alta":
		return 3
	case "4", "critical", "critica", "crítica":
		return 4
	case "2", "medium", "media", "média":
		fallthrough
	default:
		return 2
	}
}

func toInt(values ...any) int {
	for _, v := range values {
		switch n := v.(type) {
		case float64:
			return int(n)
		case float32:
			return int(n)
		case int:
			return n
		case int64:
			return int(n)
		case json.Number:
			if i, err := n.Int64(); err == nil {
				return int(i)
			}
		case string:
			s := strings.TrimSpace(n)
			if s == "" {
				continue
			}
			var parsed int
			if _, err := fmt.Sscanf(s, "%d", &parsed); err == nil {
				return parsed
			}
		}
	}
	return 0
}

func toBool(values ...any) bool {
	for _, v := range values {
		switch b := v.(type) {
		case bool:
			return b
		case string:
			s := strings.ToLower(strings.TrimSpace(b))
			if s == "true" || s == "1" || s == "yes" || s == "sim" {
				return true
			}
			if s == "false" || s == "0" || s == "no" || s == "nao" || s == "não" {
				return false
			}
		case float64:
			return b != 0
		case int:
			return b != 0
		}
	}
	return false
}

func setAgentAuthHeaders(req *http.Request, token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}
	req.Header.Set("X-Agent-Token", token)
	req.Header.Set("Authorization", "Bearer "+token)
}

func extractAgentInfoFromJSON(body []byte, cfg DebugConfig) (AgentInfo, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return AgentInfo{}, fmt.Errorf("resposta inválida de /api/agent-auth/me: %w", err)
	}

	asMap := func(v any) map[string]any {
		m, _ := v.(map[string]any)
		return m
	}
	getStr := func(m map[string]any, keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k]; ok {
				s := strings.TrimSpace(fmt.Sprint(v))
				if s != "" && s != "<nil>" {
					return s
				}
			}
		}
		return ""
	}

	candidates := []map[string]any{raw}
	for _, key := range []string{"data", "agent", "result", "payload"} {
		if m := asMap(raw[key]); m != nil {
			candidates = append(candidates, m)
		}
	}

	info := AgentInfo{}
	for _, c := range candidates {
		if info.AgentID == "" {
			info.AgentID = getStr(c, "agentId", "agentID", "id")
		}
		if info.ClientID == "" {
			info.ClientID = getStr(c, "clientId", "clientID")
		}
		if info.ClientID == "" {
			if client := asMap(c["client"]); client != nil {
				info.ClientID = getStr(client, "id", "clientId", "clientID")
			}
		}
		if info.SiteID == "" {
			info.SiteID = getStr(c, "siteId", "siteID")
		}
		if info.SiteID == "" {
			if site := asMap(c["site"]); site != nil {
				info.SiteID = getStr(site, "id", "siteId", "siteID")
			}
		}
		if info.Hostname == "" {
			info.Hostname = getStr(c, "hostname", "hostName")
		}
		if info.Name == "" {
			info.Name = getStr(c, "displayName", "name")
		}
	}

	if s := strings.TrimSpace(cfg.AgentID); s != "" {
		info.AgentID = s
	}

	info.AgentID = strings.TrimSpace(info.AgentID)
	info.ClientID = strings.TrimSpace(info.ClientID)
	info.SiteID = strings.TrimSpace(info.SiteID)
	info.Hostname = strings.TrimSpace(info.Hostname)
	info.Name = strings.TrimSpace(info.Name)

	return info, nil
}

// fetchAgentContext resolves clientId/siteId from /api/agent-auth/me (cached).
func (a *App) fetchAgentContext() (AgentInfo, error) {
	// 1. Tentar carregar do cache em memória primeiro (mais rápido)
	if info, ok := a.agentInfo.get(); ok {
		if strings.TrimSpace(info.ClientID) != "" {
			return info, nil
		}
		a.supportLogf("cache em memória sem clientId; ignorando e recarregando do servidor")
		a.agentInfo.invalidate()
	}

	// 2. Tentar carregar do SQLite (cache persistente)
	if a.db != nil {
		var cached AgentInfo
		found, err := a.db.CacheGetJSON("agent_info", &cached)
		if err == nil && found {
			if strings.TrimSpace(cached.ClientID) != "" {
				a.agentInfo.set(cached) // Cachear em memória também
				return cached, nil
			}
			a.supportLogf("cache SQLite sem clientId; removendo entrada e atualizando do servidor")
			if delErr := a.db.CacheDelete("agent_info"); delErr != nil {
				log.Printf("[support] aviso: falha ao limpar cache SQLite agent_info inválido: %v", delErr)
			}
		}
	}

	// 3. Buscar do servidor
	cfg := a.GetDebugConfig()
	cfg.ApiScheme = strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
	cfg.ApiServer = strings.TrimSpace(cfg.ApiServer)
	if cfg.ApiServer == "" || strings.TrimSpace(cfg.AuthToken) == "" {
		err := fmt.Errorf("configuração de servidor API incompleta: preencha apiServer e token no Debug")
		a.supportLogf("falha ao resolver contexto do agente: %v", err)
		return AgentInfo{}, err
	}
	if cfg.ApiScheme != "http" && cfg.ApiScheme != "https" {
		err := fmt.Errorf("apiScheme inválido: use http ou https")
		a.supportLogf("falha ao resolver contexto do agente: %v", err)
		return AgentInfo{}, err
	}

	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	target := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/agent-auth/me"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		wrapped := fmt.Errorf("URL inválida: %w", err)
		a.supportLogf("falha ao montar request de contexto do agente: %v", wrapped)
		return AgentInfo{}, wrapped
	}
	setAgentAuthHeaders(req, cfg.AuthToken)

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		wrapped := fmt.Errorf("falha ao conectar em %s: %w", target, err)
		a.supportLogf("erro HTTP ao resolver contexto do agente: %v", wrapped)
		return AgentInfo{}, wrapped
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		wrapped := fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
		a.supportLogf("/api/agent-auth/me retornou erro: %v", wrapped)
		return AgentInfo{}, wrapped
	}

	info, err := extractAgentInfoFromJSON(body, cfg)
	if err != nil {
		a.supportLogf("falha ao decodificar /api/agent-auth/me: %v", err)
		return AgentInfo{}, err
	}
	if info.ClientID == "" {
		err := fmt.Errorf("clientId não retornado por /api/agent-auth/me: verifique token/escopo do agente")
		a.supportLogf("%v | resposta=%s", err, shortBodyForLog(body))
		return AgentInfo{}, err
	}

	// 4. Salvar em ambos os caches (memória + SQLite)
	a.agentInfo.set(info)
	if a.db != nil {
		// Cache por 24 horas
		if err := a.db.CacheSetJSON("agent_info", info, 24*time.Hour); err != nil {
			log.Printf("[support] aviso: falha ao salvar no cache SQLite (agent_info): %v", err)
		}
	}
	a.supportLogf("contexto do agente resolvido: agentId=%s clientId=%s siteId=%s", info.AgentID, info.ClientID, info.SiteID)

	return info, nil
}

// GetAgentInfo resolves and returns the current agent identifiers from the server.
func (a *App) GetAgentInfo() (AgentInfo, error) {
	return a.fetchAgentContext()
}

// GetSupportTickets returns tickets linked to this agent (filtered by agentId).
func (a *App) GetSupportTickets() ([]APITicket, error) {
	a.supportLogf("listando chamados vinculados ao agente")
	info, err := a.fetchAgentContext()
	if err != nil {
		a.supportLogf("falha ao obter contexto para listagem de chamados: %v", err)
		return nil, err
	}
	if strings.TrimSpace(info.ClientID) == "" {
		err := fmt.Errorf("clientId não resolvido: verifique a configuração do agente")
		a.supportLogf("%v (agentId=%s)", err, info.AgentID)
		return nil, err
	}

	cfg := a.GetDebugConfig()
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	target := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/agent-auth/me/tickets"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		wrapped := fmt.Errorf("URL inválida: %w", err)
		a.supportLogf("falha ao montar request de listagem: %v", wrapped)
		return nil, wrapped
	}
	setAgentAuthHeaders(req, cfg.AuthToken)

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		wrapped := fmt.Errorf("falha ao buscar chamados: %w", err)
		a.supportLogf("erro HTTP ao listar chamados: %v", wrapped)
		return nil, wrapped
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		wrapped := fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
		a.supportLogf("erro na listagem de chamados: %v", wrapped)
		return nil, wrapped
	}

	var tickets []APITicket
	if err := json.Unmarshal(body, &tickets); err != nil {
		// Try paginated response envelope
		var envelope struct {
			Items []APITicket `json:"items"`
			Data  []APITicket `json:"data"`
		}
		if err2 := json.Unmarshal(body, &envelope); err2 == nil {
			if envelope.Items != nil {
				tickets = envelope.Items
			} else {
				tickets = envelope.Data
			}
		} else {
			return nil, fmt.Errorf("resposta inválida ao listar chamados: %w", err)
		}
	}
	if tickets == nil {
		tickets = []APITicket{}
	}

	// Endpoint /me/tickets já é escopado ao agente autenticado.
	a.supportLogf("listagem concluída: %d chamado(s) retornado(s)", len(tickets))
	return tickets, nil
}

// CreateSupportTicket opens a new ticket linked to this agent.
func (a *App) CreateSupportTicket(input CreateTicketInput) (APITicket, error) {
	a.supportLogf("criando chamado: title=%q priority=%d category=%q", strings.TrimSpace(input.Title), input.Priority, strings.TrimSpace(input.Category))
	info, err := a.fetchAgentContext()
	if err != nil {
		a.supportLogf("falha ao obter contexto para criação de chamado: %v", err)
		return APITicket{}, err
	}
	if strings.TrimSpace(info.ClientID) == "" {
		err := fmt.Errorf("clientId não resolvido: verifique a configuração do agente")
		a.supportLogf("%v (agentId=%s)", err, info.AgentID)
		return APITicket{}, err
	}

	cfg := a.GetDebugConfig()
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	type createReq struct {
		DepartmentID      *string `json:"departmentId,omitempty"`
		WorkflowProfileID *string `json:"workflowProfileId,omitempty"`
		Title             string  `json:"title"`
		Description       string  `json:"description"`
		Priority          *string `json:"priority,omitempty"`
		Category          *string `json:"category,omitempty"`
	}

	payload := createReq{
		Title:       strings.TrimSpace(input.Title),
		Description: strings.TrimSpace(input.Description),
	}
	if c := strings.TrimSpace(input.Category); c != "" {
		payload.Category = &c
	}
	if input.Priority > 0 {
		pri := priorityIntToLabel(input.Priority)
		payload.Priority = &pri
	}

	reqBody, err := json.Marshal(payload)
	if err != nil {
		wrapped := fmt.Errorf("erro ao serializar chamado: %w", err)
		a.supportLogf("falha ao serializar payload de chamado: %v", wrapped)
		return APITicket{}, wrapped
	}

	target := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/agent-auth/me/tickets"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(reqBody))
	if err != nil {
		wrapped := fmt.Errorf("URL inválida: %w", err)
		a.supportLogf("falha ao montar request de criação: %v", wrapped)
		return APITicket{}, wrapped
	}
	req.Header.Set("Content-Type", "application/json")
	setAgentAuthHeaders(req, cfg.AuthToken)

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		wrapped := fmt.Errorf("falha ao criar chamado: %w", err)
		a.supportLogf("erro HTTP ao criar chamado: %v", wrapped)
		return APITicket{}, wrapped
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		wrapped := fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
		a.supportLogf("erro na criação do chamado: %v | payload=%s | resposta=%s", wrapped, shortBodyForLog(reqBody), shortBodyForLog(respBody))
		return APITicket{}, wrapped
	}

	var ticket APITicket
	if err := json.Unmarshal(respBody, &ticket); err != nil {
		wrapped := fmt.Errorf("resposta inválida ao criar chamado: %w", err)
		a.supportLogf("falha ao decodificar resposta da criação: %v | resposta=%s", wrapped, shortBodyForLog(respBody))
		return APITicket{}, wrapped
	}
	a.supportLogf("chamado criado com sucesso: ticketId=%s", ticket.ID)
	return ticket, nil
}

// GetSupportTicketDetails returns a single ticket if it belongs to the authenticated agent.
func (a *App) GetSupportTicketDetails(ticketID string) (APITicket, error) {
	ticketID = strings.TrimSpace(ticketID)
	if !guidPattern.MatchString(ticketID) {
		return APITicket{}, fmt.Errorf("ticketId inválido")
	}

	cfg := a.GetDebugConfig()
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	target := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/agent-auth/me/tickets/" + ticketID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return APITicket{}, err
	}
	setAgentAuthHeaders(req, cfg.AuthToken)

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return APITicket{}, fmt.Errorf("falha ao buscar ticket: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return APITicket{}, fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var ticket APITicket
	if err := json.Unmarshal(body, &ticket); err != nil {
		var envelope struct {
			Ticket *APITicket `json:"ticket"`
			Data   *APITicket `json:"data"`
			Item   *APITicket `json:"item"`
		}
		if err2 := json.Unmarshal(body, &envelope); err2 == nil {
			switch {
			case envelope.Ticket != nil:
				ticket = *envelope.Ticket
			case envelope.Data != nil:
				ticket = *envelope.Data
			case envelope.Item != nil:
				ticket = *envelope.Item
			default:
				return APITicket{}, fmt.Errorf("resposta inválida: ticket não encontrado no payload")
			}
		} else {
			return APITicket{}, fmt.Errorf("resposta inválida: %w", err)
		}
	}

	return ticket, nil
}

func parseWorkflowStatesFromBody(body []byte) ([]APIWorkflowState, error) {
	var states []APIWorkflowState
	if err := json.Unmarshal(body, &states); err == nil {
		return states, nil
	}

	var envelope struct {
		Items []APIWorkflowState `json:"items"`
		Data  []APIWorkflowState `json:"data"`
		State []APIWorkflowState `json:"states"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}

	switch {
	case envelope.Items != nil:
		return envelope.Items, nil
	case envelope.Data != nil:
		return envelope.Data, nil
	case envelope.State != nil:
		return envelope.State, nil
	default:
		return []APIWorkflowState{}, nil
	}
}

// GetTicketWorkflowStates returns available workflow states for tickets.
// It probes known endpoints because deployments may expose different routes.
func (a *App) GetTicketWorkflowStates() ([]APIWorkflowState, error) {
	cfg := a.GetDebugConfig()
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	base := strings.TrimSpace(cfg.ApiScheme) + "://" + strings.TrimSpace(cfg.ApiServer)
	paths := []string{
		"/api/agent-auth/me/tickets/workflow-states",
		"/api/agent-auth/me/workflow-states",
		"/api/agent-auth/workflow-states",
	}

	var lastErr error
	for _, p := range paths {
		target := base + p
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			lastErr = fmt.Errorf("URL inválida: %w", err)
			continue
		}
		setAgentAuthHeaders(req, cfg.AuthToken)

		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			lastErr = fmt.Errorf("falha ao buscar estados de workflow: %w", err)
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			lastErr = fmt.Errorf("endpoint não encontrado em %s", p)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
			continue
		}

		states, err := parseWorkflowStatesFromBody(body)
		if err != nil {
			lastErr = fmt.Errorf("resposta inválida de estados de workflow: %w", err)
			continue
		}

		if states == nil {
			states = []APIWorkflowState{}
		}

		sort.SliceStable(states, func(i, j int) bool {
			if states[i].DisplayOrder == states[j].DisplayOrder {
				return strings.ToLower(states[i].Name) < strings.ToLower(states[j].Name)
			}
			return states[i].DisplayOrder < states[j].DisplayOrder
		})

		a.supportLogf("workflow states carregados: %d estado(s) via %s", len(states), p)
		return states, nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("não foi possível carregar estados de workflow")
	}
	return nil, lastErr
}

// GetTicketComments returns comments for a given ticket.
func (a *App) GetTicketComments(ticketID string) ([]TicketComment, error) {
	ticketID = strings.TrimSpace(ticketID)
	if !guidPattern.MatchString(ticketID) {
		return nil, fmt.Errorf("ticketId inválido")
	}
	cfg := a.GetDebugConfig()
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	target := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/agent-auth/me/tickets/" + ticketID + "/comments"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	setAgentAuthHeaders(req, cfg.AuthToken)

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("falha ao buscar comentários: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var comments []TicketComment
	if err := json.Unmarshal(body, &comments); err != nil {
		var envelope struct {
			Items []TicketComment `json:"items"`
			Data  []TicketComment `json:"data"`
		}
		if err2 := json.Unmarshal(body, &envelope); err2 == nil {
			if envelope.Items != nil {
				comments = envelope.Items
			} else {
				comments = envelope.Data
			}
		} else {
			return nil, fmt.Errorf("resposta inválida: %w", err)
		}
	}
	if comments == nil {
		comments = []TicketComment{}
	}
	return comments, nil
}

// AddTicketCommentWithOptions adds a comment and returns the created comment.
func (a *App) AddTicketCommentWithOptions(ticketID, content string, isInternal bool) (TicketComment, error) {
	ticketID = strings.TrimSpace(ticketID)
	if !guidPattern.MatchString(ticketID) {
		return TicketComment{}, fmt.Errorf("ticketId inválido")
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return TicketComment{}, fmt.Errorf("content não pode ser vazio")
	}

	cfg := a.GetDebugConfig()
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	payload := map[string]any{
		"content":    content,
		"isInternal": isInternal,
	}
	body, _ := json.Marshal(payload)

	target := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/agent-auth/me/tickets/" + ticketID + "/comments"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return TicketComment{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	setAgentAuthHeaders(req, cfg.AuthToken)

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return TicketComment{}, fmt.Errorf("falha ao enviar comentário: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return TicketComment{}, fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	var created TicketComment
	if len(respBody) == 0 {
		return created, nil
	}
	if err := json.Unmarshal(respBody, &created); err != nil {
		return TicketComment{}, fmt.Errorf("resposta inválida ao criar comentário: %w", err)
	}
	return created, nil
}

// AddTicketComment adds a comment to a ticket.
func (a *App) AddTicketComment(ticketID, author, content string) error {
	_ = author // Mantido por compatibilidade da assinatura pública do método.
	_, err := a.AddTicketCommentWithOptions(ticketID, content, false)
	if err != nil {
		return err
	}
	return nil
}

// CloseSupportTicket closes a ticket with optional rating/comment/final workflow state.
func (a *App) CloseSupportTicket(ticketID string, input CloseTicketInput) (APITicket, error) {
	ticketID = strings.TrimSpace(ticketID)
	if !guidPattern.MatchString(ticketID) {
		return APITicket{}, fmt.Errorf("ticketId inválido")
	}

	workflowStateID := strings.TrimSpace(input.WorkflowStateID)
	if workflowStateID != "" && !guidPattern.MatchString(workflowStateID) {
		return APITicket{}, fmt.Errorf("workflowStateId inválido")
	}

	if input.Rating != nil {
		if *input.Rating < 0 || *input.Rating > 5 {
			return APITicket{}, fmt.Errorf("rating inválido: informe valor entre 0 e 5")
		}
	}

	cfg := a.GetDebugConfig()
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	payload := map[string]any{}
	if input.Rating != nil {
		payload["rating"] = *input.Rating
	}
	if c := strings.TrimSpace(input.Comment); c != "" {
		payload["comment"] = c
	}
	if workflowStateID != "" {
		payload["workflowStateId"] = workflowStateID
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return APITicket{}, fmt.Errorf("erro ao serializar payload de fechamento: %w", err)
	}

	target := cfg.ApiScheme + "://" + cfg.ApiServer + "/api/agent-auth/me/tickets/" + ticketID + "/close"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return APITicket{}, fmt.Errorf("URL inválida: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	setAgentAuthHeaders(req, cfg.AuthToken)

	a.supportLogf("fechando chamado %s", ticketID)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return APITicket{}, fmt.Errorf("falha ao fechar chamado: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return APITicket{}, fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	var ticket APITicket
	if len(respBody) == 0 {
		a.supportLogf("chamado %s fechado (resposta vazia); buscando detalhes atualizados", ticketID)
		return a.GetSupportTicketDetails(ticketID)
	}
	if err := json.Unmarshal(respBody, &ticket); err != nil {
		var envelope struct {
			Ticket *APITicket `json:"ticket"`
			Data   *APITicket `json:"data"`
			Item   *APITicket `json:"item"`
		}
		if err2 := json.Unmarshal(respBody, &envelope); err2 == nil {
			switch {
			case envelope.Ticket != nil:
				ticket = *envelope.Ticket
			case envelope.Data != nil:
				ticket = *envelope.Data
			case envelope.Item != nil:
				ticket = *envelope.Item
			default:
				return APITicket{}, fmt.Errorf("resposta inválida ao fechar chamado")
			}
		} else {
			return APITicket{}, fmt.Errorf("resposta inválida ao fechar chamado: %w", err)
		}
	}

	a.supportLogf("chamado fechado com sucesso: ticketId=%s", ticket.ID)
	return ticket, nil
}

// CloseAgentTicket closes an agent ticket via MCP tool.
func (a *App) CloseAgentTicket(ticketID string, rating *int, comment, workflowStateID string) (json.RawMessage, error) {
	ticket, err := a.CloseSupportTicket(ticketID, CloseTicketInput{
		Rating:          rating,
		Comment:         comment,
		WorkflowStateID: workflowStateID,
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(ticket)
}

// GetAgentInfoJSON returns the agent info as JSON (for MCP tools).
func (a *App) GetAgentInfoJSON() (json.RawMessage, error) {
	info, err := a.fetchAgentContext()
	if err != nil {
		return nil, err
	}
	return json.Marshal(info)
}

// ListAgentTickets returns agent tickets as JSON (for MCP tools).
func (a *App) ListAgentTickets() (json.RawMessage, error) {
	tickets, err := a.GetSupportTickets()
	if err != nil {
		return nil, err
	}
	return json.Marshal(tickets)
}

// GetAgentTicketDetails returns one agent ticket as JSON (for MCP tools).
func (a *App) GetAgentTicketDetails(ticketID string) (json.RawMessage, error) {
	ticket, err := a.GetSupportTicketDetails(ticketID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(ticket)
}

// AddAgentTicketComment adds a comment to an agent ticket via MCP tool.
func (a *App) AddAgentTicketComment(ticketID, content string, isInternal bool) (json.RawMessage, error) {
	comment, err := a.AddTicketCommentWithOptions(ticketID, content, isInternal)
	if err != nil {
		return nil, err
	}
	return json.Marshal(comment)
}

// CreateAgentTicket creates a ticket via MCP tool.
func (a *App) CreateAgentTicket(title, description string, priority int, category string) (json.RawMessage, error) {
	ticket, err := a.CreateSupportTicket(CreateTicketInput{
		Title:       title,
		Description: description,
		Priority:    priority,
		Category:    category,
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(ticket)
}

func toStringSlice(value any) []string {
	arr, ok := value.([]any)
	if !ok {
		if strArr, ok := value.([]string); ok {
			return strArr
		}
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		s := strings.TrimSpace(fmt.Sprint(item))
		if s != "" && s != "<nil>" {
			out = append(out, s)
		}
	}
	return out
}

func estimateReadTimeMin(markdown string) int {
	words := len(strings.Fields(strings.TrimSpace(markdown)))
	if words <= 0 {
		return 1
	}
	minutes := words / 180
	if words%180 != 0 {
		minutes++
	}
	if minutes < 1 {
		minutes = 1
	}
	return minutes
}

func buildSummary(content string) string {
	if strings.TrimSpace(content) == "" {
		return ""
	}
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimLeft(line, "#*-0123456789. "))
		if line != "" {
			if len(line) > 180 {
				return line[:180] + "..."
			}
			return line
		}
	}
	return ""
}

func parseKnowledgeArticle(raw map[string]any) KnowledgeArticle {
	article := KnowledgeArticle{
		ID:          strings.TrimSpace(fmt.Sprint(raw["id"])),
		Title:       strings.TrimSpace(fmt.Sprint(raw["title"])),
		Category:    strings.TrimSpace(fmt.Sprint(raw["category"])),
		Summary:     strings.TrimSpace(fmt.Sprint(raw["summary"])),
		Content:     strings.TrimSpace(fmt.Sprint(raw["content"])),
		Tags:        toStringSlice(raw["tags"]),
		Author:      strings.TrimSpace(fmt.Sprint(raw["author"])),
		Scope:       strings.TrimSpace(fmt.Sprint(raw["scope"])),
		PublishedAt: strings.TrimSpace(fmt.Sprint(raw["publishedAt"])),
		Difficulty:  strings.TrimSpace(fmt.Sprint(raw["difficulty"])),
		UpdatedAt:   strings.TrimSpace(fmt.Sprint(raw["updatedAt"])),
	}

	if article.ID == "<nil>" {
		article.ID = ""
	}
	if article.Title == "<nil>" {
		article.Title = ""
	}
	if article.Category == "<nil>" {
		article.Category = ""
	}
	if article.Summary == "<nil>" {
		article.Summary = ""
	}
	if article.Content == "<nil>" {
		article.Content = ""
	}
	if article.Author == "<nil>" {
		article.Author = ""
	}
	if article.Scope == "<nil>" {
		article.Scope = ""
	}
	if article.PublishedAt == "<nil>" {
		article.PublishedAt = ""
	}
	if article.Difficulty == "<nil>" {
		article.Difficulty = ""
	}
	if article.UpdatedAt == "<nil>" {
		article.UpdatedAt = ""
	}

	if article.Summary == "" {
		article.Summary = buildSummary(article.Content)
	}
	if article.Difficulty == "" {
		scope := strings.ToLower(strings.TrimSpace(article.Scope))
		switch scope {
		case "global":
			article.Difficulty = "Global"
		case "client":
			article.Difficulty = "Cliente"
		case "site":
			article.Difficulty = "Site"
		}
	}

	article.ReadTimeMin = toInt(raw["readTimeMin"], raw["readTime"])
	if article.ReadTimeMin <= 0 {
		article.ReadTimeMin = estimateReadTimeMin(article.Content)
	}
	if article.UpdatedAt == "" {
		article.UpdatedAt = article.PublishedAt
	}

	return article
}

func parseKnowledgeListBody(body []byte) ([]KnowledgeArticle, error) {
	var direct []map[string]any
	if err := json.Unmarshal(body, &direct); err == nil {
		out := make([]KnowledgeArticle, 0, len(direct))
		for _, item := range direct {
			out = append(out, parseKnowledgeArticle(item))
		}
		return out, nil
	}

	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}

	for _, key := range []string{"items", "data", "articles", "knowledge", "result"} {
		arr, ok := envelope[key].([]any)
		if !ok {
			continue
		}
		out := make([]KnowledgeArticle, 0, len(arr))
		for _, entry := range arr {
			if m, ok := entry.(map[string]any); ok {
				out = append(out, parseKnowledgeArticle(m))
			}
		}
		return out, nil
	}

	return []KnowledgeArticle{}, nil
}

func parseKnowledgeDetailBody(body []byte) (KnowledgeArticle, error) {
	var direct map[string]any
	if err := json.Unmarshal(body, &direct); err != nil {
		return KnowledgeArticle{}, err
	}

	for _, key := range []string{"item", "data", "article", "result"} {
		if inner, ok := direct[key].(map[string]any); ok {
			return parseKnowledgeArticle(inner), nil
		}
	}

	return parseKnowledgeArticle(direct), nil
}

const (
	knowledgeListCacheTTL   = 5 * time.Minute
	knowledgeDetailCacheTTL = 30 * time.Minute
)

func knowledgeCacheScope(cfg DebugConfig, info AgentInfo) string {
	parts := []string{
		strings.TrimSpace(strings.ToLower(cfg.ApiScheme)),
		strings.TrimSpace(strings.ToLower(cfg.ApiServer)),
		strings.TrimSpace(strings.ToLower(info.ClientID)),
		strings.TrimSpace(strings.ToLower(info.SiteID)),
		strings.TrimSpace(strings.ToLower(info.AgentID)),
	}
	for i, p := range parts {
		parts[i] = url.QueryEscape(p)
	}
	return strings.Join(parts, ":")
}

func (a *App) fetchKnowledgeList(info AgentInfo, category string) ([]KnowledgeArticle, error) {
	cfg := a.GetDebugConfig()
	base := strings.TrimSpace(strings.ToLower(cfg.ApiScheme)) + "://" + strings.TrimSpace(cfg.ApiServer)
	if strings.TrimSpace(cfg.ApiServer) == "" || strings.TrimSpace(cfg.AuthToken) == "" {
		return nil, fmt.Errorf("configuração de servidor API incompleta: preencha apiServer e token no Debug")
	}
	cacheKey := "knowledge:list:" + knowledgeCacheScope(cfg, info) + ":" + url.QueryEscape(strings.TrimSpace(strings.ToLower(category)))

	if a.db != nil {
		var cached []KnowledgeArticle
		if found, err := a.db.CacheGetJSON(cacheKey, &cached); err == nil && found {
			if cached == nil {
				return []KnowledgeArticle{}, nil
			}
			return cached, nil
		}
	}

	path := "/api/agent-auth/knowledge"
	if c := strings.TrimSpace(category); c != "" {
		path += "?category=" + url.QueryEscape(c)
	}
	target := base + path

	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("URL inválida: %w", err)
	}
	setAgentAuthHeaders(req, cfg.AuthToken)

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("falha ao buscar artigos da base de conhecimento: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	articles, err := parseKnowledgeListBody(body)
	if err != nil {
		return nil, fmt.Errorf("resposta inválida ao listar artigos: %w", err)
	}
	if articles == nil {
		articles = []KnowledgeArticle{}
	}

	if a.db != nil {
		if err := a.db.CacheSetJSON(cacheKey, articles, knowledgeListCacheTTL); err != nil {
			log.Printf("[support] aviso: falha ao salvar cache de knowledge list: %v", err)
		}
	}

	return articles, nil
}

func (a *App) fetchKnowledgeDetail(info AgentInfo, articleID string) (KnowledgeArticle, error) {
	articleID = strings.TrimSpace(articleID)
	if articleID == "" {
		return KnowledgeArticle{}, fmt.Errorf("articleId inválido")
	}

	cfg := a.GetDebugConfig()
	if strings.TrimSpace(cfg.ApiServer) == "" || strings.TrimSpace(cfg.AuthToken) == "" {
		return KnowledgeArticle{}, fmt.Errorf("configuração de servidor API incompleta: preencha apiServer e token no Debug")
	}
	cacheKey := "knowledge:detail:" + knowledgeCacheScope(cfg, info) + ":" + url.QueryEscape(strings.ToLower(articleID))

	if a.db != nil {
		var cached KnowledgeArticle
		if found, err := a.db.CacheGetJSON(cacheKey, &cached); err == nil && found {
			if strings.TrimSpace(cached.ID) != "" {
				return cached, nil
			}
		}
	}

	target := strings.TrimSpace(strings.ToLower(cfg.ApiScheme)) + "://" + strings.TrimSpace(cfg.ApiServer) + "/api/agent-auth/knowledge/" + url.PathEscape(articleID)

	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return KnowledgeArticle{}, fmt.Errorf("URL inválida: %w", err)
	}
	setAgentAuthHeaders(req, cfg.AuthToken)

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return KnowledgeArticle{}, fmt.Errorf("falha ao buscar detalhe do artigo: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return KnowledgeArticle{}, fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	article, err := parseKnowledgeDetailBody(body)
	if err != nil {
		return KnowledgeArticle{}, fmt.Errorf("resposta inválida no detalhe do artigo: %w", err)
	}

	if a.db != nil && strings.TrimSpace(article.ID) != "" {
		if err := a.db.CacheSetJSON(cacheKey, article, knowledgeDetailCacheTTL); err != nil {
			log.Printf("[support] aviso: falha ao salvar cache de knowledge detail: %v", err)
		}
	}

	return article, nil
}

// GetKnowledgeBaseArticles returns knowledge-base articles available to the authenticated agent.
func (a *App) GetKnowledgeBaseArticles() []KnowledgeArticle {
	info, err := a.fetchAgentContext()
	if err != nil {
		a.supportLogf("falha ao resolver contexto para knowledge base: %v", err)
		return []KnowledgeArticle{}
	}

	articles, err := a.fetchKnowledgeList(info, "")
	if err != nil {
		a.supportLogf("falha ao listar base de conhecimento: %v", err)
		return []KnowledgeArticle{}
	}

	for i := range articles {
		if strings.TrimSpace(articles[i].Content) != "" || strings.TrimSpace(articles[i].ID) == "" {
			continue
		}
		detail, err := a.fetchKnowledgeDetail(info, articles[i].ID)
		if err != nil {
			a.supportLogf("falha ao carregar markdown do artigo %s: %v", articles[i].ID, err)
			continue
		}
		if strings.TrimSpace(detail.Content) != "" {
			articles[i].Content = detail.Content
		}
		if strings.TrimSpace(articles[i].Summary) == "" {
			articles[i].Summary = detail.Summary
		}
		if len(articles[i].Tags) == 0 {
			articles[i].Tags = detail.Tags
		}
	}

	return articles
}

// SearchKnowledgeBaseArticles filters articles by title/category/tags/content.
func (a *App) SearchKnowledgeBaseArticles(query string) []KnowledgeArticle {
	articles := a.GetKnowledgeBaseArticles()
	q := strings.TrimSpace(strings.ToLower(query))
	if q == "" {
		return articles
	}

	matches := make([]KnowledgeArticle, 0, len(articles))
	for _, article := range articles {
		if strings.Contains(strings.ToLower(article.Title), q) ||
			strings.Contains(strings.ToLower(article.Category), q) ||
			strings.Contains(strings.ToLower(article.Summary), q) ||
			strings.Contains(strings.ToLower(article.Content), q) ||
			strings.Contains(strings.ToLower(article.Author), q) ||
			strings.Contains(strings.ToLower(article.Scope), q) {
			matches = append(matches, article)
			continue
		}

		for _, tag := range article.Tags {
			if strings.Contains(strings.ToLower(tag), q) {
				matches = append(matches, article)
				break
			}
		}
	}

	return matches
}
