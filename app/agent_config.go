package app

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"time"

	"discovery/internal/database"
)

// AgentAutoUpdateConfig represents the agent-side auto-update policy.
type AgentAutoUpdateConfig struct {
	Enabled               bool     `json:"enabled"`
	CheckEveryHours       int      `json:"checkEveryHours"`
	AllowUserDelay        bool     `json:"allowUserDelay"`
	MaxDelayHours         int      `json:"maxDelayHours"`
	ForceRestartDelay     bool     `json:"forceRestartDelay"`
	RestartDelayHours     int      `json:"restartDelayHours"`
	UpdateOnLogon         bool     `json:"updateOnLogon"`
	MaintenanceWindows    []string `json:"maintenanceWindows"`
	SilentInstall         bool     `json:"silentInstall"`
	AutoRollbackOnFailure bool     `json:"autoRollbackOnFailure"`
}

// AgentPSADTConfig defines PSAppDeployToolkit integration settings from the API.
type AgentPSADTConfig struct {
	Enabled                 *bool  `json:"enabled"`
	RequiredVersion         string `json:"requiredVersion"`
	AutoInstallModule       *bool  `json:"autoInstallModule"`
	InstallSource           string `json:"installSource"`
	ExecutionTimeoutSeconds *int   `json:"executionTimeoutSeconds"`
	FallbackPolicy          string `json:"fallbackPolicy"`
	InstallOnStartup        *bool  `json:"installOnStartup"`
	InstallOnDemand         *bool  `json:"installOnDemand"`
	SuccessExitCodes        []int  `json:"successExitCodes"`
	RebootExitCodes         []int  `json:"rebootExitCodes"`
	IgnoreExitCodes         []int  `json:"ignoreExitCodes"`
	TimeoutAction           string `json:"timeoutAction"`
	UnknownExitCodePolicy   string `json:"unknownExitCodePolicy"`
}

// NotificationThemeConfig defines base colors used by notification UI.
type NotificationThemeConfig struct {
	Surface string `json:"surface"`
	Text    string `json:"text"`
	Accent  string `json:"accent"`
	Success string `json:"success"`
	Warning string `json:"warning"`
	Danger  string `json:"danger"`
}

// AgentNotificationBrandingConfig defines tenant-level notification branding.
type AgentNotificationBrandingConfig struct {
	CompanyName string                  `json:"companyName"`
	LogoURL     string                  `json:"logoUrl"`
	BannerURL   string                  `json:"bannerUrl"`
	Theme       NotificationThemeConfig `json:"theme"`
}

// AgentNotificationStyleOverride defines per-event visual customizations.
type AgentNotificationStyleOverride struct {
	Layout     string `json:"layout"`
	Background string `json:"background"`
	Text       string `json:"text"`
}

// AgentNotificationAction defines actions available in an interactive notification.
type AgentNotificationAction struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	ActionType string `json:"actionType"`
}

// AgentNotificationPolicy defines behavior and style for a notification event type.
type AgentNotificationPolicy struct {
	EventType      string                         `json:"eventType"`
	Mode           string                         `json:"mode"`
	Severity       string                         `json:"severity"`
	TimeoutSeconds *int                           `json:"timeoutSeconds"`
	StyleOverride  AgentNotificationStyleOverride `json:"styleOverride"`
	Actions        []AgentNotificationAction      `json:"actions"`
}

// AgentRolloutConfig defines kill switches and phased rollout gates.
type AgentRolloutConfig struct {
	EnableNotifications           *bool    `json:"enableNotifications"`
	EnableRequireConfirmation     *bool    `json:"enableRequireConfirmation"`
	EnablePSADTBootstrap          *bool    `json:"enablePsadtBootstrap"`
	AllowedNotificationEventTypes []string `json:"allowedNotificationEventTypes"`
	BlockedNotificationEventTypes []string `json:"blockedNotificationEventTypes"`
}

