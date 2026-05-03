package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"discovery/internal/agentconn"
	"discovery/internal/automation"
	"discovery/internal/database"
	"discovery/internal/selfupdate"
)

// ServiceManager gerencia o Windows Service (headless)
// Responsabilidades:
// - Manter o serviço rodando 24/7
// - Executar automação, P2P, inventory sincronização
// - Escutar conexões Named Pipe de UI apps
// - Coordenar múltiplos usuários logados
type AutomationService interface {
	RefreshPolicy(ctx context.Context, includeScriptContent bool) (interface{}, error)
	// GetCurrentTasks retorna as tarefas de automação carregadas em memória (estado online).
	// Retorna nil se o serviço ainda não tiver uma política sincronizada em memória.
	GetCurrentTasks() []automation.AutomationTask
}

type InventoryService interface {
	Collect(ctx context.Context) (interface{}, error)
}

type AppsService interface {
	Install(ctx context.Context, id string) (string, error)
	Uninstall(ctx context.Context, id string) (string, error)
	Upgrade(ctx context.Context, id string) (string, error)
	UpgradeAll(ctx context.Context) (string, error)
}

type AgentConnectionStatus struct {
	Connected bool
	AgentID   string
	Server    string
	LastEvent string
	Transport string
}

type AgentRuntime interface {
	Run(ctx context.Context)
	Reload()
	GetStatus() AgentConnectionStatus
}

type P2PService interface {
	Run(ctx context.Context) error
}

type p2pDiscoverySnapshotAware interface {
	ApplyP2PDiscoverySnapshot(snapshot agentconn.P2PDiscoverySnapshot)
}

type databaseAwareService interface {
	SetDB(db *database.DB)
}

type ServiceManager struct {
	dataDir       string
	config        *SharedConfig
	updateTrigger chan bool
	db            *database.DB
	ipcServer     *IPCServer
	agentRuntime  AgentRuntime
	automationSvc AutomationService
	inventorySvc  InventoryService
	appsSvc       AppsService
	p2pSvc        P2PService
	actionTrigger chan struct{}
	mu            sync.RWMutex
	startTime     time.Time
	isRunning     bool
	errLogPath    string
}

// SharedConfig representa a configuração compartilhada entre service e UI
// Localizada em C:\ProgramData\Discovery\config.json
type SharedConfig struct {
	AgentID       string             `json:"agent_id"`
	ServerURL     string             `json:"server_url"`
	ApiScheme     string             `json:"api_scheme"` // "http" ou "https"
	ApiServer     string             `json:"api_server"` // hostname:port
	AuthToken     string             `json:"auth_token"` // Bearer token
	ClientID      string             `json:"client_id"`  // Identificador único da instalação
	SiteID        string             `json:"site_id"`
	P2PEnabled    bool               `json:"p2p_enabled"`
	InventorySync int                `json:"inventory_sync_interval_minutes"` // minutos
	LastSync      string             `json:"last_inventory_sync"`             // ISO 8601
	AgentUpdate   *selfupdate.Policy `json:"agentUpdate,omitempty"`
}

func (c *SharedConfig) IsProvisioned() bool {
	if c == nil {
		return false
	}
	serverURL := strings.TrimSpace(c.ServerURL)
	scheme := strings.TrimSpace(strings.ToLower(c.ApiScheme))
	server := strings.TrimSpace(c.ApiServer)
	token := strings.TrimSpace(c.AuthToken)
	agentID := strings.TrimSpace(c.AgentID)
	hasEndpoint := serverURL != "" || (scheme != "" && server != "")
	return hasEndpoint && token != "" && agentID != ""
}

