// Package agentconfig encapsula os tipos e a lógica pura de configuração
// do agente (parsing, merge hierárquico e normalização), separados do App.
package agentconfig

import "discovery/app/core/selfupdate"

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
	EnableConsolidationEngine     *bool    `json:"enableConsolidationEngine"`
	CommandResultOfflineMode      string   `json:"commandResultOfflineMode"`
	P2PTelemetryOfflineMode       string   `json:"p2pTelemetryOfflineMode"`
	AllowedNotificationEventTypes []string `json:"allowedNotificationEventTypes"`
	BlockedNotificationEventTypes []string `json:"blockedNotificationEventTypes"`
}

// AgentConsolidationPolicy defines the window mode for a specific data type.
type AgentConsolidationPolicy struct {
	DataType   string `json:"dataType"`
	WindowMode string `json:"windowMode"`
}

// AgentConsolidationConfig groups feature flags and policies for send windows.
type AgentConsolidationConfig struct {
	Enabled  *bool                      `json:"enabled"`
	Policies []AgentConsolidationPolicy `json:"policies"`
}

// AgentConfiguration defines the configuration schema returned by /api/v1/agent-auth/me/configuration.
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
	ChatAIEnabled                 *bool                           `json:"chatAIEnabled"`
	KnowledgeBaseEnabled          *bool                           `json:"knowledgeBaseEnabled"`
	AppStoreEnabled               *bool                           `json:"appStoreEnabled"`
	InventoryIntervalHours        *int                            `json:"inventoryIntervalHours"`
	AgentHeartbeatIntervalSeconds *int                            `json:"agentHeartbeatIntervalSeconds"`
	SiteID                        string                          `json:"siteId"`
	ClientID                      string                          `json:"clientId"`
	ResolvedAt                    string                          `json:"resolvedAt"`
	AutoUpdate                    AgentAutoUpdateConfig           `json:"autoUpdate"`
	AgentUpdate                   selfupdate.Policy               `json:"agentUpdate"`
	PSADT                         AgentPSADTConfig                `json:"psadt"`
	NotificationBranding          AgentNotificationBrandingConfig `json:"notificationBranding"`
	NotificationPolicies          []AgentNotificationPolicy       `json:"notificationPolicies"`
	Consolidation                 AgentConsolidationConfig        `json:"consolidation"`
	Rollout                       AgentRolloutConfig              `json:"rollout"`
	// StartupThrottleEnabled enables CPU-aware throttling during agent startup.
	// When nil (default), auto-detection is used (throttled on <=4 cores).
	// When explicitly false, the agent runs at full speed during startup.
	StartupThrottleEnabled *bool `json:"startupThrottleEnabled"`
	// StartupMaxCPUPercent defines the CPU usage threshold (0-100) above which
	// the agent will throttle osquery queries during startup. Default 50.
	StartupMaxCPUPercent *int `json:"startupMaxCPUPercent"`
}

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
	ID                        string  `json:"id"`
	SiteID                    string  `json:"siteId"`
	ClientID                  string  `json:"clientId"`
	RecoveryEnabled           *bool   `json:"recoveryEnabled"`
	DiscoveryEnabled          *bool   `json:"discoveryEnabled"`
	P2PFilesEnabled           *bool   `json:"p2PFilesEnabled"`
	SupportEnabled            *bool   `json:"supportEnabled"`
	ChatAIEnabled             *bool   `json:"chatAIEnabled"`
	KnowledgeBaseEnabled      *bool   `json:"knowledgeBaseEnabled"`
	AppStorePolicy            *string `json:"appStorePolicy"`
	AIIntegrationSettingsJSON *string `json:"aiIntegrationSettingsJson"`
	InventoryIntervalHours    *int    `json:"inventoryIntervalHours"`
	AutoUpdateSettingsJSON    *string `json:"autoUpdateSettingsJson"`
	AgentUpdatePolicyJSON     *string `json:"agentUpdatePolicyJson"`
	AgentOnlineGraceSeconds   *int    `json:"agentOnlineGraceSeconds"`
	Timezone                  *string `json:"timezone"`
	Location                  *string `json:"location"`
	ContactPerson             *string `json:"contactPerson"`
	ContactEmail              *string `json:"contactEmail"`
	LockedFieldsJSON          string  `json:"lockedFieldsJson"`
	CreatedAt                 string  `json:"createdAt"`
	UpdatedAt                 string  `json:"updatedAt"`
	CreatedBy                 *string `json:"createdBy"`
	UpdatedBy                 *string `json:"updatedBy"`
	Version                   int     `json:"version"`
	StartupThrottleEnabled    *bool   `json:"startupThrottleEnabled"`
	StartupMaxCPUPercent      *int    `json:"startupMaxCPUPercent"`
}

// AgentConfigResponse é o wrapper completo retornado por GET /me/configuration (API v1).
type AgentConfigResponse struct {
	Server *ServerConfiguration `json:"server"`
	Client *ClientConfiguration `json:"client"`
	Site   *SiteConfiguration   `json:"site"`
}
