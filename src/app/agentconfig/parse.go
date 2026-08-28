package agentconfig

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"discovery/app/core/selfupdate"
)

// ParseAgentConfiguration parses a configuration blob into a normalized AgentConfiguration.
// Supports both:
//   - Old flat format: {"chatAIEnabled": true, "appStoreEnabled": true, ...}
//   - New API v1 hierarchical format: {"server": {...}, "client": {...}, "site": {...}}
func ParseAgentConfiguration(data []byte) (AgentConfiguration, error) {
	var cfg AgentConfiguration
	var err error

	// Try new API v1 format first (has "server" key at top level)
	if newCfg, ok := tryParseAgentConfigV1(data); ok {
		cfg = newCfg
	} else {
		// Fallback to legacy flat format
		cfg, err = parseLegacyAgentConfiguration(data)
		if err != nil {
			return AgentConfiguration{}, err
		}
	}

	// Default: instalação de winget via P2P habilitada quando o campo não foi
	// fornecido pela API (valor ausente/nil). Pode ser desligado explicitamente.
	if cfg.AutomationP2PWingetInstallEnabled == nil {
		enabled := true
		cfg.AutomationP2PWingetInstallEnabled = &enabled
	}
	return cfg, nil
}

// tryParseAgentConfigV1 tenta parsear o formato hierárquico novo (API v1).
func tryParseAgentConfigV1(data []byte) (AgentConfiguration, bool) {
	var resp AgentConfigResponse
	if err := json.Unmarshal(data, &resp); err != nil || resp.Server == nil {
		return AgentConfiguration{}, false
	}
	return mergeAgentConfigResponse(&resp), true
}

