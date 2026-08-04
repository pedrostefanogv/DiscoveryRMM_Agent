package p2p

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

func (c *Coordinator) GetStatus() P2PDebugStatus {
	cfg := c.deps.GetP2PConfig()
	c.mu.RLock()
	active := c.deps.DebugMode() && cfg.Enabled
	listenAddress := c.listenAddress
	if strings.TrimSpace(listenAddress) == "" && c.transferServer != nil {
		listenAddress = c.transferServer.BaseURL()
	}
	lastDiscoveryTick := c.lastDiscoveryTick
	if lastDiscoveryTick.IsZero() && active {
		lastDiscoveryTick = time.Now().UTC()
	}
	defer c.mu.RUnlock()
	return P2PDebugStatus{
		Active:               active,
		DiscoveryMode:        cfg.DiscoveryMode,
		KnownPeers:           c.knownPeers,
		ListenAddress:        listenAddress,
		TempDir:              c.deps.P2PTempDir(),
		TempTTLHours:         cfg.TempTTLHours,
		LastCleanupUTC:       formatTimeRFC3339(c.lastCleanupUTC),
		LastDiscoveryTickUTC: formatTimeRFC3339(lastDiscoveryTick),
		LastError:            c.lastErr,
		CurrentSeedPlan:      c.currentSeedPlan,
		Metrics:              c.metrics,
	}
}

func (c *Coordinator) GetPeers() []P2PPeerView {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]P2PPeerView, 0, len(c.peers))
	for _, peer := range c.peers {
		out = append(out, P2PPeerView{
			AgentID:      peer.Peer.AgentID,
			ClientID:     peer.Peer.ClientID,
			Host:         peer.Peer.Host,
			Address:      peer.Peer.Address,
			Port:         peer.Peer.Port,
			Source:       peer.Peer.Source,
			LastSeenUTC:  formatTimeRFC3339(peer.LastSeenUTC),
			KnownPeers:   peer.Peer.KnownPeers,
			ConnectedVia: peer.Peer.ConnectedVia,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		hi := strings.ToLower(strings.TrimSpace(out[i].Host))
		hj := strings.ToLower(strings.TrimSpace(out[j].Host))
		if hi != hj {
			return hi < hj
		}
		return strings.ToLower(strings.TrimSpace(out[i].AgentID)) < strings.ToLower(strings.TrimSpace(out[j].AgentID))
	})
	return out
}

