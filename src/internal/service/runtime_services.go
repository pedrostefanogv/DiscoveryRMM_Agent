package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	appdebug "discovery/app/debug"
	appinventory "discovery/app/inventory"
	"discovery/internal/agentconn"
	"discovery/internal/automation"
	"discovery/internal/database"
	"discovery/internal/inventory"
	"discovery/internal/models"
	"discovery/internal/services"
	"discovery/internal/winget"
)

type automationRuntimeService struct {
	svc *automation.Service
}

type AgentRuntimeHooks struct {
	ReloadConfig            func() error
	RefreshAutomationPolicy func(context.Context) error
	RequestSelfUpdateCheck  func(string) bool
}

type commandResultOutboxPayload struct {
	CommandID    string `json:"commandId"`
	ExitCode     int    `json:"exitCode"`
	Output       string `json:"output,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

func NewAutomationRuntimeService(loadConfig func() *SharedConfig, logger func(string)) AutomationService {
	service := automation.NewService(func() automation.RuntimeConfig {
		cfg := loadConfig()
		if cfg == nil {
			return automation.RuntimeConfig{}
		}

		baseURL := strings.TrimSpace(cfg.ServerURL)
		if scheme := strings.TrimSpace(cfg.ApiScheme); scheme != "" {
			if server := strings.TrimSpace(cfg.ApiServer); server != "" {
				baseURL = scheme + "://" + server
			}
		}

		return automation.RuntimeConfig{
			BaseURL: baseURL,
			Token:   strings.TrimSpace(cfg.AuthToken),
			AgentID: strings.TrimSpace(cfg.AgentID),
		}
	}, logger)

	return &automationRuntimeService{svc: service}
}

func (s *automationRuntimeService) RefreshPolicy(ctx context.Context, includeScriptContent bool) (interface{}, error) {
	return s.svc.RefreshPolicy(ctx, includeScriptContent)
}

func (s *automationRuntimeService) GetCurrentTasks() []automation.AutomationTask {
	state := s.svc.GetState()
	if !state.Available || len(state.Tasks) == 0 {
		return nil
	}
	return state.Tasks
}

func (s *automationRuntimeService) SetDB(db *database.DB) {
	if s == nil || s.svc == nil {
		return
	}
	s.svc.SetDB(db)
}

type agentRuntimeService struct {
	runtime    *agentconn.Runtime
	loadConfig func() *SharedConfig
	db         *database.DB
	logf       func(string)
	hooks      AgentRuntimeHooks
}

func NewAgentRuntimeService(loadConfig func() *SharedConfig, logf func(string), hooks AgentRuntimeHooks) AgentRuntime {
	if logf == nil {
		logf = func(string) {}
	}
	s := &agentRuntimeService{
		loadConfig: loadConfig,
		logf:       logf,
		hooks:      hooks,
	}
	s.runtime = agentconn.NewRuntime(agentconn.Options{
		LoadConfig: s.runtimeConfig,
		Logf: func(format string, args ...any) {
			logf(fmt.Sprintf(format, args...))
		},
		OnSyncPing: s.handleSyncPing,
		HandleCommand: func(parent context.Context, cmdType string, payload any) (bool, int, string, string) {
			return s.handleCommand(parent, cmdType, payload)
		},
		OnCommandOutput: s.onCommandOutput,
		EnqueueCommandResultOutbox: func(transport, commandID string, exitCode int, output, errText, sendError string) error {
			return s.enqueueCommandResultOutbox(transport, commandID, exitCode, output, errText, sendError)
		},
		ListDueCommandResultOutbox: func(transport string, now time.Time, limit int) ([]agentconn.CommandResultOutboxItem, error) {
			return s.listDueCommandResultOutbox(transport, now, limit)
		},
		MarkSentCommandResultOutbox:   s.markSentCommandResultOutbox,
		RescheduleCommandResultOutbox: s.rescheduleCommandResultOutbox,
	})
	return s
}

func (s *agentRuntimeService) Run(ctx context.Context) {
	if s == nil || s.runtime == nil {
		return
	}
	s.runtime.Run(ctx)
}

func (s *agentRuntimeService) Reload() {
	if s == nil || s.runtime == nil {
		return
	}
	s.runtime.Reload()
}

func (s *agentRuntimeService) GetStatus() AgentConnectionStatus {
	if s == nil || s.runtime == nil {
		return AgentConnectionStatus{}
	}
	status := s.runtime.GetStatus()
	return AgentConnectionStatus{
		Connected: status.Connected,
		AgentID:   status.AgentID,
		Server:    status.Server,
		LastEvent: status.LastEvent,
		Transport: status.Transport,
	}
}

func (s *agentRuntimeService) SetDB(db *database.DB) {
	if s == nil {
		return
	}
	s.db = db
}

func (s *agentRuntimeService) runtimeConfig() agentconn.Config {
	return sharedConfigToAgentConnConfig(s.loadConfig)
}

func (s *agentRuntimeService) handleSyncPing(ping agentconn.SyncPing) {
	resource := strings.ToLower(strings.TrimSpace(ping.Resource))
	if resource == "" {
		return
	}
	s.logf(fmt.Sprintf("[agent-sync] ping recebido: resource=%s revision=%s", resource, strings.TrimSpace(ping.Revision)))

	switch resource {
	case "automationpolicy":
		if s.hooks.RefreshAutomationPolicy == nil {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if err := s.hooks.RefreshAutomationPolicy(ctx); err != nil {
				s.logf("[agent-sync] falha ao atualizar automationpolicy: " + err.Error())
			}
		}()
	case "configuration":
		if s.hooks.ReloadConfig == nil {
			return
		}
		go func() {
			if err := s.hooks.ReloadConfig(); err != nil {
				s.logf("[agent-sync] falha ao recarregar config: " + err.Error())
			}
		}()
	case "agentupdate":
		if s.hooks.RequestSelfUpdateCheck == nil {
			return
		}
		queued := s.hooks.RequestSelfUpdateCheck("sync:" + strings.TrimSpace(ping.Reason))
		if queued {
			s.logf("[agent-sync] self-update solicitado via ping")
		}
	default:
		return
	}
}

func (s *agentRuntimeService) handleCommand(_ context.Context, cmdType string, payload any) (bool, int, string, string) {
	if isServiceUIOnlyCommandType(cmdType) {
		return true, 2, "", "comando indisponivel no modo service sem interface interativa"
	}
	if isAgentUpdateCommandType(cmdType) {
		action, err := parseAgentUpdatePayload(payload)
		if err != nil {
			return true, 2, "", err.Error()
		}
		if s.hooks.RequestSelfUpdateCheck == nil {
			return true, 1, "", "self-update indisponivel no service"
		}
		queued := s.hooks.RequestSelfUpdateCheck("command:" + action)
		if queued {
			return true, 0, "self-update check solicitado", ""
		}
		return true, 0, "self-update check ja pendente", ""
	}
	return false, 0, "", ""
}

func (s *agentRuntimeService) onCommandOutput(cmdType, output, errText string) {
	parts := []string{"[agent-cmd] type=" + strings.TrimSpace(cmdType)}
	if strings.TrimSpace(output) != "" {
		parts = append(parts, "output="+strings.TrimSpace(output))
	}
	if strings.TrimSpace(errText) != "" {
		parts = append(parts, "error="+strings.TrimSpace(errText))
	}
	s.logf(strings.Join(parts, " "))
}

func (s *agentRuntimeService) enqueueCommandResultOutbox(transport, commandID string, exitCode int, output, errText, sendError string) error {
	if s == nil || s.db == nil {
		return nil
	}
	config := s.runtimeConfig()
	agentID := strings.TrimSpace(config.AgentID)
	if agentID == "" {
		return nil
	}
	payload := commandResultOutboxPayload{
		CommandID:    strings.TrimSpace(commandID),
		ExitCode:     exitCode,
		Output:       output,
		ErrorMessage: errText,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	payloadHash := sha256.Sum256(payloadJSON)
	return s.db.EnqueueCommandResultOutbox(database.CommandResultOutboxEntry{
		AgentID:        agentID,
		Transport:      strings.TrimSpace(transport),
		CommandID:      payload.CommandID,
		IdempotencyKey: strings.TrimSpace(transport) + ":" + payload.CommandID,
		PayloadJSON:    string(payloadJSON),
		PayloadHash:    hex.EncodeToString(payloadHash[:]),
		Attempts:       0,
		NextAttemptAt:  time.Now(),
		LastError:      strings.TrimSpace(sendError),
		ExpiresAt:      time.Now().Add(14 * 24 * time.Hour),
	})
}

func (s *agentRuntimeService) listDueCommandResultOutbox(transport string, now time.Time, limit int) ([]agentconn.CommandResultOutboxItem, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	config := s.runtimeConfig()
	agentID := strings.TrimSpace(config.AgentID)
	if agentID == "" {
		return nil, nil
	}
	entries, err := s.db.ListDueCommandResultOutbox(agentID, transport, now, limit)
	if err != nil {
		return nil, err
	}
	out := make([]agentconn.CommandResultOutboxItem, 0, len(entries))
	for _, entry := range entries {
		var payload commandResultOutboxPayload
		if err := json.Unmarshal([]byte(entry.PayloadJSON), &payload); err != nil {
			s.logf(fmt.Sprintf("[agent-outbox] payload invalido removido id=%d erro=%v", entry.ID, err))
			_ = s.db.DeleteCommandResultOutbox(entry.ID)
			continue
		}
		out = append(out, agentconn.CommandResultOutboxItem{
			ID:           entry.ID,
			CommandID:    strings.TrimSpace(payload.CommandID),
			ExitCode:     payload.ExitCode,
			Output:       payload.Output,
			ErrorMessage: payload.ErrorMessage,
			Attempts:     entry.Attempts,
		})
	}
	return out, nil
}

func (s *agentRuntimeService) markSentCommandResultOutbox(id int64) error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.MarkSentCommandResultOutbox(id)
}

func (s *agentRuntimeService) rescheduleCommandResultOutbox(id int64, attempts int, nextAttemptAt time.Time, lastError string) error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.RescheduleCommandResultOutbox(id, attempts, nextAttemptAt, strings.TrimSpace(lastError))
}

type inventoryRuntimeService struct {
	provider   *inventory.Provider
	loadConfig func() *SharedConfig
	collect    func(context.Context) (models.InventoryReport, error)
	db         *database.DB
	logf       func(string)
	version    string
}

func NewInventoryRuntimeService(timeout time.Duration, loadConfig func() *SharedConfig, logf func(string), version string) InventoryService {
	if logf == nil {
		logf = func(string) {}
	}
	return &inventoryRuntimeService{
		provider:   inventory.NewProvider(timeout),
		loadConfig: loadConfig,
		logf:       logf,
		version:    strings.TrimSpace(version),
	}
}

func (s *inventoryRuntimeService) Collect(ctx context.Context) (interface{}, error) {
	if s != nil && s.loadConfig != nil {
		cfg := s.loadConfig()
		if cfg == nil || !cfg.IsProvisioned() {
			return nil, fmt.Errorf("inventario indisponivel enquanto o agente nao estiver provisionado")
		}
	}

	var (
		report models.InventoryReport
		err    error
	)
	if s.collect != nil {
		report, err = s.collect(ctx)
	} else if s.provider != nil {
		report, err = s.provider.Collect(ctx)
	} else {
		return nil, fmt.Errorf("inventory provider nao configurado")
	}
	if err != nil {
		return nil, err
	}

	s.syncCollectedInventory(ctx, report)
	return report, nil
}

func (s *inventoryRuntimeService) SetDB(db *database.DB) {
	if s == nil {
		return
	}
	s.db = db
}

func (s *inventoryRuntimeService) syncCollectedInventory(ctx context.Context, report models.InventoryReport) {
	syncSvc := appinventory.NewService(appinventory.Options{
		DB: s.db,
		DebugConfig: func() appdebug.Config {
			return sharedConfigToDebugConfig(s.loadConfig)
		},
		Logf:    s.logf,
		Version: s.version,
	})
	syncSvc.SyncInventoryOnStartup(ctx, report)
}

func sharedConfigToDebugConfig(loadConfig func() *SharedConfig) appdebug.Config {
	if loadConfig == nil {
		return appdebug.Config{}
	}

	cfg := loadConfig()
	if cfg == nil {
		return appdebug.Config{}
	}

	return appdebug.Config{
		ApiScheme: strings.TrimSpace(strings.ToLower(cfg.ApiScheme)),
		ApiServer: strings.TrimSpace(cfg.ApiServer),
		AuthToken: strings.TrimSpace(cfg.AuthToken),
		AgentID:   strings.TrimSpace(cfg.AgentID),
	}
}

func sharedConfigToAgentConnConfig(loadConfig func() *SharedConfig) agentconn.Config {
	if loadConfig == nil {
		return agentconn.Config{}
	}

	cfg := loadConfig()
	if cfg == nil {
		return agentconn.Config{}
	}

	apiScheme := strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
	apiServer := strings.TrimSpace(cfg.ApiServer)
	if baseURL := strings.TrimSpace(cfg.ServerURL); baseURL != "" && (apiScheme == "" || apiServer == "") {
		if parsed, err := url.Parse(baseURL); err == nil {
			if apiScheme == "" {
				apiScheme = strings.TrimSpace(strings.ToLower(parsed.Scheme))
			}
			if apiServer == "" {
				apiServer = strings.TrimSpace(parsed.Host)
			}
		}
	}

	return agentconn.Config{
		ApiScheme: apiScheme,
		ApiServer: apiServer,
		AuthToken: strings.TrimSpace(cfg.AuthToken),
		AgentID:   strings.TrimSpace(cfg.AgentID),
		ClientID:  strings.TrimSpace(cfg.ClientID),
	}
}

func isAgentUpdateCommandType(cmdType string) bool {
	switch strings.ToLower(strings.TrimSpace(cmdType)) {
	case "10", "update", "agentupdate", "selfupdate", "self-update":
		return true
	default:
		return false
	}
}

func isServiceUIOnlyCommandType(cmdType string) bool {
	switch strings.ToLower(strings.TrimSpace(cmdType)) {
	case "8", "remotedebug", "remote-debug", "notification", "notify", "notification_dispatch", "notification-dispatch":
		return true
	default:
		return false
	}
}

func parseAgentUpdatePayload(payload any) (string, error) {
	if payload == nil {
		return "check-update", nil
	}
	switch typed := payload.(type) {
	case string:
		action := strings.ToLower(strings.TrimSpace(typed))
		if action == "" {
			return "check-update", nil
		}
		return action, nil
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return "", fmt.Errorf("falha ao serializar payload de update: %w", err)
		}
		var parsed struct {
			Action string `json:"action"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return "", fmt.Errorf("payload de update invalido: %w", err)
		}
		if strings.TrimSpace(parsed.Action) == "" {
			return "check-update", nil
		}
		return strings.ToLower(strings.TrimSpace(parsed.Action)), nil
	}
}

type appsRuntimeService struct {
	svc *services.AppsService
}

func NewAppsRuntimeService(timeout time.Duration) AppsService {
	return &appsRuntimeService{svc: services.NewAppsService(winget.NewClient(timeout))}
}

func (s *appsRuntimeService) Install(ctx context.Context, id string) (string, error) {
	return s.svc.Install(ctx, id)
}

func (s *appsRuntimeService) Uninstall(ctx context.Context, id string) (string, error) {
	return s.svc.Uninstall(ctx, id)
}

func (s *appsRuntimeService) Upgrade(ctx context.Context, id string) (string, error) {
	return s.svc.Upgrade(ctx, id)
}

func (s *appsRuntimeService) UpgradeAll(ctx context.Context) (string, error) {
	return s.svc.UpgradeAll(ctx)
}