// UnmarshalJSON suporta schema canônico (snake_case) e legado (camelCase)
// para manter compatibilidade com config escrita pelo instalador/UI.
func (c *SharedConfig) UnmarshalJSON(data []byte) error {
	type canonical SharedConfig
	var base canonical
	if err := json.Unmarshal(data, &base); err != nil {
		return err
	}

	type legacy struct {
		AgentID    string `json:"agentId"`
		ServerURL  string `json:"serverUrl"`
		ApiScheme  string `json:"apiScheme"`
		ApiServer  string `json:"apiServer"`
		AuthToken  string `json:"authToken"`
		APIKey     string `json:"apiKey"`
		ClientID   string `json:"clientId"`
		SiteID     string `json:"siteId"`
		P2PEnabled *bool  `json:"p2pEnabled"`
		P2P        *struct {
			Enabled *bool `json:"enabled"`
		} `json:"p2p"`
		InventorySyncIntervalMin int                `json:"inventorySyncIntervalMinutes"`
		InventoryIntervalMin     int                `json:"inventoryIntervalMinutes"`
		LastSync                 string             `json:"lastSync"`
		AgentUpdate              *selfupdate.Policy `json:"agentUpdate,omitempty"`
	}

	var old legacy
	if err := json.Unmarshal(data, &old); err != nil {
		return err
	}

	result := SharedConfig(base)
	if strings.TrimSpace(result.AgentID) == "" {
		result.AgentID = strings.TrimSpace(old.AgentID)
	}
	if strings.TrimSpace(result.ServerURL) == "" {
		result.ServerURL = strings.TrimSpace(old.ServerURL)
	}
	if strings.TrimSpace(result.ApiScheme) == "" {
		result.ApiScheme = strings.TrimSpace(old.ApiScheme)
	}
	if strings.TrimSpace(result.ApiServer) == "" {
		result.ApiServer = strings.TrimSpace(old.ApiServer)
	}
	if strings.TrimSpace(result.AuthToken) == "" {
		result.AuthToken = strings.TrimSpace(old.AuthToken)
	}
	if strings.TrimSpace(result.AuthToken) == "" {
		result.AuthToken = strings.TrimSpace(old.APIKey)
	}
	if strings.TrimSpace(result.ClientID) == "" {
		result.ClientID = strings.TrimSpace(old.ClientID)
	}
	if strings.TrimSpace(result.SiteID) == "" {
		result.SiteID = strings.TrimSpace(old.SiteID)
	}
	if !result.P2PEnabled && old.P2PEnabled != nil {
		result.P2PEnabled = *old.P2PEnabled
	}
	if !result.P2PEnabled && old.P2P != nil && old.P2P.Enabled != nil {
		result.P2PEnabled = *old.P2P.Enabled
	}
	if result.InventorySync <= 0 {
		result.InventorySync = old.InventorySyncIntervalMin
	}
	if result.InventorySync <= 0 {
		result.InventorySync = old.InventoryIntervalMin
	}
	if strings.TrimSpace(result.LastSync) == "" {
		result.LastSync = strings.TrimSpace(old.LastSync)
	}
	if result.AgentUpdate == nil {
		result.AgentUpdate = old.AgentUpdate
	}

	// Default seguro para evitar ticker inválido.
	if result.InventorySync <= 0 {
		result.InventorySync = 15
	}

	*c = result
	return nil
}

func mergeLegacyInstallerConfigData(baseData, overrideData []byte) ([]byte, error) {
	base := map[string]any{}
	if trimmed := strings.TrimSpace(string(baseData)); trimmed != "" {
		if err := json.Unmarshal(baseData, &base); err != nil {
			return nil, fmt.Errorf("config.json invalido: %w", err)
		}
	}

	override := map[string]any{}
	if err := json.Unmarshal(overrideData, &override); err != nil {
		return nil, fmt.Errorf("installer.json invalido: %w", err)
	}

	for _, key := range []string{
		"serverUrl",
		"apiKey",
		"autoProvisioning",
		"discoveryEnabled",
		"apiScheme",
		"apiServer",
		"authToken",
		"agentId",
		"clientId",
		"siteId",
		"natsServer",
		"natsWsServer",
		"agentUpdate",
		"p2p",
		"meshCentralInstalled",
	} {
		if value, ok := override[key]; ok {
			base[key] = value
		}
	}

	return json.MarshalIndent(base, "", "  ")
}

func (sm *ServiceManager) migrateLegacyInstallerConfig(configPath string) error {
	installerPath := filepath.Join(sm.dataDir, "installer.json")
	installerData, err := os.ReadFile(installerPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("não conseguiu ler %s: %w", installerPath, err)
	}

	var baseData []byte
	if currentData, err := os.ReadFile(configPath); err == nil {
		baseData = currentData
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("não conseguiu ler %s: %w", configPath, err)
	}

	mergedData, err := mergeLegacyInstallerConfigData(baseData, installerData)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(configPath, mergedData, 0o600); err != nil {
		return err
	}

	_ = os.Remove(installerPath)
	return nil
}

func (sm *ServiceManager) RequestSelfUpdateCheck(source string) bool {
	if err := sm.loadConfig(); err != nil {
		sm.logError("falha ao recarregar config antes do self-update: %v", err)
	}
	source = strings.TrimSpace(source)
	if source == "" {
		source = "manual"
	}
	// Comandos explícitos do servidor (via NATS/SignalR command handler) forçam instalação
	// mesmo que a versão no manifest seja igual ou inferior à atual.
	force := strings.HasPrefix(source, "command:")
	select {
	case sm.updateTrigger <- force:
		if force {
			sm.logInfo("SelfUpdate force-install solicitado: source=%s", source)
		} else {
			sm.logInfo("SelfUpdate check solicitado: source=%s", source)
		}
		return true
	default:
		sm.logInfo("SelfUpdate check ja pendente: source=%s", source)
		return false
	}
}

// NewServiceManager cria um novo gerenciador de serviço
func NewServiceManager(dataDir string) *ServiceManager {
	return &ServiceManager{
		dataDir:       dataDir,
		ipcServer:     NewIPCServer(dataDir),
		startTime:     time.Now(),
		errLogPath:    dataDir + "\\logs\\service-errors.log",
		actionTrigger: make(chan struct{}, 1),
		updateTrigger: make(chan bool, 1),
	}
}

// SetAutomationService registra o serviço de automação real
func (sm *ServiceManager) SetAutomationService(svc AutomationService) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.automationSvc = svc
}

// SetInventoryService registra o serviço de inventário real
func (sm *ServiceManager) SetInventoryService(svc InventoryService) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.inventorySvc = svc
}