// parseLegacyAgentConfiguration parseia o formato flat antigo.
func parseLegacyAgentConfiguration(data []byte) (AgentConfiguration, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return AgentConfiguration{}, fmt.Errorf("falha ao decodificar configuração do agent: %w", err)
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
		b := ToBool(v)
		return &b
	}
	getIntPtr := func(keys ...string) *int {
		v := getAny(keys...)
		if v == nil {
			return nil
		}
		i := ToInt(v)
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
		RecoveryEnabled:                   getBoolPtr("recoveryEnabled"),
		DiscoveryEnabled:                  getBoolPtr("discoveryEnabled"),
		P2PFilesEnabled:                   getBoolPtr("p2pFilesEnabled"),
		SupportEnabled:                    getBoolPtr("supportEnabled"),
		NatsServerHost:                    getString("natsServerHost"),
		NatsServerHostInternal:            getString("natsServerHostInternal"),
		NatsUseWssExternal:                getBoolPtr("natsUseWssExternal"),
		EnforceTlsHashValidation:          getBoolPtr("enforceTlsHashValidation"),
		HandshakeEnabled:                  getBoolPtr("handshakeEnabled"),
		ApiTlsCertHash:                    strings.ToUpper(getString("apiTlsCertHash")),
		NatsTlsCertHash:                   strings.ToUpper(getString("natsTlsCertHash")),
		ChatAIEnabled:                     getBoolPtr("chatAIEnabled"),
		KnowledgeBaseEnabled:              getBoolPtr("knowledgeBaseEnabled"),
		AppStoreEnabled:                   getBoolPtr("appStoreEnabled"),
		AutomationP2PWingetInstallEnabled: getBoolPtr("automationP2pWingetInstallEnabled"),
		InventoryIntervalHours:            getIntPtr("inventoryIntervalHours"),
		AgentHeartbeatIntervalSeconds:     getIntPtr("agentHeartbeatIntervalSeconds"),
		SiteID:                            getString("siteId"),
		ClientID:                          getString("clientId"),
		ResolvedAt:                        getString("resolvedAt"),
		AgentUpdate:                       selfupdate.DefaultPolicy(),
	}
	// Parse nested autoUpdate object if present.
	hasAutoUpdate := false
	if auRaw, ok := raw["autoUpdate"]; ok {
		if auMap, ok := auRaw.(map[string]any); ok {
			hasAutoUpdate = true
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
	if agentUpdateRaw, ok := raw["agentUpdate"]; ok {
		if agentUpdateMap, ok := agentUpdateRaw.(map[string]any); ok {
			cfg.AgentUpdate = parseAgentUpdatePolicy(agentUpdateMap)
		} else {
			cfg.AgentUpdate = selfupdate.NormalizePolicy(cfg.AgentUpdate)
		}
	} else if hasAutoUpdate {
		cfg.AgentUpdate = deriveAgentUpdatePolicyFromLegacy(cfg.AutoUpdate)
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
			NormalizePSADTConfigDefaults(&cfg.PSADT)
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
	// Parse consolidation policies when present.
	if consolidationRaw, ok := raw["consolidation"]; ok {
		if consolidationMap, ok := consolidationRaw.(map[string]any); ok {
			cfg.Consolidation.Enabled = getBoolPtrFromMap(consolidationMap, "enabled")
			if policiesRaw, ok := consolidationMap["policies"]; ok {
				if policyItems, ok := policiesRaw.([]any); ok {
					policies := make([]AgentConsolidationPolicy, 0, len(policyItems))
					for _, item := range policyItems {
						policyMap, ok := item.(map[string]any)
						if !ok {
							continue
						}
						policies = append(policies, AgentConsolidationPolicy{
							DataType:   getStringFromMap(policyMap, "dataType"),
							WindowMode: getStringFromMap(policyMap, "windowMode"),
						})
					}
					cfg.Consolidation.Policies = policies
				}
			}
		}
	}
	// Parse rollout gates/kill-switches when present.
	if rolloutRaw, ok := raw["rollout"]; ok {
		if rolloutMap, ok := rolloutRaw.(map[string]any); ok {
			cfg.Rollout.EnableNotifications = getBoolPtrFromMap(rolloutMap, "enableNotifications")
			cfg.Rollout.EnableRequireConfirmation = getBoolPtrFromMap(rolloutMap, "enableRequireConfirmation")
			cfg.Rollout.EnablePSADTBootstrap = getBoolPtrFromMap(rolloutMap, "enablePsadtBootstrap")
			cfg.Rollout.EnableConsolidationEngine = getBoolPtrFromMap(rolloutMap, "enableConsolidationEngine")
			cfg.Rollout.CommandResultOfflineMode = getStringFromMap(rolloutMap, "commandResultOfflineMode")
			cfg.Rollout.P2PTelemetryOfflineMode = getStringFromMap(rolloutMap, "p2pTelemetryOfflineMode")
			cfg.Rollout.AllowedNotificationEventTypes = getStringSliceFromMap(rolloutMap, "allowedNotificationEventTypes")
			cfg.Rollout.BlockedNotificationEventTypes = getStringSliceFromMap(rolloutMap, "blockedNotificationEventTypes")
		}
	}
	// Parse startup throttle fields (flat format only; API v1 handled in mergeAgentConfigResponse).
	cfg.StartupThrottleEnabled = getBoolPtr("startupThrottleEnabled")
	cfg.StartupMaxCPUPercent = getIntPtr("startupMaxCPUPercent")
	NormalizePSADTConfigDefaults(&cfg.PSADT)
	normalizeConsolidationConfigDefaults(&cfg.Consolidation)
	NormalizeRolloutDefaults(&cfg.Rollout)
	// Normalize ResolvedAt to RFC3339 when possible (keeps original otherwise)
	if cfg.ResolvedAt != "" {
		if t, err := time.Parse(time.RFC3339, cfg.ResolvedAt); err == nil {
			cfg.ResolvedAt = t.UTC().Format(time.RFC3339)
		}
	}
	return cfg, nil
}

// mergeAgentConfigResponse mescla a hierarquia Server → Client → Site
// em um AgentConfiguration flat (compatível com o modelo existente).
func mergeAgentConfigResponse(resp *AgentConfigResponse) AgentConfiguration {
	if resp == nil || resp.Server == nil {
		return AgentConfiguration{}
	}

	srv := resp.Server
	cli := resp.Client
	site := resp.Site

	cfg := AgentConfiguration{}

	// Boolean flags — site sobrescreve client sobrescreve server
	cfg.RecoveryEnabled = boolPtr(resolveBool(
		boolFromPtr(site, func(s *SiteConfiguration) *bool { return s.RecoveryEnabled }),
		boolFromPtr(cli, func(c *ClientConfiguration) *bool { return c.RecoveryEnabled }),
		srv.RecoveryEnabled,
	))
	cfg.DiscoveryEnabled = boolPtr(resolveBool(
		boolFromPtr(site, func(s *SiteConfiguration) *bool { return s.DiscoveryEnabled }),
		boolFromPtr(cli, func(c *ClientConfiguration) *bool { return c.DiscoveryEnabled }),
		srv.DiscoveryEnabled,
	))
	cfg.P2PFilesEnabled = boolPtr(resolveBool(
		boolFromPtr(site, func(s *SiteConfiguration) *bool { return s.P2PFilesEnabled }),
		boolFromPtr(cli, func(c *ClientConfiguration) *bool { return c.P2PFilesEnabled }),
		srv.P2PFilesEnabled,
	))
	cfg.SupportEnabled = boolPtr(resolveBool(
		boolFromPtr(site, func(s *SiteConfiguration) *bool { return s.SupportEnabled }),
		boolFromPtr(cli, func(c *ClientConfiguration) *bool { return c.SupportEnabled }),
		srv.SupportEnabled,
	))
	cfg.ChatAIEnabled = boolPtr(resolveBool(
		boolFromPtr(site, func(s *SiteConfiguration) *bool { return s.ChatAIEnabled }),
		boolFromPtr(cli, func(c *ClientConfiguration) *bool { return c.ChatAIEnabled }),
		srv.ChatAIEnabled,
	))
	cfg.KnowledgeBaseEnabled = boolPtr(resolveBool(
		boolFromPtr(site, func(s *SiteConfiguration) *bool { return s.KnowledgeBaseEnabled }),
		boolFromPtr(cli, func(c *ClientConfiguration) *bool { return c.KnowledgeBaseEnabled }),
		srv.KnowledgeBaseEnabled,
	))
	cfg.AppStoreEnabled = boolPtr(resolveBool(
		boolFromPtr(site, func(s *SiteConfiguration) *bool {
			if s.AppStorePolicy != nil && strings.ToLower(*s.AppStorePolicy) != "disabled" {
				b := true
				return &b
			}
			return nil
		}),
		boolFromPtr(cli, func(c *ClientConfiguration) *bool {
			if c.AppStorePolicy != nil && strings.ToLower(*c.AppStorePolicy) != "disabled" {
				b := true
				return &b
			}
			return nil
		}),
		strings.ToLower(srv.AppStorePolicy) != "disabled",
	))
	// Instalação de winget via P2P-first. Precedência: site > client > server.
	// Se o servidor não enviar o campo (nil), o padrão é habilitado (true) para
	// que a ausência no /me/configuration não desligue o comportamento.
	serverP2PWinget := true
	if srv.AutomationP2PWingetInstallEnabled != nil {
		serverP2PWinget = *srv.AutomationP2PWingetInstallEnabled
	}
	cfg.AutomationP2PWingetInstallEnabled = boolPtr(resolveBool(
		boolFromPtr(site, func(s *SiteConfiguration) *bool { return s.AutomationP2PWingetInstallEnabled }),
		boolFromPtr(cli, func(c *ClientConfiguration) *bool { return c.AutomationP2PWingetInstallEnabled }),
		serverP2PWinget,
	))

	// Int fields
	cfg.InventoryIntervalHours = resolveIntPtr(
		intFromPtr(site, func(s *SiteConfiguration) *int { return s.InventoryIntervalHours }),
		resolveInt(
			intFromPtr(cli, func(c *ClientConfiguration) *int { return c.InventoryIntervalHours }),
			srv.InventoryIntervalHours,
		),
	)
	cfg.AgentHeartbeatIntervalSeconds = resolveIntPtr(
		intFromPtr(site, func(s *SiteConfiguration) *int { return nil }), // site não tem heartbeat
		resolveInt(
			intFromPtr(cli, func(c *ClientConfiguration) *int { return c.AgentHeartbeatIntervalSeconds }),
			srv.AgentHeartbeatIntervalSeconds,
		),
	)

	// String fields
	// NatsServerHost (externo) é o host público usado para WSS; o host interno
	// (LAN) é preservado separadamente para permitir NATS nativo quando o agente
	// está na mesma rede do servidor.
	cfg.NatsServerHost = srv.NatsServerHostExternal
	cfg.NatsServerHostInternal = srv.NatsServerHostInternal
	if cfg.NatsServerHost == "" {
		cfg.NatsServerHost = srv.NatsServerHostInternal
	}
	cfg.NatsUseWssExternal = boolPtr(srv.NatsUseWssExternal)

	// Parse embedded JSON strings (autoUpdate, agentUpdate, branding, etc.)
	// Server fornece os defaults; Client/Site podem sobrescrever
	autoUpdateJSON := srv.AutoUpdateSettingsJSON
	if cli != nil && cli.AutoUpdateSettingsJSON != nil && *cli.AutoUpdateSettingsJSON != "" {
		autoUpdateJSON = *cli.AutoUpdateSettingsJSON
	}
	if site != nil && site.AutoUpdateSettingsJSON != nil && *site.AutoUpdateSettingsJSON != "" {
		autoUpdateJSON = *site.AutoUpdateSettingsJSON
	}
	if autoUpdateJSON != "" {
		var auCfg AgentAutoUpdateConfig
		if err := json.Unmarshal([]byte(autoUpdateJSON), &auCfg); err == nil {
			cfg.AutoUpdate = auCfg
		}
	}

	agentUpdateJSON := srv.AgentUpdatePolicyJSON
	if cli != nil && cli.AgentUpdatePolicyJSON != nil && *cli.AgentUpdatePolicyJSON != "" {
		agentUpdateJSON = *cli.AgentUpdatePolicyJSON
	}
	if site != nil && site.AgentUpdatePolicyJSON != nil && *site.AgentUpdatePolicyJSON != "" {
		agentUpdateJSON = *site.AgentUpdatePolicyJSON
	}
	if agentUpdateJSON != "" {
		var auPolicy map[string]any
		if err := json.Unmarshal([]byte(agentUpdateJSON), &auPolicy); err == nil {
			cfg.AgentUpdate = parseAgentUpdatePolicy(auPolicy)
		}
	}

	brandingJSON := srv.BrandingSettingsJSON
	if brandingJSON != "" {
		var branding AgentNotificationBrandingConfig
		if err := json.Unmarshal([]byte(brandingJSON), &branding); err == nil {
			cfg.NotificationBranding = branding
		}
	}

	// Startup throttle fields: server provides defaults; site/client can override.
	cfg.StartupThrottleEnabled = resolveBoolPtr(
		resolveBoolPtr(
			boolFromPtr(site, func(s *SiteConfiguration) *bool { return s.StartupThrottleEnabled }),
			boolFromPtr(cli, func(c *ClientConfiguration) *bool { return c.StartupThrottleEnabled }),
		),
		srv.StartupThrottleEnabled,
	)
	cfg.StartupMaxCPUPercent = resolveIntPtr(
		intFromPtr(site, func(s *SiteConfiguration) *int { return s.StartupMaxCPUPercent }),
		resolveInt(
			intFromPtr(cli, func(c *ClientConfiguration) *int { return c.StartupMaxCPUPercent }),
			srv.StartupMaxCPUPercent,
		),
	)

	return cfg
}

// Helpers
func boolPtr(b bool) *bool       { return &b }
func stringPtr(s string) *string { return &s }

func boolFromPtr[T any](obj *T, getter func(*T) *bool) *bool {
	if obj == nil {
		return nil
	}
	return getter(obj)
}

func intFromPtr[T any](obj *T, getter func(*T) *int) *int {
	if obj == nil {
		return nil
	}
	return getter(obj)
}

func stringFromPtr[T any](obj *T, getter func(*T) *string) *string {
	if obj == nil {
		return nil
	}
	return getter(obj)
}

func resolveBool(val *bool, parentVal *bool, defaultVal bool) bool {
	if val != nil {
		return *val
	}
	if parentVal != nil {
		return *parentVal
	}
	return defaultVal
}

func resolveBoolPtr(val *bool, parentVal *bool) *bool {
	if val != nil {
		return val
	}
	return parentVal
}

func resolveString(val *string, parentVal string) string {
	if val != nil {
		return *val
	}
	return parentVal
}

func resolveInt(val *int, parentVal int) int {
	if val != nil {
		return *val
	}
	return parentVal
}

func resolveIntPtr(val *int, parentVal int) *int {
	if val != nil {
		return val
	}
	if parentVal == 0 {
		return nil
	}
	return &parentVal
}

func parseAgentUpdatePolicy(raw map[string]any) selfupdate.Policy {
	policy := selfupdate.Policy{
		Enabled:                    getBoolFromMap(raw, "enabled", "isEnabled"),
		CheckOnStartup:             getBoolFromMap(raw, "checkOnStartup"),
		CheckPeriodically:          getBoolFromMap(raw, "checkPeriodically"),
		CheckOnSyncManifest:        getBoolFromMap(raw, "checkOnSyncManifest"),
		CheckEveryHours:            getIntFromMap(raw, "checkEveryHours", "checkEvery"),
		PreferredArtifactType:      getStringFromMap(raw, "preferredArtifactType", "artifactType"),
		RequireSignatureValidation: getBoolFromMap(raw, "requireSignatureValidation"),
	}
	return selfupdate.NormalizePolicy(policy)
}

func deriveAgentUpdatePolicyFromLegacy(legacy AgentAutoUpdateConfig) selfupdate.Policy {
	policy := selfupdate.DefaultPolicy()
	policy.Enabled = legacy.Enabled
	if legacy.CheckEveryHours > 0 {
		policy.CheckEveryHours = legacy.CheckEveryHours
	}
	return selfupdate.NormalizePolicy(policy)
}

// NormalizePSADTConfigDefaults aplica defaults à configuração PSADT.
func NormalizePSADTConfigDefaults(cfg *AgentPSADTConfig) {
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

// NormalizeRolloutDefaults aplica defaults à configuração de rollout.
func NormalizeRolloutDefaults(cfg *AgentRolloutConfig) {
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
	if cfg.EnableConsolidationEngine == nil {
		cfg.EnableConsolidationEngine = ptrBoolConfig(true)
	}
	cfg.CommandResultOfflineMode = NormalizeOfflineQueueMode(cfg.CommandResultOfflineMode)
	cfg.P2PTelemetryOfflineMode = NormalizeOfflineQueueMode(cfg.P2PTelemetryOfflineMode)
}

func normalizeConsolidationConfigDefaults(cfg *AgentConsolidationConfig) {
	if cfg == nil {
		return
	}
	if len(cfg.Policies) == 0 {
		return
	}
	normalized := make([]AgentConsolidationPolicy, 0, len(cfg.Policies))
	for _, policy := range cfg.Policies {
		dataType := strings.TrimSpace(strings.ToLower(policy.DataType))
		if dataType == "" {
			continue
		}
		normalized = append(normalized, AgentConsolidationPolicy{
			DataType:   dataType,
			WindowMode: NormalizeConsolidationWindowMode(policy.WindowMode),
		})
	}
	cfg.Policies = normalized
}

func ptrBoolConfig(v bool) *bool {
	return &v
}
func ptrIntConfig(v int) *int {
	return &v
}

func getBoolFromMap(m map[string]any, keys ...string) bool {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return ToBool(v)
		}
	}
	return false
}
func getIntFromMap(m map[string]any, keys ...string) int {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return ToInt(v)
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
			b := ToBool(v)
			return &b
		}
	}
	return nil
}
func getIntPtrFromMap(m map[string]any, keys ...string) *int {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			i := ToInt(v)
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
				out = append(out, ToInt(item))
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
				out = append(out, ToInt(part))
			}
			return out
		default:
			return nil
		}
	}
	return nil
}

// ToInt converte um valor para int com tolerância a tipos comuns.
func ToInt(values ...any) int {
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

// ToBool converte um valor para bool com tolerância a tipos comuns.
func ToBool(values ...any) bool {
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
