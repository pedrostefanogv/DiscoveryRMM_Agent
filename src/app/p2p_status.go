package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

func (c *p2pCoordinator) GetStatus() P2PDebugStatus {
	cfg := c.app.GetP2PConfig()
	c.mu.RLock()
	active := c.app.runtimeFlags.DebugMode && cfg.Enabled
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
		TempDir:              c.app.p2pTempDir(),
		TempTTLHours:         cfg.TempTTLHours,
		LastCleanupUTC:       formatTimeRFC3339(c.lastCleanupUTC),
		LastDiscoveryTickUTC: formatTimeRFC3339(lastDiscoveryTick),
		LastError:            c.lastErr,
		CurrentSeedPlan:      c.currentSeedPlan,
		Metrics:              c.metrics,
	}
}

func (c *p2pCoordinator) GetPeers() []P2PPeerView {
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
func (c *p2pCoordinator) GetPeerArtifactIndex() []P2PPeerArtifactIndexView {
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
// named artifact. Lookup uses cached data first; on cache miss or stale cache,
// falls back to live libp2p query to each known peer.
func (c *p2pCoordinator) FindArtifactPeers(artifactName string) P2PArtifactAvailabilityView {
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

	// Tenta cache primeiro.
	cacheHadEntry := false
	for _, peer := range c.GetPeerArtifactIndex() {
		for _, artifact := range peer.Artifacts {
			if strings.EqualFold(strings.TrimSpace(artifact.ArtifactID), artifactID) {
				result.PeerAgentIDs = append(result.PeerAgentIDs, strings.TrimSpace(peer.PeerAgentID))
				cacheHadEntry = true
				break
			}
		}
	}

	// Se o cache não tinha nenhum dado para este artifact (miss total),
	// faz query on-demand via libp2p para cada peer conhecido.
	if !cacheHadEntry {
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
	}

	sort.Strings(result.PeerAgentIDs)
	result.PeerCount = len(result.PeerAgentIDs)
	result.Found = result.PeerCount > 0
	return result
}

// InvalidatePeerArtifact remove um artifact específico do cache de um peer.
// Usado quando o cache diz que o peer tem o artifact mas o download falha
// (peer não tem mais o arquivo).
func (c *p2pCoordinator) InvalidatePeerArtifact(peerAgentID, artifactName string) {
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
		c.app.logs.append(fmt.Sprintf("[p2p][cache] artifact %s removido do cache do peer %s (stale)", artifactName, peerAgentID))
	}
	state.Artifacts = filtered
	state.LastUpdatedUTC = time.Now().UTC()
	c.peerArtifacts[peerKey] = state
}

// FindArtifactPeersByReleaseID busca peers que anunciam um artifact
// com o artifactID (GUID de release) especificado e, opcionalmente,
// filtra por SHA256 para garantir integridade cross-peer.
// Se expectedSHA256 for vazia, retorna todos os peers com o artifactID.
func (c *p2pCoordinator) FindArtifactPeersByReleaseID(artifactID string, expectedSHA256 string) P2PArtifactAvailabilityView {
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

func (c *p2pCoordinator) ListAuditEvents() []P2PAuditEvent {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]P2PAuditEvent, len(c.audit))
	copy(out, c.audit)
	return out
}

func (c *p2pCoordinator) ListAuditEventsFiltered(action, peerAgentID, status string) []P2PAuditEvent {
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

func (c *p2pCoordinator) GetArtifactAccess(artifactName, targetPeerID string) (P2PArtifactAccess, error) {
	c.mu.RLock()
	transfer := c.transferServer
	c.mu.RUnlock()
	if transfer == nil {
		return P2PArtifactAccess{}, fmt.Errorf("servidor de transferência indisponível")
	}
	return transfer.BuildArtifactAccess(artifactName, targetPeerID)
}
