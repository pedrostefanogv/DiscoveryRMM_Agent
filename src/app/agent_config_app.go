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
	a.AgentConfigMu.RLock()
	previous := a.AgentConfig
	a.AgentConfigMu.RUnlock()
	a.AgentConfigMu.Lock()
	a.AgentConfig = cfg
	a.AgentConfigMu.Unlock()
	a.persistAgentRoutingContext(cfg)
	a.applyAgentConfiguration(cfg)
	if a.AgentConn != nil {
		clientChanged := strings.TrimSpace(previous.ClientID) != strings.TrimSpace(cfg.ClientID)
		siteChanged := strings.TrimSpace(previous.SiteID) != strings.TrimSpace(cfg.SiteID)
		if clientChanged || siteChanged {
			a.Logs.Append("[config] contexto NATS canônico atualizado; reconexão solicitada")
			a.AgentConn.Reload()
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
		a.Logs.Append("[config] falha ao carregar config compartilhada para clientId/siteId: " + err.Error())
		return
	}
	if strings.TrimSpace(inst.ClientID) == clientID && strings.TrimSpace(inst.SiteID) == siteID {
		return
	}
	inst.ClientID = clientID
	inst.SiteID = siteID
	if _, err := persistInstallerConfig(path, inst); err != nil {
		a.Logs.Append("[config] falha ao persistir clientId/siteId: " + err.Error())
		return
	}
	a.Logs.Append(fmt.Sprintf("[config] contexto canônico persistido: clientId=%s siteId=%s", clientID, siteID))
}

// applyAgentConfiguration adjusts runtime behavior based on the agent configuration.
func (a *App) applyAgentConfiguration(cfg agentconfig.AgentConfiguration) {
	// P2P files toggle.
	if cfg.P2PFilesEnabled != nil {
		p2pCfg := a.GetP2PConfig()
		p2pCfg.Enabled = *cfg.P2PFilesEnabled
		a.applyP2PConfig(p2pCfg)
	}
	// Instalação de winget via P2P-first — quando a API remota envia o campo,
	// ele sobrescreve o valor local. Quando ausente, mantém o default do agente (true).
	if a.DebugSvc != nil {
		changed, applyErr := a.DebugSvc.ApplyP2PWingetInstallEnabledRemote(cfg.AutomationP2PWingetInstallEnabled)
		if applyErr != nil {
			a.Logs.Append("[config] falha ao aplicar automationP2pWingetInstallEnabled remota: " + applyErr.Error())
		} else if changed {
			a.Logs.Append("[config] automationP2pWingetInstallEnabled atualizado pela API")
		}
	}
	if a.DebugSvc != nil {
		changed, err := a.DebugSvc.ApplyRemoteConnectionSecurity(
			cfg.NatsServerHost,
			cfg.NatsServerHostInternal,
			cfg.NatsUseWssExternal,
			cfg.EnforceTlsHashValidation,
			cfg.HandshakeEnabled,
			cfg.ApiTlsCertHash,
			cfg.NatsTlsCertHash)
		if err != nil {
			a.Logs.Append("[config] falha ao aplicar seguranca remota: " + err.Error())
		} else if changed {
			a.Logs.Append("[config] segurança remota aplicada; reconexão solicitada")
		}
	}
	a.persistAgentUpdatePolicy(cfg.AgentUpdate)
	// Discovery onboarding toggle — governs whether this agent participates in P2P onboarding.
	if cfg.DiscoveryEnabled != nil {
		a.Logs.Append(fmt.Sprintf("[config] discoveryEnabled=%t", *cfg.DiscoveryEnabled))
	}
	// Sync interval (if specified).
	if cfg.InventoryIntervalHours != nil && a.SyncSvc != nil {
		if *cfg.InventoryIntervalHours > 0 {
			a.SyncSvc.SetPollEvery(time.Duration(*cfg.InventoryIntervalHours) * time.Hour)
		}
	}

	// Consolidation engine: propagar políticas de janela quando disponíveis.
	if a.ConsolEngine != nil {
		a.ConsolEngine.SetAgentID(strings.TrimSpace(a.GetDebugConfig().AgentID))
		a.ConsolEngine.ApplyAgentConfig(cfg)
	}
}

func (a *App) persistAgentUpdatePolicy(policy selfupdate.Policy) {
	policy = selfupdate.NormalizePolicy(policy)
	inst, path, err := loadInstallerConfig()
	if err != nil {
		a.Logs.Append("[config] falha ao carregar config compartilhada para agentUpdate: " + err.Error())
		return
	}
	if inst.AgentUpdate != nil && *inst.AgentUpdate == policy {
		return
	}
	inst.AgentUpdate = &policy
	if _, err := persistInstallerConfig(path, inst); err != nil {
		a.Logs.Append("[config] falha ao persistir agentUpdate em config compartilhada: " + err.Error())
		return
	}
	a.Logs.Append("[config] policy de agentUpdate persistida em config compartilhada")
}

func (a *App) loadCachedAgentConfiguration() error {
	if a.CoreAgent.DB == nil {
		return fmt.Errorf("cache nao disponivel")
	}
	raw, err := a.CoreAgent.DB.CacheGet("agent_configuration_raw")
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
	a.Logs.Append("[sync] configuração do agent carregada do cache")
	return nil
}
