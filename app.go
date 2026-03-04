package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
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
	"winget-store/internal/export"
	"winget-store/internal/inventory"
	"winget-store/internal/mcp"
	"winget-store/internal/models"
	"winget-store/internal/processutil"
	"winget-store/internal/services"
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

// SupportTicket represents a mock support ticket.
type SupportTicket struct {
	ID          string `json:"id"`
	Subject     string `json:"subject"`
	Category    string `json:"category"`
	Priority    string `json:"priority"`
	Description string `json:"description"`
	Status      string `json:"status"`
	CreatedAt   string `json:"createdAt"`
}

// KnowledgeArticle represents a knowledge base article for support guidance.
type KnowledgeArticle struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Category    string   `json:"category"`
	Summary     string   `json:"summary"`
	Content     string   `json:"content"`
	Tags        []string `json:"tags"`
	Difficulty  string   `json:"difficulty"`
	ReadTimeMin int      `json:"readTimeMin"`
	UpdatedAt   string   `json:"updatedAt"`
}

// ticketStore manages an in-memory list of support tickets.
type ticketStore struct {
	mu      sync.RWMutex
	tickets []SupportTicket
	nextID  int
}

func (ts *ticketStore) create(t SupportTicket) SupportTicket {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.nextID++
	t.ID = fmt.Sprintf("TK-%04d", ts.nextID)
	t.Status = "Aberto"
	t.CreatedAt = time.Now().Format("02/01/2006 15:04")
	ts.tickets = append(ts.tickets, t)
	return t
}