// GetPeerArtifactIndex SEMPRE faz fetch live dos artifacts de cada peer via libp2p.
// Não usa cache — é o caminho de debug que precisa de dados fidedignos.
func (c *Coordinator) GetPeerArtifactIndex() []P2PPeerArtifactIndexView {
	h, registry := c.libp2pHostAndRegistry()

	c.mu.RLock()
	peers := make([]p2pPeerState, 0, len(c.peers))
	for k, v := range c.peers {
		_ = k
		peers = append(peers, v)
	}
	c.mu.RUnlock()

	type liveEntry struct {
		agentID    string
		host       string
		artifacts  []P2PArtifactView
		source     string
		updatedUTC string
	}
	var entries []liveEntry
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, peer := range peers {
		if strings.TrimSpace(peer.Peer.Address) == "" || peer.Peer.Port <= 0 {
			continue
		}
		if h == nil || registry == nil {
			continue
		}
		wg.Add(1)
		go func(p p2pPeerState) {
			defer wg.Done()
			lpID, ok := registry.Lookup(p.Peer.AgentID)
			if !ok {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			resp, err := libp2pFetchPeers(ctx, h, lpID)
			if err != nil {
				return
			}
			// Atualiza o cache com dados frescos.
			c.upsertPeerArtifacts(p.Peer.AgentID, resp.Artifacts, "debug-live")

			mu.Lock()
			entries = append(entries, liveEntry{
				agentID:    strings.TrimSpace(p.Peer.AgentID),
				host:       strings.TrimSpace(p.Peer.Host),
				artifacts:  resp.Artifacts,
				source:     "live",
				updatedUTC: resp.UpdatedAtUTC,
			})
			mu.Unlock()
		}(peer)
	}
	wg.Wait()

	out := make([]P2PPeerArtifactIndexView, 0, len(entries))
	for _, e := range entries {
		out = append(out, P2PPeerArtifactIndexView{
			PeerAgentID:    e.agentID,
			PeerHost:       e.host,
			LastUpdatedUTC: e.updatedUTC,
			Source:         e.source,
			Artifacts:      e.artifacts,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		hi := strings.ToLower(strings.TrimSpace(out[i].PeerHost))
		hj := strings.ToLower(strings.TrimSpace(out[j].PeerHost))
		if hi != hj {
			return hi < hj
		}
		return strings.ToLower(strings.TrimSpace(out[i].PeerAgentID)) < strings.ToLower(strings.TrimSpace(out[j].PeerAgentID))
	})
	return out
}

// FindArtifactPeers returns a summary of which peers currently advertise the
// named artifact. Lookup uses the in-memory cache (peerArtifacts) first;
// on cache miss, falls back to live libp2p query to each known peer.
//
// O cache peerArtifacts é populado por:
//   - gossip pull (RefreshPeerArtifactIndex)
//   - fetch on-demand (libp2pFetchPeers)
//   - snapshots NATS (ApplyP2PDiscoverySnapshot)
//
// Usar o cache evita latência (5s timeout por peer) e permite que o lookup
// funcione mesmo quando libp2p não está disponível (ex.: modo service-only).
func (c *Coordinator) FindArtifactPeers(artifactName string) P2PArtifactAvailabilityView {
	safeArtifact := sanitizeArtifactName(artifactName)
	artifactID := CanonicalArtifactID("", safeArtifact, "")
	result := P2PArtifactAvailabilityView{
		ArtifactID:   artifactID,
		ArtifactName: strings.TrimSpace(safeArtifact),
		PeerAgentIDs: []string{},
	}
	if artifactID == "" {
		return result
	}

	// 1. Consulta o cache peerArtifacts primeiro (rápido, sem rede).
	//    Cada entrada é peerKey (lowercase agentID) → []P2PArtifactView.
	cacheHadEntry := false
	c.mu.RLock()
	for peerKey, state := range c.peerArtifacts {
		for _, artifact := range state.Artifacts {
			if strings.EqualFold(strings.TrimSpace(artifact.ArtifactID), artifactID) {
				result.PeerAgentIDs = append(result.PeerAgentIDs, peerKey)
				cacheHadEntry = true
				break
			}
		}
	}
	c.mu.RUnlock()

	// 2. Se o cache tinha pelo menos um peer com o artifact, retorna imediatamente.
	//    Não há motivo para fazer fetch live — já temos a resposta.
	if cacheHadEntry {
		sort.Strings(result.PeerAgentIDs)
		result.PeerCount = len(result.PeerAgentIDs)
		result.Found = result.PeerCount > 0
		return result
	}

	// 3. Cache miss total → fetch on-demand via libp2p para cada peer conhecido.
	//    Atualiza o cache peerArtifacts com os resultados para próximas chamadas.
	h, registry := c.libp2pHostAndRegistry()
	if h != nil && registry != nil {
		c.mu.RLock()
		peers := make([]p2pPeerState, 0, len(c.peers))
		for _, p := range c.peers {
			peers = append(peers, p)
		}
		c.mu.RUnlock()

		for _, peer := range peers {
			if lpID, ok := registry.Lookup(peer.Peer.AgentID); ok {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				resp, err := libp2pFetchPeers(ctx, h, lpID)
				cancel()
				if err != nil {
					continue
				}
				// Atualiza o cache com os artifacts recebidos.
				c.upsertPeerArtifacts(peer.Peer.AgentID, resp.Artifacts, "on-demand")
				// Verifica se este peer tem o artifact procurado.
				for _, a := range resp.Artifacts {
					if strings.EqualFold(strings.TrimSpace(a.ArtifactID), artifactID) {
						result.PeerAgentIDs = append(result.PeerAgentIDs, strings.TrimSpace(peer.Peer.AgentID))
						break
					}
				}
			}
		}
	}

	sort.Strings(result.PeerAgentIDs)
	result.PeerCount = len(result.PeerAgentIDs)
	result.Found = result.PeerCount > 0
	return result
}

// resolveArtifactNameByID retorna o nome do arquivo de um artifact a partir do
// seu artifactID (GUID de release ou "sha256:<hex>"), consultando o cache de
// peers. Retorna "" se não encontrar. Usado quando o fetch recebe apenas o
// artifactID e precisa do nome real para o download.
func (c *Coordinator) resolveArtifactNameByID(artifactID string) string {
	artifactID = strings.TrimSpace(artifactID)
	if artifactID == "" {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, state := range c.peerArtifacts {
		for _, a := range state.Artifacts {
			if strings.EqualFold(strings.TrimSpace(a.ArtifactID), artifactID) {
				return strings.TrimSpace(a.ArtifactName)
			}
		}
	}
	return ""
}

// InvalidatePeerArtifact remove um artifact específico do cache de um peer.
// Usado quando o cache diz que o peer tem o artifact mas o download falha
// (peer não tem mais o arquivo).
func (c *Coordinator) InvalidatePeerArtifact(peerAgentID, artifactName string) {
	peerKey := strings.ToLower(strings.TrimSpace(peerAgentID))
	if peerKey == "" {
		return
	}
	artifactName = sanitizeArtifactName(artifactName)
	if artifactName == "" {
		return
	}
	artifactID := CanonicalArtifactID("", artifactName, "")

	c.mu.Lock()
	defer c.mu.Unlock()
	state, ok := c.peerArtifacts[peerKey]
	if !ok {
		return
	}
	filtered := make([]P2PArtifactView, 0, len(state.Artifacts))
	for _, a := range state.Artifacts {
		if strings.EqualFold(strings.TrimSpace(a.ArtifactID), artifactID) {
			continue // remove este artifact
		}
		filtered = append(filtered, a)
	}
	if len(filtered) < len(state.Artifacts) {
		c.deps.Log(fmt.Sprintf("[p2p][cache] artifact %s removido do cache do peer %s (stale)", artifactName, peerAgentID))
	}
	state.Artifacts = filtered
	state.LastUpdatedUTC = time.Now().UTC()
	c.peerArtifacts[peerKey] = state
}

// FindArtifactPeersByReleaseID busca peers que anunciam um artifact
// com o artifactID (GUID de release) especificado e, opcionalmente,
// filtra por SHA256 para garantir integridade cross-peer.
// Se expectedSHA256 for vazia, retorna todos os peers com o artifactID.
func (c *Coordinator) FindArtifactPeersByReleaseID(artifactID string, expectedSHA256 string) P2PArtifactAvailabilityView {
	artifactID = strings.TrimSpace(artifactID)
	result := P2PArtifactAvailabilityView{
		ArtifactID:   artifactID,
		ArtifactName: "",
		PeerAgentIDs: []string{},
	}
	if artifactID == "" {
		return result
	}
	expectedSHA256 = strings.TrimSpace(expectedSHA256)

	for _, peer := range c.GetPeerArtifactIndex() {
		for _, artifact := range peer.Artifacts {
			if !strings.EqualFold(strings.TrimSpace(artifact.ArtifactID), artifactID) {
				continue
			}
			// Se esperadoSHA256 foi informado, só aceita peer com checksum correspondente
			if expectedSHA256 != "" && !strings.EqualFold(strings.TrimSpace(artifact.ChecksumSHA256), expectedSHA256) {
				continue
			}
			result.PeerAgentIDs = append(result.PeerAgentIDs, strings.TrimSpace(peer.PeerAgentID))
			break
		}
	}
	sort.Strings(result.PeerAgentIDs)
	result.PeerCount = len(result.PeerAgentIDs)
	result.Found = result.PeerCount > 0
	return result
}

func (c *Coordinator) ListAuditEvents() []P2PAuditEvent {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]P2PAuditEvent, len(c.audit))
	copy(out, c.audit)
	return out
}

func (c *Coordinator) ListAuditEventsFiltered(action, peerAgentID, status string) []P2PAuditEvent {
	action = strings.ToLower(strings.TrimSpace(action))
	peerAgentID = strings.ToLower(strings.TrimSpace(peerAgentID))
	status = strings.ToLower(strings.TrimSpace(status))

	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]P2PAuditEvent, 0, len(c.audit))
	for _, event := range c.audit {
		if action != "" && action != "all" && strings.ToLower(strings.TrimSpace(event.Action)) != action {
			continue
		}
		if peerAgentID != "" && peerAgentID != "all" && strings.ToLower(strings.TrimSpace(event.PeerAgentID)) != peerAgentID {
			continue
		}
		if status == "success" && !event.Success {
			continue
		}
		if (status == "error" || status == "failed") && event.Success {
			continue
		}
		out = append(out, event)
	}
	return out
}