// SetAgentRuntime registra o runtime de conexão remota usado pelo modo service.
func (sm *ServiceManager) SetAgentRuntime(runtime AgentRuntime) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.agentRuntime = runtime
}

// SetAppsService registra o serviço de apps/pacotes real.
func (sm *ServiceManager) SetAppsService(svc AppsService) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.appsSvc = svc
}

// SetP2PService registra o runtime P2P real usado pelo modo service.
func (sm *ServiceManager) SetP2PService(svc P2PService) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.p2pSvc = svc
}

// Start inicia o Windows Service e mantém rodando até receber shutdown signal
func (sm *ServiceManager) Start(ctx context.Context) error {
	sm.mu.Lock()
	if sm.isRunning {
		sm.mu.Unlock()
		return fmt.Errorf("service already running")
	}
	sm.isRunning = true
	sm.mu.Unlock()

	sm.logInfo("ServiceManager iniciando... dataDir=%s errLogPath=%s", sm.dataDir, sm.errLogPath)

	// 1. Carregar configuração de C:\ProgramData\Discovery\config.json
	sm.logInfo("Carregando config de %s\\config.json...", sm.dataDir)
	if err := sm.loadConfig(); err != nil {
		sm.logError("falha ao carregar config: %v", err)
		return err
	}
	sm.logInfo("Config carregada: agentId=%s server=%s apiScheme=%s apiServer=%s provisioning=%v p2p=%v",
		sm.config.AgentID, sm.config.ServerURL, sm.config.ApiScheme, sm.config.ApiServer,
		sm.config.IsProvisioned(), sm.config.P2PEnabled)

	// 2. Inicializar banco de dados
	sm.logInfo("Inicializando database em %s...", sm.dataDir)
	db, err := database.Open(sm.dataDir)
	if err != nil {
		sm.logError("falha ao inicializar database: %v", err)
		return err
	}
	sm.mu.Lock()
	sm.db = db
	sm.mu.Unlock()
	sm.logInfo("Database inicializada com sucesso")
	if aware, ok := sm.automationSvc.(databaseAwareService); ok {
		aware.SetDB(db)
		sm.logInfo("Database injetada no automationSvc")
	}
	if aware, ok := sm.inventorySvc.(databaseAwareService); ok {
		aware.SetDB(db)
		sm.logInfo("Database injetada no inventorySvc")
	}
	if aware, ok := sm.agentRuntime.(databaseAwareService); ok {
		aware.SetDB(db)
		sm.logInfo("Database injetada no agentRuntime")
	}
	if aware, ok := sm.p2pSvc.(databaseAwareService); ok {
		aware.SetDB(db)
		sm.logInfo("Database injetada no p2pSvc")
	}
	defer func() {
		sm.logInfo("Fechando database...")
		if closeErr := db.Close(); closeErr != nil {
			sm.logError("falha ao fechar database: %v", closeErr)
		} else {
			sm.logInfo("Database fechada com sucesso")
		}
	}()
	// 2.1 Watchdog removed.

	// 3. Iniciar servidor IPC (Named Pipes)
	sm.logInfo("Iniciando IPC server (Named Pipe)...")
	if err := sm.ipcServer.Start(sm); err != nil {
		sm.logError("falha ao iniciar IPC server: %v", err)
		return err
	}
	sm.logInfo("IPC server iniciado em \\\\?\\pipe\\Discovery_Service")

	if err := os.MkdirAll(filepath.Join(sm.dataDir, "updates"), 0o755); err != nil {
		sm.logError("falha ao criar diretorio de updates: %v", err)
		return err
	}
	sm.logInfo("Diretorio de updates criado: %s", filepath.Join(sm.dataDir, "updates"))

	// 4. Iniciar worker threads para automação, inventário, conexão remota, P2P
	sm.logInfo("Iniciando workers: automation, inventory, agentRuntime, actionQueue, p2p, selfUpdate...")
	go sm.automationWorker(ctx)
	go sm.inventoryWorker(ctx)
	go sm.agentConnectionWorker(ctx)
	go sm.actionQueueWorker(ctx)
	go sm.p2pWorker(ctx)
	go sm.selfUpdateWorker(ctx)
	sm.logInfo("Todos os workers iniciados, aguardando shutdown signal...")

	// 5. Aguardar contexto de cancelamento (shutdown)
	<-ctx.Done()

	sm.logInfo("Shutdown signal recebido, encerrando...")
	sm.ipcServer.Stop()
	sm.logInfo("IPC server parado")
	sm.mu.Lock()
	sm.isRunning = false
	sm.mu.Unlock()
	sm.logInfo("ServiceManager encerrado")

	return nil
}