func (ts *ticketStore) getAll() []SupportTicket {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	out := make([]SupportTicket, len(ts.tickets))
	copy(out, ts.tickets)
	return out
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

type App struct {
	ctx        context.Context
	cancel     context.CancelFunc
	catalogSvc *services.CatalogService
	appsSvc    *services.AppsService
	invSvc     *services.InventoryService

	invCache  inventoryCache
	exportCfg exportConfig
	logs      logBuffer

	mcpRegistry *mcp.Registry
	chatSvc     *ai.Service
	agentConn   *agentconn.Runtime
	tickets     ticketStore
	knowledge   []KnowledgeArticle

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

	a := &App{
		catalogSvc:  services.NewCatalogService(catalogClient),
		appsSvc:     services.NewAppsService(wingetClient),
		invSvc:      services.NewInventoryService(inventoryProvider),
		mcpRegistry: reg,
		chatSvc:     chatSvc,
		knowledge:   mockKnowledgeBaseArticles(),
	}
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

	return a
}

func (a *App) startup(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	a.ctx = ctx
	a.cancel = cancel
	a.startTray()
	a.applyIdleMode(true)
	a.startupWg.Add(1)
	go func() {
		defer a.startupWg.Done()
		done := a.beginActivity("inventario inicial")
		defer done()
		report, err := a.invSvc.GetInventory(ctx)
		if err != nil {
			log.Printf("[startup] falha ao coletar inventario em background: %v", err)
			a.startupMu.Lock()
			a.startupErr = err
			a.startupMu.Unlock()
			return
		}
		a.invCache.set(report)
		a.syncInventoryOnStartup(ctx, report)
	}()

	a.startupWg.Add(1)
	go func() {
		defer a.startupWg.Done()
		a.agentConn.Run(ctx)
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

func (a *App) GetInventory() (models.InventoryReport, error) {
	done := a.beginActivity("coleta de inventario")
	defer done()
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

func (a *App) RefreshInventory() (models.InventoryReport, error) {
	done := a.beginActivity("atualizacao de inventario")
	defer done()
	report, err := a.invSvc.GetInventory(a.ctx)
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
	Scheme    string `json:"scheme"`    // "http", "https" or "nats"
	Server    string `json:"server"`    // hostname:port or IP
	AuthToken string `json:"authToken"` // bearer token
	AgentID   string `json:"agentId"`   // agent identifier
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
	cfg.Scheme = strings.TrimSpace(strings.ToLower(cfg.Scheme))
	if !isValidDebugScheme(cfg.Scheme) {
		return fmt.Errorf("scheme invalido: use 'http', 'https' ou 'nats'")
	}
	if strings.TrimSpace(cfg.Server) == "" {
		return fmt.Errorf("servidor nao pode ser vazio")
	}
	if cfg.Scheme == "nats" && !guidPattern.MatchString(strings.TrimSpace(cfg.AgentID)) {
		return fmt.Errorf("agentId invalido para NATS: informe um GUID valido")
	}

	a.logs.append(fmt.Sprintf("[debug] atualizando configuracao: scheme=%s server=%s agentId=%s", cfg.Scheme, strings.TrimSpace(cfg.Server), strings.TrimSpace(cfg.AgentID)))

	a.debugMu.Lock()
	a.debugConfig = cfg
	a.debugMu.Unlock()

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

// TestDebugConnection calls GET {scheme}://{server}/api/agent-auth/me with the
// Bearer token and returns the raw response body (or a descriptive error).
func (a *App) TestDebugConnection(cfg DebugConfig) (string, error) {
	cfg.Scheme = strings.TrimSpace(strings.ToLower(cfg.Scheme))
	if !isValidDebugScheme(cfg.Scheme) {
		return "", fmt.Errorf("scheme invalido: use 'http', 'https' ou 'nats'")
	}
	server := strings.TrimSpace(cfg.Server)
	if server == "" {
		return "", fmt.Errorf("servidor nao pode ser vazio")
	}
	a.logs.append(fmt.Sprintf("[debug-test] iniciando teste: scheme=%s server=%s agentId=%s", cfg.Scheme, server, strings.TrimSpace(cfg.AgentID)))
	if cfg.Scheme == "nats" {
		if !guidPattern.MatchString(strings.TrimSpace(cfg.AgentID)) {
			err := fmt.Errorf("agentId invalido para NATS: informe um GUID valido")
			a.logs.append("[debug-test] falha: " + err.Error())
			return "", err
		}
		out, err := agentconn.FetchNATSInfo(server, 10*time.Second)
		if err != nil {
			a.logs.append("[debug-test] falha no teste NATS: " + err.Error())
			return "", err
		}
		a.logs.append("[debug-test] teste NATS concluido com sucesso")
		return out, nil
	}

	target := cfg.Scheme + "://" + server + "/api/agent-auth/me"
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return "", fmt.Errorf("URL invalida: %w", err)
	}
	if strings.TrimSpace(cfg.AuthToken) != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	}
	if strings.TrimSpace(cfg.AgentID) != "" {
		req.Header.Set("X-Agent-ID", cfg.AgentID)
	}

	resp, err := client.Do(req)
	if err != nil {
		wrapped := fmt.Errorf("falha ao conectar em %s: %w", target, err)
		a.logs.append("[debug-test] falha no teste HTTP: " + wrapped.Error())
		return "", wrapped
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		wrapped := fmt.Errorf("erro ao ler resposta (%s): %w", resp.Status, err)
		a.logs.append("[debug-test] falha no teste HTTP: " + wrapped.Error())
		return "", wrapped
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		wrapped := fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
		a.logs.append("[debug-test] falha no teste HTTP: " + wrapped.Error())
		return "", wrapped
	}

	// Pretty-print JSON if possible
	var pretty interface{}
	if json.Unmarshal(body, &pretty) == nil {
		if formatted, err := json.MarshalIndent(pretty, "", "  "); err == nil {
			a.logs.append("[debug-test] teste HTTP concluido com sucesso")
			return string(formatted), nil
		}
	}
	a.logs.append("[debug-test] teste HTTP concluido com sucesso")
	return string(body), nil
}

// GetRealtimeStatus queries /api/realtime/status from the configured HTTP server.
func (a *App) GetRealtimeStatus() (RealtimeStatus, error) {
	cfg := a.GetDebugConfig()
	cfg.Scheme = strings.TrimSpace(strings.ToLower(cfg.Scheme))
	if cfg.Scheme != "http" && cfg.Scheme != "https" {
		return RealtimeStatus{}, fmt.Errorf("status indisponivel: configure scheme http/https")
	}
	server := strings.TrimSpace(cfg.Server)
	if server == "" {
		return RealtimeStatus{}, fmt.Errorf("servidor nao pode ser vazio")
	}

	target := cfg.Scheme + "://" + server + "/api/realtime/status"
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
	Hostname               string                    `json:"hostname"`
	DisplayName            string                    `json:"displayName"`
	Status                 int                       `json:"status"`
	OperatingSystem        string                    `json:"operatingSystem"`
	OSVersion              string                    `json:"osVersion"`
	AgentVersion           string                    `json:"agentVersion"`
	LastIPAddress          string                    `json:"lastIpAddress"`
	MACAddress             string                    `json:"macAddress"`
	Hardware               agentHardwareInfo         `json:"hardware"`
	Disks                  []agentDiskInfo           `json:"disks"`
	NetworkAdapters        []agentNetworkAdapterInfo `json:"networkAdapters"`
	MemoryModules          []agentMemoryModuleInfo   `json:"memoryModules"`
	InventoryRaw           string                    `json:"inventoryRaw"`
	InventorySchemaVersion string                    `json:"inventorySchemaVersion"`
	InventoryCollectedAt   string                    `json:"inventoryCollectedAt"`
}

type agentHardwareInfo struct {
	InventoryRaw            string `json:"inventoryRaw"`
	InventorySchemaVersion  string `json:"inventorySchemaVersion"`
	InventoryCollectedAt    string `json:"inventoryCollectedAt"`
	Manufacturer            string `json:"manufacturer"`
	Model                   string `json:"model"`
	SerialNumber            string `json:"serialNumber"`
	MotherboardManufacturer string `json:"motherboardManufacturer"`
	MotherboardModel        string `json:"motherboardModel"`
	MotherboardSerialNumber string `json:"motherboardSerialNumber"`
	Processor               string `json:"processor"`
	ProcessorCores          int    `json:"processorCores"`
	ProcessorThreads        int    `json:"processorThreads"`
	ProcessorArchitecture   string `json:"processorArchitecture"`
	TotalMemoryBytes        int64  `json:"totalMemoryBytes"`
	BIOSVersion             string `json:"biosVersion"`
	BIOSManufacturer        string `json:"biosManufacturer"`
	OSName                  string `json:"osName"`
	OSVersion               string `json:"osVersion"`
	OSBuild                 string `json:"osBuild"`
	OSArchitecture          string `json:"osArchitecture"`
	CollectedAt             string `json:"collectedAt"`
	UpdatedAt               string `json:"updatedAt"`
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
	cfg := a.GetDebugConfig()
	server := strings.TrimSpace(cfg.Server)
	if cfg.Scheme != "http" && cfg.Scheme != "https" {
		a.logs.append("[agent-sync] ignorado: sincronizacao HTTP indisponivel para scheme atual")
		return
	}
	if server == "" || strings.TrimSpace(cfg.AuthToken) == "" || strings.TrimSpace(cfg.AgentID) == "" {
		a.logs.append("[agent-sync] ignorado: faltam server/token/agentId no Debug")
		return
	}

	hardwarePayload := buildAgentHardwareEnvelope(report)
	a.logs.append(fmt.Sprintf(
		"[agent-sync] hardware payload: collectedAt=%s disks=%d networkAdapters=%d memoryModules=%d hostname=%s",
		hardwarePayload.InventoryCollectedAt,
		len(hardwarePayload.Disks),
		len(hardwarePayload.NetworkAdapters),
		len(hardwarePayload.MemoryModules),
		hardwarePayload.Hostname,
	))
	hardwareBody, err := json.Marshal(hardwarePayload)
	if err != nil {
		a.logs.append("[agent-sync] falha ao serializar inventario: " + err.Error())
	} else {
		hardwareEndpoint := cfg.Scheme + "://" + server + "/api/agent-auth/me/hardware"
		if err := a.sendAgentInventoryRequest(ctx, hardwareEndpoint, cfg, http.MethodPost, hardwareBody); err != nil {
			a.logs.append("[agent-sync] POST hardware falhou: " + err.Error())
			if err := a.sendAgentInventoryRequest(ctx, hardwareEndpoint, cfg, http.MethodPut, hardwareBody); err != nil {
				a.logs.append("[agent-sync] PUT hardware falhou: " + err.Error())
			} else {
				a.logs.append("[agent-sync] inventario de hardware atualizado via PUT")
			}
		} else {
			a.logs.append("[agent-sync] inventario de hardware enviado via POST")
		}
	}

	softwarePayload := buildAgentSoftwareEnvelope(report)
	a.logs.append(fmt.Sprintf(
		"[agent-sync] software payload: collectedAt=%s softwareCount=%d",
		softwarePayload.CollectedAt,
		len(softwarePayload.Software),
	))
	softwareBody, err := json.Marshal(softwarePayload)
	if err != nil {
		a.logs.append("[agent-sync] falha ao serializar softwares: " + err.Error())
		return
	}

	softwareEndpoint := cfg.Scheme + "://" + server + "/api/agent-auth/me/software"
	a.logs.append("[agent-sync] endpoint software: " + softwareEndpoint)
	if err := a.sendAgentInventoryRequest(ctx, softwareEndpoint, cfg, http.MethodPost, softwareBody); err != nil {
		a.logs.append("[agent-sync] POST software falhou: " + err.Error())
		if err := a.sendAgentInventoryRequest(ctx, softwareEndpoint, cfg, http.MethodPut, softwareBody); err != nil {
			a.logs.append("[agent-sync] PUT software falhou: " + err.Error())
			return
		}
		a.logs.append("[agent-sync] inventario de software atualizado via PUT")
		return
	}
	a.logs.append("[agent-sync] inventario de software enviado via POST")
}

func (a *App) sendAgentInventoryRequest(parent context.Context, endpoint string, cfg DebugConfig, method string, body []byte) error {
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
		return err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	return nil
}

func buildAgentSoftwareEnvelope(report models.InventoryReport) agentSoftwareEnvelope {
	collected := strings.TrimSpace(report.CollectedAt)
	if collected == "" {
		collected = time.Now().UTC().Format(time.RFC3339)
	}

	software := make([]agentSoftwareItem, 0, len(report.Software))
	for _, s := range report.Software {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			continue
		}
		source := strings.TrimSpace(s.Source)
		if source == "" {
			source = "osquery/programs"
		}
		software = append(software, agentSoftwareItem{
			Name:      name,
			Version:   strings.TrimSpace(s.Version),
			Publisher: strings.TrimSpace(s.Publisher),
			InstallID: strings.TrimSpace(s.InstallID),
			Serial:    strings.TrimSpace(s.Serial),
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
		total := int64(d.SizeGB * 1024 * 1024 * 1024)
		if total < 0 {
			total = 0
		}
		free := int64(d.FreeGB * 1024 * 1024 * 1024)
		if free < 0 || !d.FreeKnown {
			free = 0
		}
		disks = append(disks, agentDiskInfo{
			DriveLetter:    normalizeDriveLetter(d.Device),
			Label:          d.Label,
			FileSystem:     d.FileSystem,
			TotalSizeBytes: total,
			FreeSpaceBytes: free,
			MediaType:      d.Type,
			CollectedAt:    collected,
		})
	}

	adapters := make([]agentNetworkAdapterInfo, 0, len(report.Networks))
	for _, n := range report.Networks {
		name := firstNonEmptyString(strings.TrimSpace(n.FriendlyName), strings.TrimSpace(n.Interface))
		adapters = append(adapters, agentNetworkAdapterInfo{
			Name:          name,
			MACAddress:    n.MAC,
			IPAddress:     firstNonEmptyString(strings.TrimSpace(n.IPv4), strings.TrimSpace(n.IPv6)),
			SubnetMask:    "",
			Gateway:       n.Gateway,
			DNSServers:    normalizeDNSServers(n.DNSServers),
			IsDhcpEnabled: n.DHCPEnabled,
			AdapterType:   n.Type,
			Speed:         formatLinkSpeed(n.LinkSpeedMbps),
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
			Slot:          m.Slot,
			CapacityBytes: capacity,
			SpeedMhz:      m.SpeedMHz,
			MemoryType:    m.Type,
			Manufacturer:  m.Manufacturer,
			PartNumber:    m.PartNumber,
			SerialNumber:  m.Serial,
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

	hostname := strings.TrimSpace(report.Hardware.Hostname)
	osName := strings.TrimSpace(report.OS.Name)
	osVersion := strings.TrimSpace(report.OS.Version)

	envelope := agentHardwareEnvelope{
		Hostname:        hostname,
		DisplayName:     hostname,
		Status:          1,
		OperatingSystem: osName,
		OSVersion:       osVersion,
		AgentVersion:    strings.TrimSpace(Version),
		LastIPAddress:   lastIP,
		MACAddress:      primaryMAC,
		Hardware: agentHardwareInfo{
			InventoryRaw:            rawJSON,
			InventorySchemaVersion:  "discovery.inventory.v1",
			InventoryCollectedAt:    collected,
			Manufacturer:            report.Hardware.Manufacturer,
			Model:                   report.Hardware.Model,
			SerialNumber:            report.Hardware.MotherboardSerial,
			MotherboardManufacturer: report.Hardware.MotherboardManufacturer,
			MotherboardModel:        report.Hardware.MotherboardModel,
			MotherboardSerialNumber: report.Hardware.MotherboardSerial,
			Processor:               report.Hardware.CPU,
			ProcessorCores:          report.Hardware.Cores,
			ProcessorThreads:        report.Hardware.LogicalCores,
			ProcessorArchitecture:   report.OS.Architecture,
			TotalMemoryBytes:        memTotalBytes,
			BIOSVersion:             report.Hardware.BIOSVersion,
			BIOSManufacturer:        report.Hardware.BIOSVendor,
			OSName:                  osName,
			OSVersion:               osVersion,
			OSBuild:                 report.OS.Build,
			OSArchitecture:          report.OS.Architecture,
			CollectedAt:             collected,
			UpdatedAt:               updated,
		},
		Disks:                  disks,
		NetworkAdapters:        adapters,
		MemoryModules:          modules,
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
) string {
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
		return "{}"
	}
	return string(b)
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
	go func() {
		defer done()
		_, err := a.chatSvc.SendStream(
			a.ctx,
			message,
			func(token string) {
				wailsRuntime.EventsEmit(a.ctx, "chat:token", token)
			},
			func(status string) {
				wailsRuntime.EventsEmit(a.ctx, "chat:thinking", status)
			},
		)
		if err != nil {
			wailsRuntime.EventsEmit(a.ctx, "chat:error", err.Error())
		} else {
			wailsRuntime.EventsEmit(a.ctx, "chat:done")
		}
	}()
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
// Support tickets (mock — in-memory)
// -----------------------------------------------------------------------

// CreateSupportTicket creates a new ticket and returns it.
func (a *App) CreateSupportTicket(t SupportTicket) SupportTicket {
	return a.tickets.create(t)
}

// GetSupportTickets returns all tickets.
func (a *App) GetSupportTickets() []SupportTicket {
	return a.tickets.getAll()
}

// GetKnowledgeBaseArticles returns all mock knowledge base articles.
func (a *App) GetKnowledgeBaseArticles() []KnowledgeArticle {
	out := make([]KnowledgeArticle, len(a.knowledge))
	copy(out, a.knowledge)
	return out
}

// SearchKnowledgeBaseArticles filters articles by title/category/tags/content.
func (a *App) SearchKnowledgeBaseArticles(query string) []KnowledgeArticle {
	q := strings.TrimSpace(strings.ToLower(query))
	if q == "" {
		return a.GetKnowledgeBaseArticles()
	}

	matches := make([]KnowledgeArticle, 0, len(a.knowledge))
	for _, article := range a.knowledge {
		if strings.Contains(strings.ToLower(article.Title), q) ||
			strings.Contains(strings.ToLower(article.Category), q) ||
			strings.Contains(strings.ToLower(article.Summary), q) ||
			strings.Contains(strings.ToLower(article.Content), q) {
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

func mockKnowledgeBaseArticles() []KnowledgeArticle {
	return []KnowledgeArticle{
		{
			ID:          "KB-001",
			Title:       "Checklist Rapido de Manutencao Preventiva para PCs",
			Category:    "Manutencao Preventiva",
			Summary:     "Passo a passo mensal para manter desktop e notebook estaveis e evitar travamentos.",
			Difficulty:  "Basico",
			ReadTimeMin: 6,
			UpdatedAt:   "02/03/2026",
			Tags:        []string{"poeira", "limpeza", "temperatura", "preventiva"},
			Content: `Objetivo
Aplicar uma rotina simples para reduzir superaquecimento, lentidao e falhas comuns.

Passos recomendados
1. Limpar entradas de ar, ventoinhas e filtros com pincel antiestatico.
2. Verificar temperatura media de CPU/GPU em uso comum e em carga.
3. Confirmar espaco livre no SSD/HDD (idealmente acima de 20%).
4. Revisar programas que iniciam com o Windows e desativar excessos.
5. Aplicar atualizacoes de sistema e drivers criticos.

Sinais de alerta
- Ventoinha constantemente em velocidade maxima.
- Quedas de desempenho apos 20-30 minutos de uso.
- Reinicios aleatorios durante tarefas simples.

Periodicidade sugerida
- Uso corporativo: mensal.
- Uso domestico moderado: a cada 2 meses.`,
		},
		{
			ID:          "KB-002",
			Title:       "PC Muito Lento: Diagnostico em 10 Minutos",
			Category:    "Desempenho",
			Summary:     "Fluxo rapido para identificar gargalo em disco, memoria, CPU ou software em segundo plano.",
			Difficulty:  "Intermediario",
			ReadTimeMin: 8,
			UpdatedAt:   "02/03/2026",
			Tags:        []string{"lentidao", "cpu", "memoria", "ssd", "startup"},
			Content: `Objetivo
Encontrar a principal causa de lentidao sem formatar a maquina.

Roteiro rapido
1. Abrir gerenciador de tarefas e observar uso de CPU, memoria e disco por 2 minutos.
2. Se disco em 100% frequente, checar saude do armazenamento e espaco livre.
3. Se memoria acima de 85% constante, revisar apps residentes e abas excessivas.
4. Se CPU alta sem motivo claro, verificar antivirais/scans agendados e processos suspeitos.
5. Confirmar versao do sistema e pendencias de update.

Acao imediata recomendada
- Remover softwares nao utilizados.
- Reduzir itens de inicializacao.
- Migrar de HDD para SSD quando aplicavel.

Quando escalar
- Lentidao persiste apos reinicializacao limpa.
- Disco apresenta erros SMART ou falhas de leitura.`,
		},
		{
			ID:          "KB-003",
			Title:       "Superaquecimento em Notebook: Causas e Correcao",
			Category:    "Hardware",
			Summary:     "Como validar fluxo de ar, pasta termica e perfil de energia para reduzir aquecimento.",
			Difficulty:  "Intermediario",
			ReadTimeMin: 7,
			UpdatedAt:   "02/03/2026",
			Tags:        []string{"temperatura", "cooler", "pasta termica", "energia"},
			Content: `Sintomas comuns
- Teclado e base muito quentes.
- Queda brusca de FPS ou travamentos ao abrir varias tarefas.

Checklist tecnico
1. Conferir obstrucao de saidas de ar e funcionamento das ventoinhas.
2. Verificar plano de energia (evitar modo desempenho maximo continuo).
3. Testar elevacao traseira do notebook para melhorar ventilacao.
4. Monitorar temperatura em repouso e em carga por 10 minutos.

Corretivas
- Limpeza interna completa.
- Troca de pasta termica em equipamento fora de garantia.
- Ajuste de limite de desempenho para uso diario.

Observacao
Se a temperatura sobe muito rapido mesmo em repouso, indicar avaliacao tecnica presencial.`,
		},
		{
			ID:          "KB-004",
			Title:       "Windows Nao Inicia: Procedimento Seguro de Recuperacao",
			Category:    "Sistema Operacional",
			Summary:     "Fluxo de recuperacao por etapas para evitar perda de dados em falhas de boot.",
			Difficulty:  "Avancado",
			ReadTimeMin: 9,
			UpdatedAt:   "02/03/2026",
			Tags:        []string{"boot", "reparo", "restauracao", "seguranca de dados"},
			Content: `Prioridade
Preservar dados antes de qualquer acao destrutiva.

Etapas
1. Tentar inicializacao em modo de recuperacao automatica.
2. Executar reparo de inicializacao.
3. Se falhar, abrir prompt de comando no ambiente de recuperacao e validar integridade do disco.
4. Restaurar para ponto anterior quando disponivel.
5. Como ultimo recurso, reinstalacao com backup previo.

Boas praticas
- Registrar mensagens de erro exibidas em tela.
- Validar backup em midia externa antes de formatar.

Nao recomendado
- Repetir desligamentos forcados em sequencia.
- Aplicar comandos sem confirmar a particao correta.`,
		},
		{
			ID:          "KB-005",
			Title:       "Troca de SSD: Pos-Migracao e Validacao Final",
			Category:    "Upgrade de Hardware",
			Summary:     "Itens de validacao apos clonagem ou instalacao limpa para garantir estabilidade do equipamento.",
			Difficulty:  "Basico",
			ReadTimeMin: 5,
			UpdatedAt:   "02/03/2026",
			Tags:        []string{"ssd", "upgrade", "clonagem", "desempenho"},
			Content: `Checklist pos-migracao
1. Confirmar reconhecimento do SSD no sistema e BIOS/UEFI.
2. Validar particao de boot e ordem de inicializacao.
3. Verificar espaco livre e integridade basica do sistema.
4. Executar atualizacoes pendentes e reiniciar.
5. Rodar teste rapido de leitura/escrita para comparar com esperado.

Resultado esperado
- Inicializacao mais rapida.
- Menor tempo para abrir aplicativos.
- Reducao de congelamentos intermitentes.

Se houver problema
- Revisar modo SATA/UEFI.
- Confirmar se clonagem copiou particoes de sistema corretamente.`,
		},
	}
}