// AgentConfiguration defines the configuration schema returned by /api/agent-auth/me/configuration.
// It is used to control what features should be enabled on the agent.
type AgentConfiguration struct {
	RecoveryEnabled               *bool                           `json:"recoveryEnabled"`
	DiscoveryEnabled              *bool                           `json:"discoveryEnabled"`
	P2PFilesEnabled               *bool                           `json:"p2pFilesEnabled"`
	SupportEnabled                *bool                           `json:"supportEnabled"`
	NatsServerHost                string                          `json:"natsServerHost"`
	NatsUseWssExternal            *bool                           `json:"natsUseWssExternal"`
	EnforceTlsHashValidation      *bool                           `json:"enforceTlsHashValidation"`
	HandshakeEnabled              *bool                           `json:"handshakeEnabled"`
	ApiTlsCertHash                string                          `json:"apiTlsCertHash"`
	NatsTlsCertHash               string                          `json:"natsTlsCertHash"`
	MeshCentralEnabledEffective   *bool                           `json:"meshCentralEnabledEffective"`
	MeshCentralGroupPolicyProfile string                          `json:"meshCentralGroupPolicyProfile"`
	ChatAIEnabled                 *bool                           `json:"chatAIEnabled"`
	KnowledgeBaseEnabled          *bool                           `json:"knowledgeBaseEnabled"`
	AppStoreEnabled               *bool                           `json:"appStoreEnabled"`
	InventoryIntervalHours        *int                            `json:"inventoryIntervalHours"`
	AgentHeartbeatIntervalSeconds *int                            `json:"agentHeartbeatIntervalSeconds"`
	SiteID                        string                          `json:"siteId"`
	ClientID                      string                          `json:"clientId"`
	ResolvedAt                    string                          `json:"resolvedAt"`
	AutoUpdate                    AgentAutoUpdateConfig           `json:"autoUpdate"`
	PSADT                         AgentPSADTConfig                `json:"psadt"`
	NotificationBranding          AgentNotificationBrandingConfig `json:"notificationBranding"`
	NotificationPolicies          []AgentNotificationPolicy       `json:"notificationPolicies"`
	Rollout                       AgentRolloutConfig              `json:"rollout"`
}