// loadConfig lê a configuração de C:\ProgramData\Discovery\config.json
// e migra automaticamente installer.json legado quando necessário.
func (sm *ServiceManager) loadConfig() error {
	configPath := sm.dataDir + "\\config.json"
	if err := sm.migrateLegacyInstallerConfig(configPath); err != nil {
		return fmt.Errorf("falha ao migrar installer.json legado: %w", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("não conseguiu ler %s: %w", configPath, err)
	}

	// Strip UTF-8 BOM (EF BB BF) defensivamente.
	// O instalador NSIS escreve via PowerShell 5.1 que adiciona BOM no Set-Content -Encoding UTF8.
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})

	parsed := &SharedConfig{}
	if err := json.Unmarshal(data, parsed); err != nil {
		return fmt.Errorf("JSON inválido em %s: %w", configPath, err)
	}

	sm.mu.Lock()
	sm.config = parsed
	sm.mu.Unlock()

	sm.logInfo("Config carregada: agentId=%s, server=%s", parsed.AgentID, parsed.ServerURL)
	return nil
}

// ReloadConfig recarrega config.json sem reiniciar o service e reaplica o agent runtime.
func (sm *ServiceManager) ReloadConfig() error {
	if err := sm.loadConfig(); err != nil {
		return err
	}

	sm.mu.RLock()
	runtime := sm.agentRuntime
	sm.mu.RUnlock()
	if runtime != nil {
		runtime.Reload()
	}
	return nil
}

// RefreshAutomationPolicy força uma atualização imediata da política de automação.
func (sm *ServiceManager) RefreshAutomationPolicy(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	sm.mu.RLock()
	automationSvc := sm.automationSvc
	sm.mu.RUnlock()
	if automationSvc == nil {
		return fmt.Errorf("serviço de automação não registrado")
	}
	_, err := automationSvc.RefreshPolicy(ctx, false)
	return err
}

// GetConfig retorna a configuração compartilhada (thread-safe)
func (sm *ServiceManager) GetConfig() *SharedConfig {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.config
}

// GetServiceHealth retorna status do service.
func (sm *ServiceManager) GetServiceHealth() map[string]interface{} {
	return map[string]interface{}{
		"running":    sm.isServiceRunning(),
		"checked_at": time.Now().UTC().Format(time.RFC3339),
	}
}

func (sm *ServiceManager) isServiceRunning() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.isRunning
}

// GetStatus retorna status atual do serviço
func (sm *ServiceManager) GetStatus() map[string]interface{} {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	agentID := ""
	serverURL := ""
	p2pEnabled := false
	lastSync := ""
	agentStatus := AgentConnectionStatus{}
	if sm.config != nil {
		agentID = sm.config.AgentID
		serverURL = sm.config.ServerURL
		p2pEnabled = sm.config.P2PEnabled
		lastSync = sm.config.LastSync
	}
	if sm.agentRuntime != nil {
		agentStatus = sm.agentRuntime.GetStatus()
	}

	return map[string]interface{}{
		"running":          sm.isRunning,
		"uptime_seconds":   time.Since(sm.startTime).Seconds(),
		"agent_id":         agentID,
		"server_url":       serverURL,
		"p2p_enabled":      p2pEnabled,
		"last_sync":        lastSync,
		"agent_connected":  agentStatus.Connected,
		"agent_server":     agentStatus.Server,
		"agent_last_event": agentStatus.LastEvent,
		"agent_transport":  agentStatus.Transport,
	}
}

func (sm *ServiceManager) EnqueueAction(actionID, userSID, userName, command, payloadJSON string) error {
	sm.mu.RLock()
	db := sm.db
	trigger := sm.actionTrigger
	sm.mu.RUnlock()

	if db == nil {
		return fmt.Errorf("database indisponivel")
	}

	err := db.EnqueueAction(database.ActionQueueEntry{
		ActionID:    strings.TrimSpace(actionID),
		UserSID:     strings.TrimSpace(userSID),
		UserName:    strings.TrimSpace(userName),
		Command:     strings.TrimSpace(command),
		PayloadJSON: strings.TrimSpace(payloadJSON),
		Status:      "queued",
		QueuedAt:    time.Now(),
	})
	if err != nil {
		return err
	}

	select {
	case trigger <- struct{}{}:
	default:
	}

	return nil
}

func (sm *ServiceManager) GetActionStatus(actionID string) (map[string]interface{}, bool, error) {
	actionID = strings.TrimSpace(actionID)
	if actionID == "" {
		return nil, false, fmt.Errorf("action_id obrigatorio")
	}

	sm.mu.RLock()
	db := sm.db
	sm.mu.RUnlock()
	if db == nil {
		return nil, false, fmt.Errorf("database indisponivel")
	}

	entry, found, err := db.GetAction(actionID)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}

	return actionStatusMap(entry), true, nil
}

func (sm *ServiceManager) GetActionHistory(actionID string, limit int) (map[string]interface{}, bool, error) {
	actionID = strings.TrimSpace(actionID)
	if actionID == "" {
		return nil, false, fmt.Errorf("action_id obrigatorio")
	}

	sm.mu.RLock()
	db := sm.db
	sm.mu.RUnlock()
	if db == nil {
		return nil, false, fmt.Errorf("database indisponivel")
	}

	entry, found, err := db.GetAction(actionID)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}

	history, err := db.ListActionHistory(actionID, limit)
	if err != nil {
		return nil, false, err
	}

	items := make([]map[string]interface{}, 0, len(history))
	for _, item := range history {
		items = append(items, actionHistoryMap(item))
	}

	return map[string]interface{}{
		"action":  actionStatusMap(entry),
		"count":   len(items),
		"history": items,
	}, true, nil
}

