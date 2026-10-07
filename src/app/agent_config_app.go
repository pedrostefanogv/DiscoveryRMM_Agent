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
	clientChanged := strings.TrimSpace(previous.ClientID) != strings.TrimSpace(cfg.ClientID)
	siteChanged := strings.TrimSpace(previous.SiteID) != strings.TrimSpace(cfg.SiteID)
	// Transferência de site/cliente: descarta a identidade resolvida do suporte.
	// Sem isso o cache "agent_info" (24h) serviria clientId/siteId antigos e a
	// página mostraria conteúdo do escopo anterior.
	if a.SupportSvc != nil && (clientChanged || siteChanged) {
		a.SupportSvc.InvalidateAgentContext()
		a.Logs.Append("[config] contexto do agente mudou; cache de suporte invalidado")
	}
	if a.AgentConn != nil {
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
	// Cloud bootstrap: quando o servidor envia o campo, ele é a fonte da verdade
	// (o agent não expõe esse ajuste localmente).
	if cfg.CloudBootstrapEnabled != nil {
		p2pCfg := a.GetP2PConfig()
		if p2pCfg.BootstrapConfig.CloudBootstrapEnabled != *cfg.CloudBootstrapEnabled {
			p2pCfg.BootstrapConfig.CloudBootstrapEnabled = *cfg.CloudBootstrapEnabled
			a.applyP2PConfig(p2pCfg)
			a.Logs.Append(fmt.Sprintf("[config] cloudBootstrapEnabled=%t aplicado ao P2P", *cfg.CloudBootstrapEnabled))
		}
	}

	// P2P files toggle.
	// P2P só funciona se os agents conseguirem se descobrir: Descoberta de Rede
	// (LAN) OU Bootstrap P2P via Nuvem. Sem nenhum dos dois, o download/serviço
	// de artifacts é desativado mesmo com p2pFilesEnabled=true no servidor.
	if cfg.P2PFilesEnabled != nil {
		p2pCfg := a.GetP2PConfig()
		effective, dependencyMissing := resolveP2PFileTransferEnabled(
			*cfg.P2PFilesEnabled,
			cfg.DiscoveryEnabled,
			cfg.CloudBootstrapEnabled,
			p2pCfg.BootstrapConfig.CloudBootstrapEnabled,
		)

		if p2pCfg.Enabled != effective {
			p2pCfg.Enabled = effective
			a.applyP2PConfig(p2pCfg)
		}
		if dependencyMissing {
			a.Logs.Append("[config] p2pFilesEnabled=true ignorado: habilite Descoberta de Rede ou Bootstrap P2P via Nuvem para os agents se encontrarem na rede")
		}
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

// resolveP2PFileTransferEnabled decide se o subsistema P2P de arquivos deve
// ficar ativo. Requer que o servidor tenha pedido (p2pFilesEnabled) E que exista
// um caminho de descoberta: Descoberta de Rede na LAN ou Bootstrap P2P via Nuvem.
// Sem descoberta os agents não se encontram e o download/serviço de artifacts
// ficaria ativo sem função.
//
// dependencyMissing é true quando o P2P foi pedido mas não há caminho de
// descoberta (usado para log/telemetria).
func resolveP2PFileTransferEnabled(
	requested bool,
	discoveryEnabled *bool,
	cloudBootstrapEnabled *bool,
	cloudFallback bool,
) (enabled bool, dependencyMissing bool) {
	discovery := discoveryEnabled == nil || *discoveryEnabled
	cloud := cloudFallback
	if cloudBootstrapEnabled != nil {
		cloud = *cloudBootstrapEnabled
	}

	allowed := discovery || cloud
	return requested && allowed, requested && !allowed
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
