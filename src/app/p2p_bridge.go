package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func (a *App) applyP2PConfig(cfg P2PConfig) {
	a.P2PMu.Lock()
	a.P2PConfig = normalizeP2PConfig(cfg)
	a.P2PMu.Unlock()
}

func (a *App) GetP2PConfig() P2PConfig {
	a.P2PMu.RLock()
	cfg := a.P2PConfig
	a.P2PMu.RUnlock()
	return normalizeP2PConfig(cfg)
}

func (a *App) SetP2PConfig(cfg P2PConfig) error {
	cfg = normalizeP2PConfig(cfg)
	a.applyP2PConfig(cfg)

	inst, path, err := loadInstallerConfig()
	if err != nil {
		inst = InstallerConfig{}
		path = ""
	}
	inst.P2P = cfg
	if _, err := persistInstallerConfig(path, inst); err != nil {
		a.Logs.Append("[p2p] falha ao persistir configuração em config.json: " + err.Error())
		return err
	}

	a.Logs.Append(fmt.Sprintf("[p2p] configuração atualizada: enabled=%t mode=%s ttlHours=%d seedPercent=%d minSeeds=%d",
		cfg.Enabled, cfg.DiscoveryMode, cfg.TempTTLHours, cfg.SeedPercent, cfg.MinSeeds))
	return nil
}

func (a *App) GetP2PDebugStatus() P2PDebugStatus {
	// Companion (modo interface): o coordinator P2P roda no serviço — consulta
	// via IPC RPC. A UI cria o objeto em NewApp, mas NUNCA o inicia (o core
	// vive só no serviço, runCoreStartup); sem este roteamento a página de
	// Status exibia "P2P ativo: Offline" e "Bytes P2P: 0 B / 0 B" para sempre.
	if a.ipcClient != nil {
		if resp, ok := a.ipcRequest("p2p:debug_status", nil); ok {
			if data, err := json.Marshal(resp["data"]); err == nil {
				var out P2PDebugStatus
				if json.Unmarshal(data, &out) == nil {
					return out
				}
			}
		}
	}
	if a.P2PCoord != nil {
		return a.P2PCoord.GetStatus()
	}
	return P2PDebugStatus{}
}

func (a *App) GetP2PPeers() []P2PPeerView {
	// Companion: os peers descobertos moram no serviço (o coordinator local
	// nunca inicia na UI) — IPC RPC alimenta "Agentes P2P conectados".
	if a.ipcClient != nil {
		if resp, ok := a.ipcRequest("p2p:peers", nil); ok {
			if data, err := json.Marshal(resp["data"]); err == nil {
				var out []P2PPeerView
				if json.Unmarshal(data, &out) == nil {
					return out
				}
			}
		}
	}
	if a.P2PCoord != nil {
		return a.P2PCoord.GetPeers()
	}
	return []P2PPeerView{}
}

func (a *App) RefreshP2PPeerCatalog() {
	if a.P2PCoord == nil {
		return
	}
	a.P2PCoord.RefreshPeerArtifactIndex(context.Background(), "manual")
}

