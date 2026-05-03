package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

func (a *App) applyP2PConfig(cfg P2PConfig) {
	a.p2pMu.Lock()
	a.p2pConfig = normalizeP2PConfig(cfg)
	a.p2pMu.Unlock()
}

func (a *App) GetP2PConfig() P2PConfig {
	a.p2pMu.RLock()
	cfg := a.p2pConfig
	a.p2pMu.RUnlock()
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
		a.logs.append("[p2p] falha ao persistir configuracao em config.json: " + err.Error())
		return err
	}

	a.logs.append(fmt.Sprintf("[p2p] configuracao atualizada: enabled=%t mode=%s ttlHours=%d seedPercent=%d minSeeds=%d",
		cfg.Enabled, cfg.DiscoveryMode, cfg.TempTTLHours, cfg.SeedPercent, cfg.MinSeeds))
	return nil
}

func (a *App) GetP2PDebugStatus() P2PDebugStatus {
	if a.p2pCoord == nil {
		return P2PDebugStatus{}
	}
	return a.p2pCoord.GetStatus()
}

func (a *App) GetP2PPeers() []P2PPeerView {
	if a.p2pCoord == nil {
		return []P2PPeerView{}
	}
	return a.p2pCoord.GetPeers()
}

func (a *App) RefreshP2PPeerCatalog() {
	if a.p2pCoord == nil {
		return
	}
	a.p2pCoord.RefreshPeerArtifactIndex(context.Background(), "manual")
}

func (a *App) SyncP2PBootstrapNow() (string, error) {
	if a.p2pCoord == nil {
		return "", fmt.Errorf("coordinator P2P indisponivel")
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	bootstrapCtx, cancel := context.WithTimeout(ctx, p2pCloudBootstrapTimeout+5*time.Second)
	defer cancel()
	peerCount, err := a.p2pCoord.runCloudBootstrap(bootstrapCtx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("sincronizacao de bootstrap P2P concluida: %d peer(s) retornado(s)", peerCount), nil
}

func (a *App) GetP2PPeerArtifactIndex() []P2PPeerArtifactIndexView {
	if a.p2pCoord == nil {
		return []P2PPeerArtifactIndexView{}
	}
	return a.p2pCoord.GetPeerArtifactIndex()
}

// FindP2PArtifactPeers returns availability of an artifact across known peers.
// Lookup is performed exclusively by canonical ArtifactID.
func (a *App) FindP2PArtifactPeers(artifactName string) P2PArtifactAvailabilityView {
	if a.p2pCoord == nil {
		return P2PArtifactAvailabilityView{ArtifactName: sanitizeArtifactName(artifactName), PeerAgentIDs: []string{}}
	}
	return a.p2pCoord.FindArtifactPeers(artifactName)
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

func (a *App) ComputeP2PSeedPlan(totalAgents int) P2PSeedPlan {
	cfg := a.GetP2PConfig()
	return buildP2PSeedPlan(totalAgents, cfg)
}

func (a *App) GetP2PArtifactAccess(artifactName, targetPeerID string) (P2PArtifactAccess, error) {
	if a.p2pCoord == nil {
		return P2PArtifactAccess{}, fmt.Errorf("coordinator P2P indisponivel")
	}
	return a.p2pCoord.GetArtifactAccess(artifactName, targetPeerID)
}

func (a *App) ListP2PArtifacts() ([]P2PArtifactView, error) {
	if a.p2pCoord == nil {
		return []P2PArtifactView{}, nil
	}
	return a.p2pCoord.ListArtifacts()
}

func (a *App) PublishP2PTestArtifact(artifactName, content string) (P2PArtifactView, error) {
	if a.p2pCoord == nil {
		return P2PArtifactView{}, fmt.Errorf("coordinator P2P indisponivel")
	}
	return a.p2pCoord.PublishTestArtifact(artifactName, content)
}

func (a *App) SelectAndPublishP2PArtifact() (P2PArtifactView, error) {
	if a.p2pCoord == nil {
		return P2PArtifactView{}, fmt.Errorf("coordinator P2P indisponivel")
	}
	if a.ctx == nil {
		return P2PArtifactView{}, fmt.Errorf("contexto de runtime indisponivel")
	}
	selectedPath, err := wailsRuntime.OpenFileDialog(a.ctx, wailsRuntime.OpenDialogOptions{})
	if err != nil {
		return P2PArtifactView{}, err
	}
	selectedPath = strings.TrimSpace(selectedPath)
	if selectedPath == "" {
		return P2PArtifactView{}, fmt.Errorf("selecao cancelada")
	}
	return a.p2pCoord.PublishFile(selectedPath)
}

func (a *App) ReplicateP2PArtifactToPeer(artifactName, targetPeerID string) (string, error) {
	return "", fmt.Errorf("modo push desabilitado: use transferencia pull sob demanda")
}

func (a *App) PullP2PArtifactFromPeer(artifactName, sourcePeerID string) (P2PArtifactView, error) {
	if a.p2pCoord == nil {
		return P2PArtifactView{}, fmt.Errorf("coordinator P2P indisponivel")
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return a.p2pCoord.DownloadArtifactFromPeer(ctx, artifactName, sourcePeerID)
}

// DownloadP2PArtifactSwarm finds all peers that have the artifact and performs
// a chunked swarm download when ≥2 peers are available; otherwise falls back
// to the single-peer path.
func (a *App) DownloadP2PArtifactSwarm(artifactName string) (P2PArtifactView, error) {
	if a.p2pCoord == nil {
		return P2PArtifactView{}, fmt.Errorf("coordinator P2P indisponivel")
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return a.p2pCoord.downloadArtifactSwarm(ctx, artifactName)
}

func (a *App) ListP2PAuditEvents() []P2PAuditEvent {
	if a.p2pCoord == nil {
		return []P2PAuditEvent{}
	}
	return a.p2pCoord.ListAuditEvents()
}

func (a *App) ListP2PAuditEventsFiltered(action, peerAgentID, status string) []P2PAuditEvent {
	if a.p2pCoord == nil {
		return []P2PAuditEvent{}
	}
	return a.p2pCoord.ListAuditEventsFiltered(action, peerAgentID, status)
}

// GetAutoProvisioningStats retorna estatísticas de auto-provisioning do lado
// deste agente enquanto provisionador (agente configurado que entrega ofertas).
// Útil para monitorar quantos agentes genéricos este peer já configurou.
func (a *App) GetAutoProvisioningStats() P2PAutoProvisioningStats {
	agentCfg := a.GetAgentConfiguration()
	enabled := agentCfg.DiscoveryEnabled == nil || *agentCfg.DiscoveryEnabled

	if a.p2pCoord == nil {
		return P2PAutoProvisioningStats{Enabled: enabled, RecentEvents: []P2POnboardingAuditEvent{}}
	}

	c := a.p2pCoord
	c.autoProvisionedMu.RLock()
	total := c.autoProvisionedCount
	events := make([]P2POnboardingAuditEvent, len(c.autoProvisionedAudit))
	copy(events, c.autoProvisionedAudit)
	c.autoProvisionedMu.RUnlock()

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
	}
	return result
}
