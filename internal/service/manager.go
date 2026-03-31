package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"discovery/internal/database"
	"discovery/internal/watchdog"
)

// ServiceManager gerencia o Windows Service (headless)
// Responsabilidades:
// - Manter o serviço rodando 24/7
// - Executar automação, P2P, inventory sincronização
// - Escutar conexões Named Pipe de UI apps
// - Coordenar múltiplos usuários logados
type AutomationService interface {
	RefreshPolicy(ctx context.Context, includeScriptContent bool) (interface{}, error)
}

type InventoryService interface {
	Collect(ctx context.Context) (interface{}, error)
}

type ServiceManager struct {
	dataDir       string
	config        *SharedConfig
	db            *database.DB
	ipcServer     *IPCServer
	watchdog      *watchdog.Watchdog
	automationSvc AutomationService
	inventorySvc  InventoryService
	policyMgr     interface{} // TODO: PolicyManager
	invMgr        interface{} // TODO: InventoryManager
	mu            sync.RWMutex
	startTime     time.Time
	isRunning     bool
	errLogPath    string
}

// SharedConfig representa a configuração compartilhada entre service e UI
// Localizada em C:\ProgramData\Discovery\config.json
type SharedConfig struct {
	AgentID       string `json:"agent_id"`
	ServerURL     string `json:"server_url"`
	ApiScheme     string `json:"api_scheme"` // "http" ou "https"
	ApiServer     string `json:"api_server"` // hostname:port
	AuthToken     string `json:"auth_token"` // Bearer token
	ClientID      string `json:"client_id"`  // Identificador único da instalação
	P2PEnabled    bool   `json:"p2p_enabled"`
	InventorySync int    `json:"inventory_sync_interval_minutes"` // minutos
	LastSync      string `json:"last_inventory_sync"`             // ISO 8601
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
		AgentID                  string `json:"agentId"`
		ServerURL                string `json:"serverUrl"`
		ApiScheme                string `json:"apiScheme"`
		ApiServer                string `json:"apiServer"`
		AuthToken                string `json:"authToken"`
		APIKey                   string `json:"apiKey"`
		ClientID                 string `json:"clientId"`
		P2PEnabled               *bool  `json:"p2pEnabled"`
		InventorySyncIntervalMin int    `json:"inventorySyncIntervalMinutes"`
		InventoryIntervalMin     int    `json:"inventoryIntervalMinutes"`
		LastSync                 string `json:"lastSync"`
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
	if !result.P2PEnabled && old.P2PEnabled != nil {
		result.P2PEnabled = *old.P2PEnabled
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

	// Default seguro para evitar ticker inválido.
	if result.InventorySync <= 0 {
		result.InventorySync = 15
	}

	*c = result
	return nil
}

// NewServiceManager cria um novo gerenciador de serviço
func NewServiceManager(dataDir string) *ServiceManager {
	wdCfg := watchdog.DefaultConfig()
	wdCfg.CheckInterval = 10 * time.Second
	wdCfg.DegradedThreshold = 45 * time.Second
	wdCfg.UnhealthyThreshold = 90 * time.Second

	return &ServiceManager{
		dataDir:    dataDir,
		ipcServer:  NewIPCServer(dataDir),
		watchdog:   watchdog.New(wdCfg),
		startTime:  time.Now(),
		errLogPath: dataDir + "\\logs\\service-errors.log",
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

// Start inicia o Windows Service e mantém rodando até receber shutdown signal
func (sm *ServiceManager) Start(ctx context.Context) error {
	sm.mu.Lock()
	if sm.isRunning {
		sm.mu.Unlock()
		return fmt.Errorf("service already running")
	}
	sm.isRunning = true
	sm.mu.Unlock()

	fmt.Println("[SERVICE.Manager] Iniciando...")

	// 1. Carregar configuração de C:\ProgramData\Discovery\config.json
	if err := sm.loadConfig(); err != nil {
		sm.logError("falha ao carregar config: %v", err)
		return err
	}

	// 2. Inicializar banco de dados
	db, err := database.Open(sm.dataDir)
	if err != nil {
		sm.logError("falha ao inicializar database: %v", err)
		return err
	}
	sm.mu.Lock()
	sm.db = db
	sm.mu.Unlock()
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			sm.logError("falha ao fechar database: %v", closeErr)
		}
	}()

	// 2.1 Inicializar watchdog do service
	if sm.watchdog != nil {
		sm.registerWatchdogRecovery()
		sm.watchdog.Start(ctx)
		sm.watchdog.Heartbeat(watchdog.ComponentAgent)
		sm.watchdog.Heartbeat(watchdog.ComponentAutomation)
		sm.watchdog.Heartbeat(watchdog.ComponentInventory)
	}

	// 3. Iniciar servidor IPC (Named Pipes)
	if err := sm.ipcServer.Start(sm); err != nil {
		sm.logError("falha ao iniciar IPC server: %v", err)
		return err
	}
	fmt.Println("[SERVICE.Manager] IPC server iniciado em \\\\?\\pipe\\Discovery_Service")

	// 4. Iniciar worker threads para automação, inventário, P2P
	go sm.automationWorker(ctx)
	go sm.inventoryWorker(ctx)
	go sm.p2pWorker(ctx)

	// 5. Aguardar contexto de cancelamento (shutdown)
	<-ctx.Done()

	fmt.Println("[SERVICE.Manager] Encerrando...")
	sm.ipcServer.Stop()
	if sm.watchdog != nil {
		sm.watchdog.Stop()
	}
	sm.mu.Lock()
	sm.isRunning = false
	sm.mu.Unlock()

	return nil
}

// loadConfig lê a configuração de C:\ProgramData\Discovery\config.json
func (sm *ServiceManager) loadConfig() error {
	configPath := sm.dataDir + "\\config.json"

	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("não conseguiu ler %s: %w", configPath, err)
	}

	sm.config = &SharedConfig{}
	if err := json.Unmarshal(data, sm.config); err != nil {
		return fmt.Errorf("JSON inválido em %s: %w", configPath, err)
	}

	fmt.Printf("[SERVICE.Manager] Config carregada: agentId=%s, server=%s\n",
		sm.config.AgentID, sm.config.ServerURL)
	return nil
}