func (sm *ServiceManager) GetPolicies() ([]map[string]interface{}, error) {
	sm.mu.RLock()
	db := sm.db
	cfg := sm.config
	automationSvc := sm.automationSvc
	sm.mu.RUnlock()

	// Prioridade 1: estado em memória (online) – retornado quando o serviço de
	// automação já possui uma política sincronizada nesta sessão.
	if automationSvc != nil {
		if tasks := automationSvc.GetCurrentTasks(); len(tasks) > 0 {
			policies := make([]map[string]interface{}, 0, len(tasks))
			for _, task := range tasks {
				policies = append(policies, taskToPolicy(task))
			}
			return policies, nil
		}
	}

	// Prioridade 2: snapshot persistido no banco de dados (fallback offline).
	if db == nil || cfg == nil {
		return []map[string]interface{}{}, nil
	}

	agentID := strings.TrimSpace(cfg.AgentID)
	if agentID == "" {
		return []map[string]interface{}{}, nil
	}

	payload, _, _, found, err := db.GetAutomationPolicy(agentID)
	if err != nil {
		return nil, err
	}
	if !found || len(payload) == 0 {
		return []map[string]interface{}{}, nil
	}

	var persisted automation.PersistedPolicy
	if err := json.Unmarshal(payload, &persisted); err != nil {
		return nil, err
	}

	policies := make([]map[string]interface{}, 0, len(persisted.Policy.Tasks))
	for _, task := range persisted.Policy.Tasks {
		policies = append(policies, taskToPolicy(task))
	}

	return policies, nil
}

// taskToPolicy converte uma AutomationTask para o mapa de resposta IPC.
func taskToPolicy(task automation.AutomationTask) map[string]interface{} {
	return map[string]interface{}{
		"task_id":                  strings.TrimSpace(task.TaskID),
		"name":                     strings.TrimSpace(task.Name),
		"description":              strings.TrimSpace(task.Description),
		"action_type":              strings.TrimSpace(string(task.ActionType)),
		"installation_type":        strings.TrimSpace(string(task.InstallationType)),
		"package_id":               strings.TrimSpace(task.PackageID),
		"script_id":                strings.TrimSpace(task.ScriptID),
		"requires_approval":        task.RequiresApproval,
		"trigger_immediate":        task.TriggerImmediate,
		"trigger_recurring":        task.TriggerRecurring,
		"trigger_on_user_login":    task.TriggerOnUserLogin,
		"trigger_on_agent_checkin": task.TriggerOnAgentCheckIn,
		"schedule_cron":            strings.TrimSpace(task.ScheduleCron),
		"last_updated_at":          strings.TrimSpace(task.LastUpdatedAt),
	}
}

type actionExecutionResult struct {
	status     string
	output     string
	resultJSON string
	exitCode   *int
	errMessage string
}

func (sm *ServiceManager) actionQueueWorker(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	sm.logInfo("ActionQueueWorker iniciado")

	for {
		select {
		case <-ctx.Done():
			sm.logInfo("ActionQueueWorker encerrando")
			return
		case <-sm.actionTrigger:
		case <-ticker.C:
		}

		for i := 0; i < 5; i++ {
			processed, err := sm.processNextQueuedAction(ctx)
			if err != nil {
				sm.logError("action queue worker: %v", err)
				break
			}
			if !processed {
				break
			}
		}
	}
}

func (sm *ServiceManager) agentConnectionWorker(ctx context.Context) {
	sm.mu.RLock()
	runtime := sm.agentRuntime
	sm.mu.RUnlock()
	if runtime == nil {
		sm.logInfo("AgentConnectionWorker ignorado: runtime nao registrado")
		return
	}

	sm.logInfo("AgentConnectionWorker iniciado")
	runtime.Run(ctx)
	sm.logInfo("AgentConnectionWorker encerrado")
}

func (sm *ServiceManager) processNextQueuedAction(ctx context.Context) (bool, error) {
	sm.mu.RLock()
	db := sm.db
	sm.mu.RUnlock()
	if db == nil {
		return false, fmt.Errorf("database indisponivel")
	}

	entry, found, err := db.ClaimNextQueuedAction(time.Now())
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}

	// Enriquecer o contexto com a identidade do usuário que originou a ação.
	actionCtx := withActionUserContext(ctx, entry)

	// Anexar token Windows de impersonação ao contexto, se disponível (Windows: real identity).
	// O token foi capturado via ImpersonateNamedPipeClient durante a conexão IPC.
	actionCtx = enrichContextWithToken(actionCtx, entry.ActionID)
	defer closeActionToken(actionCtx) // fechar handle após execução (evitar leak)

	// Executar ação no contexto de identidade do usuário (Windows: impersonation real).
	var result actionExecutionResult
	impErr := impersonateAndRun(actionCtx, func(ctx context.Context) error {
		var err error
		result, err = sm.executeQueuedAction(ctx, entry)
		return err
	})
	execErr := impErr
	if result.status == "" {
		if execErr != nil {
			result.status = "failed"
			result.errMessage = execErr.Error()
		} else {
			result.status = "completed"
		}
	}
	if execErr != nil && strings.TrimSpace(result.errMessage) == "" {
		result.errMessage = execErr.Error()
	}

	if err := db.CompleteAction(entry, result.status, result.exitCode, result.output, result.resultJSON, result.errMessage, time.Now()); err != nil {
		return true, err
	}

	if execErr != nil {
		sm.logError("action %s falhou: %v", entry.ActionID, execErr)
	} else {
		sm.logInfo("Action completed: id=%s, command=%s", entry.ActionID, entry.Command)
	}

	return true, nil
}