func (a *App) SyncP2PBootstrapNow() (string, error) {
	if a.P2PCoord == nil {
		return "", fmt.Errorf("coordinator P2P indisponível")
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	bootstrapCtx, cancel := context.WithTimeout(ctx, p2pCloudBootstrapTimeout+5*time.Second)
	defer cancel()
	localPeers, localErr := a.P2PCoord.RunLANDiscoveryProbe(bootstrapCtx, "manual")
	cloudEnabled := a.GetP2PConfig().BootstrapConfig.CloudBootstrapEnabled

	parts := make([]string, 0, 2)
	if localErr == nil {
		parts = append(parts, fmt.Sprintf("LAN: %d peer(s)", localPeers))
	}

	cloudPeers := 0
	cloudErr := error(nil)
	if cloudEnabled {
		cloudPeers, cloudErr = a.P2PCoord.RunCloudBootstrap(bootstrapCtx)
		if cloudErr == nil {
			parts = append(parts, fmt.Sprintf("cloud: %d peer(s)", cloudPeers))
		}
	}

	if localErr != nil && !cloudEnabled {
		return "", localErr
	}
	if localErr != nil && cloudErr != nil {
		return "", fmt.Errorf("descoberta LAN: %v; cloud bootstrap: %v", localErr, cloudErr)
	}
	if localErr != nil && cloudErr == nil {
		return "sincronização concluída: " + strings.Join(parts, " | ") + " (descoberta LAN com aviso)", nil
	}
	if localErr == nil && cloudErr != nil && cloudEnabled {
		return "sincronização concluída: " + strings.Join(parts, " | ") + " (cloud bootstrap com aviso)", nil
	}
	if len(parts) == 0 {
		return "sincronização concluída sem peers novos", nil
	}
	return "sincronização concluída: " + strings.Join(parts, " | "), nil
}

func (a *App) GetP2PPeerArtifactIndex() []P2PPeerArtifactIndexView {
	if a.P2PCoord == nil {
		return []P2PPeerArtifactIndexView{}
	}
	return a.P2PCoord.GetPeerArtifactIndex()
}

// FindP2PArtifactPeers returns availability of an artifact across known peers.
// Lookup is performed exclusively by canonical ArtifactID.
func (a *App) FindP2PArtifactPeers(artifactName string) P2PArtifactAvailabilityView {
	if a.P2PCoord == nil {
		return P2PArtifactAvailabilityView{ArtifactName: sanitizeArtifactName(artifactName), PeerAgentIDs: []string{}}
	}
	return a.P2PCoord.FindArtifactPeers(artifactName)
}

func (a *App) GetP2PTempDir() string {
	return a.p2pTempDir()
}

func (a *App) CleanupP2PTempNow() (string, error) {
	removed, err := a.cleanupExpiredP2PTempArtifacts(time.Now())
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("limpeza concluida: %d item(ns) removido(s)", removed), nil
}

func (a *App) ClearAllP2PArtifacts() (string, error) {
	removed, err := a.clearAllP2PTempArtifacts(time.Now())
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("limpeza total concluida: %d item(ns) removido(s)", removed), nil
}

// DeleteP2PArtifact remove um único artifact (arquivo + manifest + cache SHA256) do diretório P2P.
func (a *App) DeleteP2PArtifact(artifactName string) (string, error) {
	if a.P2PCoord == nil {
		return "", fmt.Errorf("coordinator P2P indisponível")
	}
	if err := a.P2PCoord.DeleteArtifact(artifactName); err != nil {
		return "", err
	}
	return fmt.Sprintf("artifact %q apagado", artifactName), nil
}

func (a *App) ComputeP2PSeedPlan(totalAgents int) P2PSeedPlan {
	cfg := a.GetP2PConfig()
	return buildP2PSeedPlan(totalAgents, cfg)
}

func (a *App) GetP2PArtifactAccess(artifactName, targetPeerID string) (P2PArtifactAccess, error) {
	if a.P2PCoord == nil {
		return P2PArtifactAccess{}, fmt.Errorf("coordinator P2P indisponível")
	}
	return a.P2PCoord.GetArtifactAccess(artifactName, targetPeerID)
}

func (a *App) ListP2PArtifacts() ([]P2PArtifactView, error) {
	if a.P2PCoord == nil {
		return []P2PArtifactView{}, nil
	}
	return a.P2PCoord.ListArtifacts()
}

func (a *App) PublishP2PTestArtifact(artifactName, content string) (P2PArtifactView, error) {
	if a.P2PCoord == nil {
		return P2PArtifactView{}, fmt.Errorf("coordinator P2P indisponível")
	}
	return a.P2PCoord.PublishTestArtifact(artifactName, content)
}

func (a *App) SelectAndPublishP2PArtifact() (P2PArtifactView, error) {
	if a.P2PCoord == nil {
		return P2PArtifactView{}, fmt.Errorf("coordinator P2P indisponível")
	}
	if a.app == nil {
		return P2PArtifactView{}, fmt.Errorf("runtime de aplicação indisponível")
	}
	selectedPath, err := a.app.Dialog.OpenFile().PromptForSingleSelection()
	if err != nil {
		return P2PArtifactView{}, err
	}
	selectedPath = strings.TrimSpace(selectedPath)
	if selectedPath == "" {
		return P2PArtifactView{}, fmt.Errorf("selecao cancelada")
	}
	return a.P2PCoord.PublishFile(selectedPath)
}

func (a *App) ReplicateP2PArtifactToPeer(artifactName, targetPeerID string) (string, error) {
	return "", fmt.Errorf("modo push desabilitado: use transferencia pull sob demanda")
}

func (a *App) PullP2PArtifactFromPeer(artifactName, sourcePeerID string) (P2PArtifactView, error) {
	if a.P2PCoord == nil {
		return P2PArtifactView{}, fmt.Errorf("coordinator P2P indisponível")
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return a.P2PCoord.DownloadArtifactFromPeer(ctx, artifactName, sourcePeerID)
}

// DownloadP2PArtifactSwarm finds all peers that have the artifact and performs
// a chunked swarm download when ≥2 peers are available; otherwise falls back
// to the single-peer path.
func (a *App) DownloadP2PArtifactSwarm(artifactName string) (P2PArtifactView, error) {
	if a.P2PCoord == nil {
		return P2PArtifactView{}, fmt.Errorf("coordinator P2P indisponivel")
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return a.P2PCoord.DownloadArtifactSwarm(ctx, artifactName)
}

func (a *App) ListP2PAuditEvents() []P2PAuditEvent {
	if a.P2PCoord == nil {
		return []P2PAuditEvent{}
	}
	return a.P2PCoord.ListAuditEvents()
}

func (a *App) ListP2PAuditEventsFiltered(action, peerAgentID, status string) []P2PAuditEvent {
	if a.P2PCoord == nil {
		return []P2PAuditEvent{}
	}
	return a.P2PCoord.ListAuditEventsFiltered(action, peerAgentID, status)
}

// GetAutoProvisioningStats retorna estatísticas de auto-provisioning do lado
// deste agente enquanto provisionador (agente configurado que entrega ofertas).
// Útil para monitorar quantos agentes genéricos este peer já configurou.
func (a *App) GetAutoProvisioningStats() P2PAutoProvisioningStats {
	// Companion: o contador de Zero Touch provisionados é do serviço (o
	// coordinator que aplica as ofertas roda lá; o local nunca inicia).
	if a.ipcClient != nil {
		if resp, ok := a.ipcRequest("p2p:ztc_stats", nil); ok {
			if data, err := json.Marshal(resp["data"]); err == nil {
				var out P2PAutoProvisioningStats
				if json.Unmarshal(data, &out) == nil {
					return out
				}
			}
		}
	}
	agentCfg := a.GetAgentConfiguration()
	enabled := agentCfg.DiscoveryEnabled == nil || *agentCfg.DiscoveryEnabled

	if a.P2PCoord == nil {
		return P2PAutoProvisioningStats{Enabled: enabled, RecentEvents: []P2POnboardingAuditEvent{}}
	}

	c := a.P2PCoord
	total, events := c.GetAutoProvisioningStats()

	// Retornar os eventos mais recentes primeiro.
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}

	return P2PAutoProvisioningStats{
		Enabled:          enabled && isAgentConfigured(),
		TotalProvisioned: total,
		RecentEvents:     events,
	}
}

// GetOnboardingStatus retorna o status do agente sob perspectiva de onboarding:
// se está configurado ou aguardando provisionamento automático da rede P2P.
func (a *App) GetOnboardingStatus() map[string]interface{} {
	configured := isAgentConfigured()
	result := map[string]interface{}{
		"configured": configured,
		"mode":       "normal",
	}
	if !configured {
		result["mode"] = "awaiting-auto-provisioning"
		result["message"] = "Agente genérico: aguardando auto-provisioning da rede P2P"
		return result
	}

	if a.isZeroTouchApprovalPending() {
		result["mode"] = "awaiting-approval"
		result["message"] = "Dispositivo provisionado, aguardando aprovacao da equipe de TI para integracao com o servidor."
	}
	return result
}