// parseAgentConfiguration parses a configuration blob into a normalized AgentConfiguration.
func parseAgentConfiguration(data []byte) (AgentConfiguration, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return AgentConfiguration{}, fmt.Errorf("falha ao decodificar configuracao do agent: %w", err)
	}

	// Helpers
	getAny := func(keys ...string) any {
		for _, k := range keys {
			if v, ok := raw[k]; ok {
				return v
			}
		}
		return nil
	}
	getBoolPtr := func(keys ...string) *bool {
		v := getAny(keys...)
		if v == nil {
			return nil
		}
		b := toBool(v)
		return &b
	}
	getIntPtr := func(keys ...string) *int {
		v := getAny(keys...)
		if v == nil {
			return nil
		}
		i := toInt(v)
		return &i
	}
	getString := func(keys ...string) string {
		v := getAny(keys...)
		if v == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(v))
	}

	cfg := AgentConfiguration{
		RecoveryEnabled:               getBoolPtr("recoveryEnabled"),
		DiscoveryEnabled:              getBoolPtr("discoveryEnabled"),
		P2PFilesEnabled:               getBoolPtr("p2pFilesEnabled"),
		SupportEnabled:                getBoolPtr("supportEnabled"),
		NatsServerHost:                getString("natsServerHost"),
		NatsUseWssExternal:            getBoolPtr("natsUseWssExternal"),
		EnforceTlsHashValidation:      getBoolPtr("enforceTlsHashValidation"),
		HandshakeEnabled:              getBoolPtr("handshakeEnabled"),
		ApiTlsCertHash:                strings.ToUpper(getString("apiTlsCertHash")),
		NatsTlsCertHash:               strings.ToUpper(getString("natsTlsCertHash")),
		MeshCentralEnabledEffective:   getBoolPtr("meshCentralEnabledEffective"),
		MeshCentralGroupPolicyProfile: getString("meshCentralGroupPolicyProfile"),
		ChatAIEnabled:                 getBoolPtr("chatAIEnabled"),
		KnowledgeBaseEnabled:          getBoolPtr("knowledgeBaseEnabled"),
		AppStoreEnabled:               getBoolPtr("appStoreEnabled"),
		InventoryIntervalHours:        getIntPtr("inventoryIntervalHours"),
		AgentHeartbeatIntervalSeconds: getIntPtr("agentHeartbeatIntervalSeconds"),
		SiteID:                        getString("siteId"),
		ClientID:                      getString("clientId"),
		ResolvedAt:                    getString("resolvedAt"),
	}

	// Parse nested autoUpdate object if present.
	if auRaw, ok := raw["autoUpdate"]; ok {
		if auMap, ok := auRaw.(map[string]any); ok {
			cfg.AutoUpdate.Enabled = getBoolFromMap(auMap, "enabled", "isEnabled")
			cfg.AutoUpdate.CheckEveryHours = getIntFromMap(auMap, "checkEveryHours", "checkEvery")
			cfg.AutoUpdate.AllowUserDelay = getBoolFromMap(auMap, "allowUserDelay")
			cfg.AutoUpdate.MaxDelayHours = getIntFromMap(auMap, "maxDelayHours")
			cfg.AutoUpdate.ForceRestartDelay = getBoolFromMap(auMap, "forceRestartDelay")
			cfg.AutoUpdate.RestartDelayHours = getIntFromMap(auMap, "restartDelayHours")
			cfg.AutoUpdate.UpdateOnLogon = getBoolFromMap(auMap, "updateOnLogon")
			cfg.AutoUpdate.MaintenanceWindows = getStringSliceFromMap(auMap, "maintenanceWindows")
			cfg.AutoUpdate.SilentInstall = getBoolFromMap(auMap, "silentInstall")
			cfg.AutoUpdate.AutoRollbackOnFailure = getBoolFromMap(auMap, "autoRollbackOnFailure")
		}
	}

	// Parse nested psadt object if present.
	if psadtRaw, ok := raw["psadt"]; ok {
		if psadtMap, ok := psadtRaw.(map[string]any); ok {
			cfg.PSADT.Enabled = getBoolPtrFromMap(psadtMap, "enabled")
			cfg.PSADT.RequiredVersion = getStringFromMap(psadtMap, "requiredVersion")
			cfg.PSADT.AutoInstallModule = getBoolPtrFromMap(psadtMap, "autoInstallModule")
			cfg.PSADT.InstallSource = getStringFromMap(psadtMap, "installSource")
			cfg.PSADT.ExecutionTimeoutSeconds = getIntPtrFromMap(psadtMap, "executionTimeoutSeconds")
			cfg.PSADT.FallbackPolicy = getStringFromMap(psadtMap, "fallbackPolicy")
			cfg.PSADT.InstallOnStartup = getBoolPtrFromMap(psadtMap, "installOnStartup")
			cfg.PSADT.InstallOnDemand = getBoolPtrFromMap(psadtMap, "installOnDemand")
			cfg.PSADT.SuccessExitCodes = getIntSliceFromMap(psadtMap, "successExitCodes")
			cfg.PSADT.RebootExitCodes = getIntSliceFromMap(psadtMap, "rebootExitCodes")
			cfg.PSADT.IgnoreExitCodes = getIntSliceFromMap(psadtMap, "ignoreExitCodes")
			cfg.PSADT.TimeoutAction = strings.ToLower(getStringFromMap(psadtMap, "timeoutAction"))
			cfg.PSADT.UnknownExitCodePolicy = strings.ToLower(getStringFromMap(psadtMap, "unknownExitCodePolicy"))
			normalizePSADTConfigDefaults(&cfg.PSADT)
		}
	}

	// Parse nested notificationBranding object if present.
	if brandingRaw, ok := raw["notificationBranding"]; ok {
		if brandingMap, ok := brandingRaw.(map[string]any); ok {
			cfg.NotificationBranding.CompanyName = getStringFromMap(brandingMap, "companyName")
			cfg.NotificationBranding.LogoURL = getStringFromMap(brandingMap, "logoUrl")
			cfg.NotificationBranding.BannerURL = getStringFromMap(brandingMap, "bannerUrl")
			if themeRaw, ok := brandingMap["theme"]; ok {
				if themeMap, ok := themeRaw.(map[string]any); ok {
					cfg.NotificationBranding.Theme.Surface = getStringFromMap(themeMap, "surface")
					cfg.NotificationBranding.Theme.Text = getStringFromMap(themeMap, "text")
					cfg.NotificationBranding.Theme.Accent = getStringFromMap(themeMap, "accent")
					cfg.NotificationBranding.Theme.Success = getStringFromMap(themeMap, "success")
					cfg.NotificationBranding.Theme.Warning = getStringFromMap(themeMap, "warning")
					cfg.NotificationBranding.Theme.Danger = getStringFromMap(themeMap, "danger")
				}
			}
		}
	}

	// Parse notificationPolicies list if present.
	if policiesRaw, ok := raw["notificationPolicies"]; ok {
		if policyItems, ok := policiesRaw.([]any); ok {
			policies := make([]AgentNotificationPolicy, 0, len(policyItems))
			for _, item := range policyItems {
				policyMap, ok := item.(map[string]any)
				if !ok {
					continue
				}
				policy := AgentNotificationPolicy{
					EventType:      getStringFromMap(policyMap, "eventType"),
					Mode:           getStringFromMap(policyMap, "mode"),
					Severity:       getStringFromMap(policyMap, "severity"),
					TimeoutSeconds: getIntPtrFromMap(policyMap, "timeoutSeconds"),
				}
				if styleRaw, ok := policyMap["styleOverride"]; ok {
					if styleMap, ok := styleRaw.(map[string]any); ok {
						policy.StyleOverride.Layout = getStringFromMap(styleMap, "layout")
						policy.StyleOverride.Background = getStringFromMap(styleMap, "background")
						policy.StyleOverride.Text = getStringFromMap(styleMap, "text")
					}
				}
				if actionsRaw, ok := policyMap["actions"]; ok {
					if actionItems, ok := actionsRaw.([]any); ok {
						actions := make([]AgentNotificationAction, 0, len(actionItems))
						for _, actionItem := range actionItems {
							actionMap, ok := actionItem.(map[string]any)
							if !ok {
								continue
							}
							actions = append(actions, AgentNotificationAction{
								ID:         getStringFromMap(actionMap, "id"),
								Label:      getStringFromMap(actionMap, "label"),
								ActionType: getStringFromMap(actionMap, "actionType"),
							})
						}
						policy.Actions = actions
					}
				}
				policies = append(policies, policy)
			}
			cfg.NotificationPolicies = policies
		}
	}

	// Parse rollout gates/kill-switches when present.
	if rolloutRaw, ok := raw["rollout"]; ok {
		if rolloutMap, ok := rolloutRaw.(map[string]any); ok {
			cfg.Rollout.EnableNotifications = getBoolPtrFromMap(rolloutMap, "enableNotifications")
			cfg.Rollout.EnableRequireConfirmation = getBoolPtrFromMap(rolloutMap, "enableRequireConfirmation")
			cfg.Rollout.EnablePSADTBootstrap = getBoolPtrFromMap(rolloutMap, "enablePsadtBootstrap")
			cfg.Rollout.AllowedNotificationEventTypes = getStringSliceFromMap(rolloutMap, "allowedNotificationEventTypes")
			cfg.Rollout.BlockedNotificationEventTypes = getStringSliceFromMap(rolloutMap, "blockedNotificationEventTypes")
		}
	}
	normalizePSADTConfigDefaults(&cfg.PSADT)
	normalizeRolloutDefaults(&cfg.Rollout)

	// Normalize ResolvedAt to RFC3339 when possible (keeps original otherwise)
	if cfg.ResolvedAt != "" {
		if t, err := time.Parse(time.RFC3339, cfg.ResolvedAt); err == nil {
			cfg.ResolvedAt = t.UTC().Format(time.RFC3339)
		}
	}

	return cfg, nil
}