func (sm *ServiceManager) executeQueuedAction(ctx context.Context, entry database.ActionQueueEntry) (actionExecutionResult, error) {
	payload := map[string]interface{}{}
	if strings.TrimSpace(entry.PayloadJSON) != "" {
		if err := json.Unmarshal([]byte(entry.PayloadJSON), &payload); err != nil {
			return actionExecutionResult{status: "failed", errMessage: "payload_json inválido"}, err
		}
	}

	switch strings.ToLower(strings.TrimSpace(entry.Command)) {
	case "refresh_policy":
		sm.mu.RLock()
		automationSvc := sm.automationSvc
		sm.mu.RUnlock()
		if automationSvc == nil {
			return actionExecutionResult{status: "failed", errMessage: "serviço de automação não registrado"}, fmt.Errorf("serviço de automação não registrado")
		}
		state, err := automationSvc.RefreshPolicy(ctx, false)
		if err != nil {
			return actionExecutionResult{status: "failed", errMessage: err.Error()}, err
		}
		resultJSON, _ := json.Marshal(state)
		return actionExecutionResult{status: "completed", output: "policy atualizada", resultJSON: string(resultJSON)}, nil
	case "refresh_inventory":
		sm.mu.RLock()
		inventorySvc := sm.inventorySvc
		sm.mu.RUnlock()
		if inventorySvc == nil {
			return actionExecutionResult{status: "failed", errMessage: "serviço de inventário não registrado"}, fmt.Errorf("serviço de inventário não registrado")
		}
		inventory, err := inventorySvc.Collect(ctx)
		if err != nil {
			return actionExecutionResult{status: "failed", errMessage: err.Error()}, err
		}
		sm.setLastSync(time.Now().UTC().Format(time.RFC3339))
		resultJSON, _ := json.Marshal(inventory)
		return actionExecutionResult{status: "completed", output: "inventário coletado", resultJSON: string(resultJSON)}, nil
	case "install_package", "uninstall_package", "upgrade_package":
		sm.mu.RLock()
		appsSvc := sm.appsSvc
		sm.mu.RUnlock()
		if appsSvc == nil {
			return actionExecutionResult{status: "failed", errMessage: "serviço de apps não registrado"}, fmt.Errorf("serviço de apps não registrado")
		}
		packageID, err := packageIDFromPayload(payload)
		if err != nil {
			return actionExecutionResult{status: "failed", errMessage: err.Error()}, err
		}
		var output string
		switch strings.ToLower(strings.TrimSpace(entry.Command)) {
		case "install_package":
			output, err = appsSvc.Install(ctx, packageID)
		case "uninstall_package":
			output, err = appsSvc.Uninstall(ctx, packageID)
		case "upgrade_package":
			output, err = appsSvc.Upgrade(ctx, packageID)
		}
		if err != nil {
			return actionExecutionResult{status: "failed", output: output, errMessage: err.Error()}, err
		}
		resultJSON, _ := json.Marshal(map[string]interface{}{"package_id": packageID, "command": entry.Command})
		return actionExecutionResult{status: "completed", output: output, resultJSON: string(resultJSON)}, nil
	case "upgrade_all_packages":
		sm.mu.RLock()
		appsSvc := sm.appsSvc
		sm.mu.RUnlock()
		if appsSvc == nil {
			return actionExecutionResult{status: "failed", errMessage: "serviço de apps não registrado"}, fmt.Errorf("serviço de apps não registrado")
		}
		output, err := appsSvc.UpgradeAll(ctx)
		if err != nil {
			return actionExecutionResult{status: "failed", output: output, errMessage: err.Error()}, err
		}
		return actionExecutionResult{status: "completed", output: output, resultJSON: `{"command":"upgrade_all_packages"}`}, nil
	case "run_automation_check":
		// Executa sincronização completa de política incluindo conteúdo de scripts,
		// útil para verificações agendadas ou sob demanda pelo operador.
		sm.mu.RLock()
		automationSvc := sm.automationSvc
		sm.mu.RUnlock()
		if automationSvc == nil {
			return actionExecutionResult{status: "failed", errMessage: "serviço de automação não registrado"}, fmt.Errorf("serviço de automação não registrado")
		}
		state, err := automationSvc.RefreshPolicy(ctx, true)
		if err != nil {
			return actionExecutionResult{status: "failed", errMessage: err.Error()}, err
		}
		resultJSON, _ := json.Marshal(state)
		return actionExecutionResult{status: "completed", output: "verificação de automação concluída", resultJSON: string(resultJSON)}, nil
	case "collect_inventory":
		// Alias explícito para refresh_inventory; mantém compatibilidade com
		// chamadores que usam o nome mais descritivo.
		sm.mu.RLock()
		inventorySvc := sm.inventorySvc
		sm.mu.RUnlock()
		if inventorySvc == nil {
			return actionExecutionResult{status: "failed", errMessage: "serviço de inventário não registrado"}, fmt.Errorf("serviço de inventário não registrado")
		}
		inventory, err := inventorySvc.Collect(ctx)
		if err != nil {
			return actionExecutionResult{status: "failed", errMessage: err.Error()}, err
		}
		sm.setLastSync(time.Now().UTC().Format(time.RFC3339))
		resultJSON, _ := json.Marshal(inventory)
		return actionExecutionResult{status: "completed", output: "inventário coletado", resultJSON: string(resultJSON)}, nil
	case "reload_config":
		// Recarrega configuração do arquivo config.json em disco sem reiniciar o service.
		if err := sm.ReloadConfig(); err != nil {
			return actionExecutionResult{status: "failed", errMessage: err.Error()}, err
		}
		return actionExecutionResult{status: "completed", output: "configuração recarregada", resultJSON: `{"command":"reload_config"}`}, nil
	default:
		return actionExecutionResult{status: "failed", errMessage: "ação não suportada pelo worker atual"}, fmt.Errorf("ação não suportada pelo worker atual: %s", entry.Command)
	}
}

