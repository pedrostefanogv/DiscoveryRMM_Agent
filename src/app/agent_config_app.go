package app

import (
	"fmt"
	"strings"
	"time"

	"discovery/app/agentconfig"
	"discovery/app/core/selfupdate"
)

// setAgentConfiguration stores the parsed configuration and applies relevant settings.
func (a *App) setAgentConfiguration(cfg agentconfig.AgentConfiguration) {
	a.agentConfigMu.RLock()
	previous := a.agentConfig
	a.agentConfigMu.RUnlock()
	a.agentConfigMu.Lock()
	a.agentConfig = cfg
	a.agentConfigMu.Unlock()
	a.persistAgentRoutingContext(cfg)
	a.applyAgentConfiguration(cfg)
	if a.agentConn != nil {
		clientChanged := strings.TrimSpace(previous.ClientID) != strings.TrimSpace(cfg.ClientID)
		siteChanged := strings.TrimSpace(previous.SiteID) != strings.TrimSpace(cfg.SiteID)
		if clientChanged || siteChanged {
			a.logs.append("[config] contexto NATS canônico atualizado; reconexão solicitada")
			a.agentConn.Reload()
		}
	}
}

func (a *App) persistAgentRoutingContext(cfg agentconfig.AgentConfiguration) {
	clientID := strings.TrimSpace(cfg.ClientID)
	siteID := strings.TrimSpace(cfg.SiteID)
	if clientID == "" && siteID == "" {
		return
	}
	inst, path, err := loadInstallerConfig()
	if err != nil {
		a.logs.append("[config] falha ao carregar config compartilhada para clientId/siteId: " + err.Error())
		return
	}
	if strings.TrimSpace(inst.ClientID) == clientID && strings.TrimSpace(inst.SiteID) == siteID {
		return
	}
	inst.ClientID = clientID
	inst.SiteID = siteID
	if _, err := persistInstallerConfig(path, inst); err != nil {
		a.logs.append("[config] falha ao persistir clientId/siteId: " + err.Error())
		return
	}
	a.logs.append(fmt.Sprintf("[config] contexto canônico persistido: clientId=%s siteId=%s", clientID, siteID))
}

// applyAgentConfiguration adjusts runtime behavior based on the agent configuration.
func (a *App) applyAgentConfiguration(cfg agentconfig.AgentConfiguration) {
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
			a.logs.append("[config] segurança remota aplicada; reconexão solicitada")
		}
	}
	a.persistAgentUpdatePolicy(cfg.AgentUpdate)
	// Discovery onboarding toggle — governs whether this agent participates in P2P onboarding.
	if cfg.DiscoveryEnabled != nil {
		a.logs.append(fmt.Sprintf("[config] discoveryEnabled=%t", *cfg.DiscoveryEnabled))
	}
	// Sync interval (if specified).
	if cfg.InventoryIntervalHours != nil && a.syncCoord != nil {
		if *cfg.InventoryIntervalHours > 0 {
			a.syncCoord.SetPollEvery(time.Duration(*cfg.InventoryIntervalHours) * time.Hour)
		}
	}

	// Consolidation engine: propagar políticas de janela quando disponíveis.
	if a.consolEngine != nil {
		a.consolEngine.SetAgentID(strings.TrimSpace(a.GetDebugConfig().AgentID))
		a.consolEngine.ApplyAgentConfig(cfg)
	}
}

func (a *App) persistAgentUpdatePolicy(policy selfupdate.Policy) {
	policy = selfupdate.NormalizePolicy(policy)
	inst, path, err := loadInstallerConfig()
	if err != nil {
		a.logs.append("[config] falha ao carregar config compartilhada para agentUpdate: " + err.Error())
		return
	}
	if inst.AgentUpdate != nil && *inst.AgentUpdate == policy {
		return
	}
	inst.AgentUpdate = &policy
	if _, err := persistInstallerConfig(path, inst); err != nil {
		a.logs.append("[config] falha ao persistir agentUpdate em config compartilhada: " + err.Error())
		return
	}
	a.logs.append("[config] policy de agentUpdate persistida em config compartilhada")
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
	cfg, err := agentconfig.ParseAgentConfiguration(raw)
	if err != nil {
		return err
	}
	a.setAgentConfiguration(cfg)
	a.logs.append("[sync] configuração do agent carregada do cache")
	return nil
}