func getBoolFromMap(m map[string]any, keys ...string) bool {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return toBool(v)
		}
	}
	return false
}

func getIntFromMap(m map[string]any, keys ...string) int {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return toInt(v)
		}
	}
	return 0
}

func getStringSliceFromMap(m map[string]any, key string) []string {
	raw, ok := m[key]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s := strings.TrimSpace(fmt.Sprint(e)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return v
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return nil
		}
		return []string{s}
	default:
		return nil
	}
}

func getBoolPtrFromMap(m map[string]any, keys ...string) *bool {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			b := toBool(v)
			return &b
		}
	}
	return nil
}

func getIntPtrFromMap(m map[string]any, keys ...string) *int {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			i := toInt(v)
			return &i
		}
	}
	return nil
}

func getStringFromMap(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return strings.TrimSpace(fmt.Sprint(v))
		}
	}
	return ""
}

func getIntSliceFromMap(m map[string]any, keys ...string) []int {
	for _, key := range keys {
		raw, ok := m[key]
		if !ok || raw == nil {
			continue
		}
		switch v := raw.(type) {
		case []any:
			out := make([]int, 0, len(v))
			for _, item := range v {
				out = append(out, toInt(item))
			}
			return out
		case []int:
			return append([]int(nil), v...)
		case string:
			text := strings.TrimSpace(v)
			if text == "" {
				return nil
			}
			parts := strings.Split(text, ",")
			out := make([]int, 0, len(parts))
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if part == "" {
					continue
				}
				out = append(out, toInt(part))
			}
			return out
		default:
			return nil
		}
	}
	return nil
}