func (c *Coordinator) GetArtifactAccess(artifactName, targetPeerID string) (P2PArtifactAccess, error) {
	c.mu.RLock()
	transfer := c.transferServer
	c.mu.RUnlock()
	if transfer == nil {
		return P2PArtifactAccess{}, fmt.Errorf("servidor de transferência indisponível")
	}
	return transfer.BuildArtifactAccess(artifactName, targetPeerID)
}

// GetAutoProvisioningStats retorna o snapshot dos campos de auto-provisioning.
func (c *Coordinator) GetAutoProvisioningStats() (total int64, events []P2POnboardingAuditEvent) {
	c.autoProvisionedMu.RLock()
	defer c.autoProvisionedMu.RUnlock()
	total = c.autoProvisionedCount
	events = make([]P2POnboardingAuditEvent, len(c.autoProvisionedAudit))
	copy(events, c.autoProvisionedAudit)
	return total, events
}

// SetLastCleanupUTC atualiza o timestamp da última limpeza de temp.
func (c *Coordinator) SetLastCleanupUTC(now time.Time) {
	c.mu.Lock()
	c.lastCleanupUTC = now.UTC()
	c.mu.Unlock()
}

// ResetSHA256Cache limpa o cache de SHA256 de artifacts locais.
func (c *Coordinator) ResetSHA256Cache() {
	c.sha256CacheMu.Lock()
	c.sha256Cache = make(map[string]artifactSHA256CacheEntry)
	c.sha256CacheMu.Unlock()
}
