package app

import (
	"encoding/json"
	"strings"
)

// ── API v1 Configuration Models ──────────────────────────────────────────────
//
// Estes modelos refletem a hierarquia de configuração da nova API v1:
//   ServerConfiguration → ClientConfiguration → SiteConfiguration
//
// Cada nível pode conter sub-configs serializadas como JSON strings
// (ex: autoUpdateSettingsJson, agentUpdatePolicyJson, brandingSettingsJson).

// ServerConfiguration representa a configuração global do servidor (API v1).
type ServerConfiguration struct {
	ID                            string `json:"id"`
	RecoveryEnabled               bool   `json:"recoveryEnabled"`
	DiscoveryEnabled              bool   `json:"discoveryEnabled"`
	P2PFilesEnabled               bool   `json:"p2PFilesEnabled"`
	CloudBootstrapEnabled         bool   `json:"cloudBootstrapEnabled"`
	SupportEnabled                bool   `json:"supportEnabled"`
	ChatAIEnabled                 bool   `json:"chatAIEnabled"`
	KnowledgeBaseEnabled          bool   `json:"knowledgeBaseEnabled"`
	AppStorePolicy                string `json:"appStorePolicy"`
	InventoryIntervalHours        int    `json:"inventoryIntervalHours"`
	AutoUpdateSettingsJSON        string `json:"autoUpdateSettingsJson"`
	AgentUpdatePolicyJSON         string `json:"agentUpdatePolicyJson"`
	AgentHeartbeatIntervalSeconds int    `json:"agentHeartbeatIntervalSeconds"`
	AgentOnlineGraceSeconds       int    `json:"agentOnlineGraceSeconds"`
	BrandingSettingsJSON          string `json:"brandingSettingsJson"`
	AIIntegrationSettingsJSON     string `json:"aiIntegrationSettingsJson"`
	NatsEnabled                   bool   `json:"natsEnabled"`
	NatsAuthEnabled               bool   `json:"natsAuthEnabled"`
	NatsAgentJwtTtlMinutes        int    `json:"natsAgentJwtTtlMinutes"`
	NatsUserJwtTtlMinutes         int    `json:"natsUserJwtTtlMinutes"`
	NatsServerHostInternal        string `json:"natsServerHostInternal"`
	NatsServerHostExternal        string `json:"natsServerHostExternal"`
	NatsUseWssExternal            bool   `json:"natsUseWssExternal"`
	ReportingSettingsJSON         string `json:"reportingSettingsJson"`
	RetentionSettingsJSON         string `json:"retentionSettingsJson"`
	TicketAttachmentSettingsJSON  string `json:"ticketAttachmentSettingsJson"`
	CreatedAt                     string `json:"createdAt"`
	UpdatedAt                     string `json:"updatedAt"`
	// StartupThrottleEnabled enables CPU-aware throttling during agent startup.
	StartupThrottleEnabled *bool `json:"startupThrottleEnabled"`
	// StartupMaxCPUPercent defines the CPU usage threshold (0-100) for throttling.
	StartupMaxCPUPercent int `json:"startupMaxCPUPercent"`
}

// ClientConfiguration representa a configuração no nível do cliente (API v1).
type ClientConfiguration struct {
	ID                            string  `json:"id"`
	ClientID                      string  `json:"clientId"`
	RecoveryEnabled               *bool   `json:"recoveryEnabled"`
	DiscoveryEnabled              *bool   `json:"discoveryEnabled"`
	P2PFilesEnabled               *bool   `json:"p2PFilesEnabled"`
	CloudBootstrapEnabled         *bool   `json:"cloudBootstrapEnabled"`
	SupportEnabled                *bool   `json:"supportEnabled"`
	ChatAIEnabled                 *bool   `json:"chatAIEnabled"`
	KnowledgeBaseEnabled          *bool   `json:"knowledgeBaseEnabled"`
	AppStorePolicy                *string `json:"appStorePolicy"`
	AIIntegrationSettingsJSON     *string `json:"aiIntegrationSettingsJson"`
	InventoryIntervalHours        *int    `json:"inventoryIntervalHours"`
	AutoUpdateSettingsJSON        *string `json:"autoUpdateSettingsJson"`
	AgentUpdatePolicyJSON         *string `json:"agentUpdatePolicyJson"`
	AgentHeartbeatIntervalSeconds *int    `json:"agentHeartbeatIntervalSeconds"`
	AgentOnlineGraceSeconds       *int    `json:"agentOnlineGraceSeconds"`
	LockedFieldsJSON              string  `json:"lockedFieldsJson"`
	CreatedAt                     string  `json:"createdAt"`
	UpdatedAt                     string  `json:"updatedAt"`
	CreatedBy                     *string `json:"createdBy"`
	UpdatedBy                     *string `json:"updatedBy"`
	Version                       int     `json:"version"`
	StartupThrottleEnabled        *bool   `json:"startupThrottleEnabled"`
	StartupMaxCPUPercent          *int    `json:"startupMaxCPUPercent"`
}

// SiteConfiguration representa a configuração no nível do site (API v1).
type SiteConfiguration struct {
	ID                                   string  `json:"id"`
	SiteID                               string  `json:"siteId"`
	ClientID                             string  `json:"clientId"`
	RecoveryEnabled                      *bool   `json:"recoveryEnabled"`
	DiscoveryEnabled                     *bool   `json:"discoveryEnabled"`
	P2PFilesEnabled                      *bool   `json:"p2PFilesEnabled"`
	SupportEnabled                       *bool   `json:"supportEnabled"`
	ChatAIEnabled                        *bool   `json:"chatAIEnabled"`
	KnowledgeBaseEnabled                 *bool   `json:"knowledgeBaseEnabled"`
	AppStorePolicy                       *string `json:"appStorePolicy"`
	AIIntegrationSettingsJSON            *string `json:"aiIntegrationSettingsJson"`
	InventoryIntervalHours               *int    `json:"inventoryIntervalHours"`
	AutoUpdateSettingsJSON               *string `json:"autoUpdateSettingsJson"`
	AgentUpdatePolicyJSON                *string `json:"agentUpdatePolicyJson"`
	AgentOnlineGraceSeconds              *int    `json:"agentOnlineGraceSeconds"`
	Timezone                             *string `json:"timezone"`
	Location                             *string `json:"location"`
	ContactPerson                        *string `json:"contactPerson"`
	ContactEmail                         *string `json:"contactEmail"`
	LockedFieldsJSON                     string  `json:"lockedFieldsJson"`
	CreatedAt                            string  `json:"createdAt"`
	UpdatedAt                            string  `json:"updatedAt"`
	CreatedBy                            *string `json:"createdBy"`
	UpdatedBy                            *string `json:"updatedBy"`
	Version                              int     `json:"version"`
	StartupThrottleEnabled               *bool   `json:"startupThrottleEnabled"`
	StartupMaxCPUPercent                 *int    `json:"startupMaxCPUPercent"`
}

// AgentConfigResponse é o wrapper completo retornado por GET /me/configuration (API v1).
type AgentConfigResponse struct {
	Server *ServerConfiguration `json:"server"`
	Client *ClientConfiguration `json:"client"`
	Site   *SiteConfiguration   `json:"site"`
}

// ── API v1 Helper: resolve configuração com fallback hierárquico ──────────────
//
// Usa o padrão: Site → Client → Server (mais específico primeiro).

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
	cfg.NatsServerHost = srv.NatsServerHostExternal
	if srv.NatsServerHostExternal == "" {
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
