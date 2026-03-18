package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
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

// AgentConfiguration defines the configuration schema returned by /api/agent-auth/me/configuration.
// It is used to control what features should be enabled on the agent.
type AgentConfiguration struct {
	RecoveryEnabled               *bool                 `json:"recoveryEnabled"`
	DiscoveryEnabled              *bool                 `json:"discoveryEnabled"`
	P2PFilesEnabled               *bool                 `json:"p2pFilesEnabled"`
	SupportEnabled                *bool                 `json:"supportEnabled"`
	MeshCentralEnabledEffective   *bool                 `json:"meshCentralEnabledEffective"`
	MeshCentralGroupPolicyProfile string                `json:"meshCentralGroupPolicyProfile"`
	ChatAIEnabled                 *bool                 `json:"chatAIEnabled"`
	KnowledgeBaseEnabled          *bool                 `json:"knowledgeBaseEnabled"`
	AppStoreEnabled               *bool                 `json:"appStoreEnabled"`
	InventoryIntervalHours        *int                  `json:"inventoryIntervalHours"`
	AgentHeartbeatIntervalSeconds *int                  `json:"agentHeartbeatIntervalSeconds"`
	SiteID                        string                `json:"siteId"`
	ClientID                      string                `json:"clientId"`
	ResolvedAt                    string                `json:"resolvedAt"`
	AutoUpdate                    AgentAutoUpdateConfig `json:"autoUpdate"`
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

// setAgentConfiguration stores the parsed configuration and applies relevant settings.
func (a *App) setAgentConfiguration(cfg AgentConfiguration) {
	a.agentConfigMu.Lock()
	a.agentConfig = cfg
	a.agentConfigMu.Unlock()

	a.applyAgentConfiguration(cfg)
}

// applyAgentConfiguration adjusts runtime behavior based on the agent configuration.
func (a *App) applyAgentConfiguration(cfg AgentConfiguration) {
	// P2P toggle
	if cfg.P2PFilesEnabled != nil {
		p2pCfg := a.GetP2PConfig()
		p2pCfg.Enabled = *cfg.P2PFilesEnabled
		a.applyP2PConfig(p2pCfg)
	}

	// Sync interval (if specified)
	if cfg.InventoryIntervalHours != nil && a.syncCoord != nil {
		if *cfg.InventoryIntervalHours > 0 {
			a.syncCoord.setPollEvery(time.Duration(*cfg.InventoryIntervalHours) * time.Hour)
		}
	}

	// (future) apply more flags as needed
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