func normalizePSADTConfigDefaults(cfg *AgentPSADTConfig) {
	if cfg == nil {
		return
	}
	if cfg.Enabled == nil {
		cfg.Enabled = ptrBoolConfig(true)
	}
	if strings.TrimSpace(cfg.RequiredVersion) == "" {
		cfg.RequiredVersion = "4.1.8"
	}
	if cfg.AutoInstallModule == nil {
		cfg.AutoInstallModule = ptrBoolConfig(true)
	}
	if strings.TrimSpace(cfg.InstallSource) == "" {
		cfg.InstallSource = "powershell_gallery"
	}
	if cfg.ExecutionTimeoutSeconds == nil || *cfg.ExecutionTimeoutSeconds <= 0 {
		cfg.ExecutionTimeoutSeconds = ptrIntConfig(1800)
	}
	if strings.TrimSpace(cfg.FallbackPolicy) == "" {
		cfg.FallbackPolicy = "winget_then_choco"
	}
	if cfg.InstallOnStartup == nil {
		cfg.InstallOnStartup = ptrBoolConfig(true)
	}
	if cfg.InstallOnDemand == nil {
		cfg.InstallOnDemand = ptrBoolConfig(true)
	}
	if len(cfg.SuccessExitCodes) == 0 {
		cfg.SuccessExitCodes = []int{0, 3010}
	}
	if len(cfg.RebootExitCodes) == 0 {
		cfg.RebootExitCodes = []int{1641, 3010}
	}
	if strings.TrimSpace(cfg.TimeoutAction) == "" {
		cfg.TimeoutAction = "fail"
	}
	if strings.TrimSpace(cfg.UnknownExitCodePolicy) == "" {
		cfg.UnknownExitCodePolicy = "recoverable_failure"
	}
}