// GetConfig retorna a configuração compartilhada (thread-safe)
func (sm *ServiceManager) GetConfig() *SharedConfig {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.config
}

// GetServiceHealth retorna status detalhado de saúde dos componentes do service.
func (sm *ServiceManager) GetServiceHealth() map[string]interface{} {
	checks := []watchdog.HealthCheck{}
	if sm.watchdog != nil {
		checks = sm.watchdog.GetHealth()
	}

	recoverable := 0
	unhealthy := 0
	degraded := 0
	for _, check := range checks {
		if check.Recoverable {
			recoverable++
		}
		switch check.Status {
		case watchdog.StatusUnhealthy:
			unhealthy++
		case watchdog.StatusDegraded:
			degraded++
		}
	}

	return map[string]interface{}{
		"running":               sm.isServiceRunning(),
		"checked_at":            time.Now().UTC().Format(time.RFC3339),
		"components":            checks,
		"component_count":       len(checks),
		"recoverable_count":     recoverable,
		"degraded_count":        degraded,
		"unhealthy_count":       unhealthy,
		"has_critical_unhealth": unhealthy > 0,
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
	if sm.config != nil {
		agentID = sm.config.AgentID
		serverURL = sm.config.ServerURL
		p2pEnabled = sm.config.P2PEnabled
		lastSync = sm.config.LastSync
	}

	return map[string]interface{}{
		"running":        sm.isRunning,
		"uptime_seconds": time.Since(sm.startTime).Seconds(),
		"agent_id":       agentID,
		"server_url":     serverURL,
		"p2p_enabled":    p2pEnabled,
		"last_sync":      lastSync,
	}
}

// automationWorker executa políticas de automação periodicamente
func (sm *ServiceManager) automationWorker(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	fmt.Println("[SERVICE.AutomationWorker] Iniciado")
	if sm.watchdog != nil {
		sm.watchdog.Heartbeat(watchdog.ComponentAutomation)
	}

	for {
		select {
		case <-ctx.Done():
			fmt.Println("[SERVICE.AutomationWorker] Encerrando")
			return
		case <-ticker.C:
			sm.mu.RLock()
			automationSvc := sm.automationSvc
			sm.mu.RUnlock()

			if automationSvc != nil {
				fmt.Println("[SERVICE.AutomationWorker] Atualizando políticas de automação...")
				_, err := automationSvc.RefreshPolicy(ctx, false)
				if err != nil {
					sm.logError("falha ao atualizar políticas de automação: %v", err)
				}
			} else {
				fmt.Println("[SERVICE.AutomationWorker] Serviço de automação não registrado")
			}

			if sm.watchdog != nil {
				sm.watchdog.Heartbeat(watchdog.ComponentAutomation)
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

	fmt.Println("[SERVICE.InventoryWorker] Iniciado, intervalo:", interval)
	if sm.watchdog != nil {
		sm.watchdog.Heartbeat(watchdog.ComponentInventory)
	}

	for {
		select {
		case <-ctx.Done():
			fmt.Println("[SERVICE.InventoryWorker] Encerrando")
			return
		case <-ticker.C:
			sm.mu.RLock()
			inventorySvc := sm.inventorySvc
			sm.mu.RUnlock()

			if inventorySvc != nil {
				fmt.Println("[SERVICE.InventoryWorker] Coletando inventário do sistema...")
				_, err := inventorySvc.Collect(ctx)
				if err != nil {
					sm.logError("falha ao coletar inventário: %v", err)
				}
			} else {
				fmt.Println("[SERVICE.InventoryWorker] Serviço de inventário não registrado")
			}

			sm.setLastSync(time.Now().UTC().Format(time.RFC3339))
			if sm.watchdog != nil {
				sm.watchdog.Heartbeat(watchdog.ComponentInventory)
			}
		}
	}
}

// p2pWorker mantém descoberta P2P ativa
func (sm *ServiceManager) p2pWorker(ctx context.Context) {
	if !sm.isP2PEnabled() {
		fmt.Println("[SERVICE.P2PWorker] P2P desabilitado, pulando")
		return
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	fmt.Println("[SERVICE.P2PWorker] Iniciado")
	if sm.watchdog != nil {
		sm.watchdog.Heartbeat(watchdog.ComponentAgent)
	}

	for {
		select {
		case <-ctx.Done():
			fmt.Println("[SERVICE.P2PWorker] Encerrando")
			return
		case <-ticker.C:
			// TODO: sm.p2pManager.DiscoverPeers()
			fmt.Println("[SERVICE.P2PWorker] Descobrindo peers P2P...")
			if sm.watchdog != nil {
				sm.watchdog.Heartbeat(watchdog.ComponentAgent)
			}
		}
	}
}

// logError registra erros em arquivo de log
func (sm *ServiceManager) logError(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(os.Stderr, "[SERVICE.ERROR] %s\n", msg)

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
	_, _ = f.WriteString(time.Now().UTC().Format(time.RFC3339) + " [SERVICE.ERROR] " + msg + "\n")
}

func (sm *ServiceManager) registerWatchdogRecovery() {
	if sm.watchdog == nil {
		return
	}
	sm.watchdog.RegisterRecovery(watchdog.ComponentAutomation, func(component watchdog.Component) error {
		sm.logError("watchdog recovery acionado para %s", component)
		return nil
	})
	sm.watchdog.RegisterRecovery(watchdog.ComponentInventory, func(component watchdog.Component) error {
		sm.logError("watchdog recovery acionado para %s", component)
		return nil
	})
	sm.watchdog.RegisterRecovery(watchdog.ComponentAgent, func(component watchdog.Component) error {
		sm.logError("watchdog recovery acionado para %s", component)
		return nil
	})
	sm.watchdog.OnUnhealthy(func(check watchdog.HealthCheck) {
		sm.logError("watchdog unhealthy: component=%s status=%s message=%s", check.Component, check.Status, check.Message)
	})
}

func (sm *ServiceManager) getInventorySyncMinutes() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.config == nil || sm.config.InventorySync <= 0 {
		return 15
	}
	return sm.config.InventorySync
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