func packageIDFromPayload(payload map[string]interface{}) (string, error) {
	for _, key := range []string{"package", "package_id", "id"} {
		if raw, ok := payload[key]; ok {
			if value, ok := raw.(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value), nil
			}
		}
	}
	return "", fmt.Errorf("identificador do pacote obrigatório")
}

func actionStatusMap(entry database.ActionQueueEntry) map[string]interface{} {
	return map[string]interface{}{
		"action_id":     entry.ActionID,
		"user_sid":      entry.UserSID,
		"user_name":     entry.UserName,
		"command":       entry.Command,
		"status":        entry.Status,
		"queued_at":     formatServiceTime(entry.QueuedAt),
		"started_at":    formatServiceTime(entry.StartedAt),
		"completed_at":  formatServiceTime(entry.CompletedAt),
		"result_json":   strings.TrimSpace(entry.ResultJSON),
		"error_message": strings.TrimSpace(entry.ErrorMessage),
	}
}

func actionHistoryMap(entry database.ActionHistoryEntry) map[string]interface{} {
	data := map[string]interface{}{
		"id":            entry.ID,
		"action_id":     entry.ActionID,
		"user_sid":      entry.UserSID,
		"user_name":     entry.UserName,
		"command":       entry.Command,
		"status":        entry.Status,
		"output":        strings.TrimSpace(entry.Output),
		"error_message": strings.TrimSpace(entry.ErrorMessage),
		"completed_at":  formatServiceTime(entry.CompletedAt),
	}
	if entry.ExitCodeSet {
		data["exit_code"] = entry.ExitCode
	}
	return data
}

func formatServiceTime(value time.Time) interface{} {
	if value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339)
}

// automationWorker executa políticas de automação periodicamente
func (sm *ServiceManager) automationWorker(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	sm.logInfo("AutomationWorker iniciado")

	for {
		select {
		case <-ctx.Done():
			sm.logInfo("AutomationWorker encerrando")
			return
		case <-ticker.C:
			sm.mu.RLock()
			automationSvc := sm.automationSvc
			sm.mu.RUnlock()

			if automationSvc != nil {
				sm.logInfo("Atualizando políticas de automação...")
				_, err := automationSvc.RefreshPolicy(ctx, false)
				if err != nil {
					sm.logError("falha ao atualizar políticas de automação: %v", err)
				}
			} else {
				sm.logInfo("Serviço de automação não registrado")
			}
		}
	}
}