func normalizeRolloutDefaults(cfg *AgentRolloutConfig) {
	if cfg == nil {
		return
	}
	if cfg.EnableNotifications == nil {
		cfg.EnableNotifications = ptrBoolConfig(true)
	}
	if cfg.EnableRequireConfirmation == nil {
		cfg.EnableRequireConfirmation = ptrBoolConfig(true)
	}
	if cfg.EnablePSADTBootstrap == nil {
		cfg.EnablePSADTBootstrap = ptrBoolConfig(true)
	}
}

func ptrBoolConfig(v bool) *bool {
	return &v
}

func ptrIntConfig(v int) *int {
	return &v
}

// setAgentConfiguration stores the parsed configuration and applies relevant settings.
func (a *App) setAgentConfiguration(cfg AgentConfiguration) {
	a.agentConfigMu.Lock()
	a.agentConfig = cfg
	a.agentConfigMu.Unlock()

	a.applyAgentConfiguration(cfg)
}

// applyAgentConfiguration adjusts runtime behavior based on the agent configuration.
func (a *App) applyAgentConfiguration(cfg AgentConfiguration) {
	// P2P files toggle.
	if cfg.P2PFilesEnabled != nil {
		p2pCfg := a.GetP2PConfig()
		p2pCfg.Enabled = *cfg.P2PFilesEnabled
		a.applyP2PConfig(p2pCfg)
	}

	if a.debugSvc != nil {
		changed, err := a.debugSvc.ApplyRemoteConnectionSecurity(
			cfg.NatsServerHost,
			cfg.NatsUseWssExternal,
			cfg.EnforceTlsHashValidation,
			cfg.HandshakeEnabled,
			cfg.ApiTlsCertHash,
			cfg.NatsTlsCertHash)
		if err != nil {
			a.logs.append("[config] falha ao aplicar seguranca remota: " + err.Error())
		} else if changed {
			a.logs.append("[config] seguranca remota aplicada; reconexao solicitada")
		}
	}

	// Discovery onboarding toggle — governs whether this agent participates in P2P onboarding.
	if cfg.DiscoveryEnabled != nil {
		a.logs.append(fmt.Sprintf("[config] discoveryEnabled=%t", *cfg.DiscoveryEnabled))
	}

	// Sync interval (if specified).
	if cfg.InventoryIntervalHours != nil && a.syncCoord != nil {
		if *cfg.InventoryIntervalHours > 0 {
			a.syncCoord.setPollEvery(time.Duration(*cfg.InventoryIntervalHours) * time.Hour)
		}
	}

	// MeshCentral agent install (runs once; idempotent via meshCentralInstalled flag in config.json).
	if cfg.MeshCentralEnabledEffective != nil && *cfg.MeshCentralEnabledEffective {
		go a.triggerMeshCentralInstallIfNeeded(context.Background())
	}

	// PSADT module bootstrap (non-blocking) when enabled by remote policy.
	go a.bootstrapPSADTFromConfiguration(cfg)
}

func (a *App) loadCachedAgentConfiguration() error {
	if a.db == nil {
		return fmt.Errorf("cache nao disponivel")
	}

	raw, err := a.db.CacheGet("agent_configuration_raw")
	if err != nil {
		return err
	}
	if raw == nil {
		return fmt.Errorf("cache de configuracao nao encontrada")
	}

	cfg, err := parseAgentConfiguration(raw)
	if err != nil {
		return err
	}
	a.setAgentConfiguration(cfg)
	a.logs.append("[sync] configuração do agent carregada do cache")
	return nil
}

func (a *App) bootstrapPSADTFromConfiguration(cfg AgentConfiguration) {
	if runtime.GOOS != "windows" {
		return
	}
	if cfg.Rollout.EnablePSADTBootstrap != nil && !*cfg.Rollout.EnablePSADTBootstrap {
		a.logs.append("[psadt] bootstrap bloqueado por rollout.enablePsadtBootstrap=false")
		return
	}
	if cfg.PSADT.Enabled == nil || !*cfg.PSADT.Enabled {
		return
	}
	autoInstallEnabled := cfg.PSADT.AutoInstallModule != nil && *cfg.PSADT.AutoInstallModule

	requiredVersion := strings.TrimSpace(cfg.PSADT.RequiredVersion)
	if requiredVersion == "" {
		requiredVersion = "4.1.8"
	}

	a.psadtBootstrapMu.Lock()
	if a.psadtBootstrapRunning {
		a.psadtBootstrapMu.Unlock()
		return
	}
	if strings.EqualFold(strings.TrimSpace(a.psadtBootstrappedVer), requiredVersion) {
		a.psadtBootstrapMu.Unlock()
		return
	}
	a.psadtBootstrapRunning = true
	a.psadtBootstrapMu.Unlock()

	defer func() {
		a.psadtBootstrapMu.Lock()
		a.psadtBootstrapRunning = false
		a.psadtBootstrapMu.Unlock()
	}()

	moduleStatus := a.CheckPSADTModuleStatus()
	if moduleStatus.Installed && strings.EqualFold(strings.TrimSpace(moduleStatus.Version), requiredVersion) {
		a.persistPSADTBootstrapEvent(database.PSADTBootstrapEntry{
			RequiredVersion:  requiredVersion,
			Installed:        true,
			InstalledVersion: strings.TrimSpace(moduleStatus.Version),
			Source:           strings.TrimSpace(cfg.PSADT.InstallSource),
			Message:          "modulo ja instalado na versao requerida",
		})
		a.psadtBootstrapMu.Lock()
		a.psadtBootstrappedVer = requiredVersion
		a.psadtBootstrapMu.Unlock()
		a.logs.append("[psadt] modulo ja instalado na versao requerida " + requiredVersion)
		return
	}

	if !autoInstallEnabled {
		message := "modulo PSADT nao instalado"
		if moduleStatus.Installed {
			message = "versao instalada " + strings.TrimSpace(moduleStatus.Version) + " difere da requerida " + requiredVersion
		}
		a.persistPSADTBootstrapEvent(database.PSADTBootstrapEntry{
			RequiredVersion:  requiredVersion,
			Installed:        moduleStatus.Installed,
			InstalledVersion: strings.TrimSpace(moduleStatus.Version),
			Source:           strings.TrimSpace(cfg.PSADT.InstallSource),
			Message:          message,
		})
		a.logs.append("[psadt] verificacao de versao no startup: " + message + " (autoInstallModule=false)")
		return
	}

	a.logs.append("[psadt] iniciando bootstrap do modulo na versao " + requiredVersion)
	installStatus := a.InstallPSADTModule(requiredVersion)
	if !installStatus.Installed {
		a.persistPSADTBootstrapEvent(database.PSADTBootstrapEntry{
			RequiredVersion: requiredVersion,
			Installed:       false,
			Source:          strings.TrimSpace(cfg.PSADT.InstallSource),
			Message:         strings.TrimSpace(installStatus.Message),
		})
		a.logs.append("[psadt] falha no bootstrap do modulo: " + strings.TrimSpace(installStatus.Message))
		return
	}
	a.persistPSADTBootstrapEvent(database.PSADTBootstrapEntry{
		RequiredVersion:  requiredVersion,
		Installed:        true,
		InstalledVersion: strings.TrimSpace(installStatus.Version),
		Source:           strings.TrimSpace(cfg.PSADT.InstallSource),
		Message:          strings.TrimSpace(installStatus.Message),
	})

	a.psadtBootstrapMu.Lock()
	a.psadtBootstrappedVer = requiredVersion
	a.psadtBootstrapMu.Unlock()
	a.logs.append("[psadt] bootstrap concluido com sucesso na versao " + strings.TrimSpace(installStatus.Version))
}

func (a *App) persistPSADTBootstrapEvent(entry database.PSADTBootstrapEntry) {
	if a == nil || a.db == nil {
		return
	}
	if err := a.db.SavePSADTBootstrapStatus(entry); err != nil {
		a.logs.append("[psadt] falha ao persistir historico de bootstrap: " + err.Error())
	}
}