// inventoryWorker sincroniza inventário com servidor periodicamente
func (sm *ServiceManager) inventoryWorker(ctx context.Context) {
	// Intervalo configurável em sm.config.InventorySync (minutos)
	interval := time.Duration(sm.getInventorySyncMinutes()) * time.Minute
	if interval < 5*time.Minute {
		interval = 5 * time.Minute // Mínimo 5 minutos
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	sm.logInfo("InventoryWorker iniciado, intervalo: %v", interval)

	// Envio imediato no startup (não aguarda o primeiro tick do ticker)
	sm.runInventoryCycle(ctx)

	for {
		select {
		case <-ctx.Done():
			sm.logInfo("InventoryWorker encerrando")
			return
		case <-ticker.C:
			sm.runInventoryCycle(ctx)
		}
	}
}

func (sm *ServiceManager) runInventoryCycle(ctx context.Context) {
	if !sm.isInventoryProvisioned() {
		sm.logInfo("InventoryWorker: agente nao provisionado, ignorando ciclo")
		return
	}

	sm.mu.RLock()
	inventorySvc := sm.inventorySvc
	sm.mu.RUnlock()

	if inventorySvc != nil {
		sm.logInfo("Coletando inventário do sistema...")
		_, err := inventorySvc.Collect(ctx)
		if err != nil {
			sm.logError("falha ao coletar inventário: %v", err)
		} else {
			sm.setLastSync(time.Now().UTC().Format(time.RFC3339))
		}
	} else {
		sm.logInfo("Serviço de inventário não registrado")
	}
}

// p2pWorker mantém descoberta P2P ativa
func (sm *ServiceManager) p2pWorker(ctx context.Context) {
	if !sm.isP2PEnabled() {
		sm.logInfo("P2P desabilitado, pulando worker")
		return
	}

	sm.mu.RLock()
	p2pSvc := sm.p2pSvc
	sm.mu.RUnlock()
	if p2pSvc == nil {
		sm.logInfo("Serviço P2P não registrado")
		return
	}

	sm.logInfo("P2PWorker iniciado")
	for {
		err := p2pSvc.Run(ctx)
		if ctx.Err() != nil {
			sm.logInfo("P2PWorker encerrando por cancelamento de contexto")
			return
		}
		if err != nil {
			sm.logError("runtime P2P finalizado com erro: %v", err)
		} else {
			sm.logInfo("runtime P2P finalizado sem erro; reiniciando em 10s")
		}

		restartDelay := 10 * time.Second
		select {
		case <-ctx.Done():
			sm.logInfo("P2PWorker encerrando apos delay de restart")
			return
		case <-time.After(restartDelay):
		}
	}
}

func (sm *ServiceManager) selfUpdateWorker(ctx context.Context) {
	sm.mu.RLock()
	cfg := sm.config
	apiScheme := ""
	apiServer := ""
	if cfg != nil {
		apiScheme = strings.TrimSpace(cfg.ApiScheme)
		apiServer = strings.TrimSpace(cfg.ApiServer)
	}
	sm.mu.RUnlock()

	updater := &selfupdate.Updater{
		ApiScheme: apiScheme,
		ApiServer: apiServer,
		GetToken: func() string {
			sm.mu.RLock()
			defer sm.mu.RUnlock()
			if sm.config == nil {
				return ""
			}
			return sm.config.AuthToken
		},
		GetAgentID: func() string {
			sm.mu.RLock()
			defer sm.mu.RUnlock()
			if sm.config == nil {
				return ""
			}
			return sm.config.AgentID
		},
		GetPolicy: func() selfupdate.Policy {
			if err := sm.loadConfig(); err != nil {
				sm.logError("falha ao recarregar policy de self-update: %v", err)
			}
			sm.mu.RLock()
			defer sm.mu.RUnlock()
			if sm.config == nil || sm.config.AgentUpdate == nil {
				return selfupdate.DefaultPolicy()
			}
			return selfupdate.NormalizePolicy(*sm.config.AgentUpdate)
		},
		TempDir: filepath.Join(sm.dataDir, "updates"),
		Logf: func(format string, args ...any) {
			sm.logInfo("SelfUpdate: "+format, args...)
		},
		InvalidateCh: sm.updateTrigger,
	}

	updater.ResumePendingInstallReport(ctx)
	updater.Run(ctx, 12*time.Hour)
}

// logError registra erros em arquivo de log
func (sm *ServiceManager) logError(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	sm.writeLog("ERROR", msg)
}

// logInfo registra informacoes de debug no log do servico
func (sm *ServiceManager) logInfo(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	sm.writeLog("INFO", msg)
}

// writeLog escreve no stderr e no arquivo de log de erros
func (sm *ServiceManager) writeLog(level string, msg string) {
	fmt.Fprintf(os.Stderr, "[SERVICE.%s] %s\n", level, msg)

	if strings.TrimSpace(sm.errLogPath) == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(sm.errLogPath), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(sm.errLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	entry := time.Now().UTC().Format(time.RFC3339)
	_, _ = f.WriteString(entry + " [SERVICE." + level + "] " + msg + "\n")
}

func (sm *ServiceManager) getInventorySyncMinutes() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.config == nil || sm.config.InventorySync <= 0 {
		return 15
	}
	return sm.config.InventorySync
}

func (sm *ServiceManager) isInventoryProvisioned() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.config != nil && sm.config.IsProvisioned()
}

func (sm *ServiceManager) isP2PEnabled() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.config != nil && sm.config.P2PEnabled
}

func (sm *ServiceManager) setLastSync(lastSync string) {
	sm.mu.Lock()
	if sm.config != nil {
		sm.config.LastSync = strings.TrimSpace(lastSync)
	}
	sm.mu.Unlock()
	if err := sm.saveConfig(); err != nil {
		sm.logError("falha ao persistir last_sync: %v", err)
	}
}

func (sm *ServiceManager) saveConfig() error {
	sm.mu.RLock()
	if sm.config == nil {
		sm.mu.RUnlock()
		return fmt.Errorf("config nao carregada")
	}
	copyCfg := *sm.config
	sm.mu.RUnlock()

	data, err := json.MarshalIndent(copyCfg, "", "  ")
	if err != nil {
		return err
	}
	configPath := filepath.Join(sm.dataDir, "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		return err
	}
	return nil
}
