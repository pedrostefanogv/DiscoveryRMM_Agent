package app

import (
	"fmt"
	"sort"
	"strings"
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
			Host:         peer.Peer.Host,
			Address:      peer.Peer.Address,
			Port:         peer.Peer.Port,
			Source:       peer.Peer.Source,
			LastSeenUTC:  formatTimeRFC3339(peer.LastSeenUTC),
			KnownPeers:   peer.Peer.KnownPeers,
			ConnectedVia: peer.Peer.ConnectedVia,
		})
	}
	return out
}

func (c *p2pCoordinator) GetPeerArtifactIndex() []P2PPeerArtifactIndexView {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]P2PPeerArtifactIndexView, 0, len(c.peerArtifacts))
	for key, state := range c.peerArtifacts {
		peerID := key
		if peer, ok := c.peers[key]; ok && strings.TrimSpace(peer.Peer.AgentID) != "" {
			peerID = strings.TrimSpace(peer.Peer.AgentID)
		}
		artifacts := make([]P2PArtifactView, len(state.Artifacts))
		copy(artifacts, state.Artifacts)
		out = append(out, P2PPeerArtifactIndexView{
			PeerAgentID:    peerID,
			LastUpdatedUTC: formatTimeRFC3339(state.LastUpdatedUTC),
			Source:         strings.TrimSpace(state.Source),
			Artifacts:      artifacts,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(strings.TrimSpace(out[i].PeerAgentID)) < strings.ToLower(strings.TrimSpace(out[j].PeerAgentID))
	})
	return out
}

// FindArtifactPeers returns a summary of which peers currently advertise the
// named artifact. Lookup uses canonical ArtifactID exclusively — name-based
// matching has been removed. Callers must ensure peers populate ArtifactID.
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

	for _, peer := range c.GetPeerArtifactIndex() {
		for _, artifact := range peer.Artifacts {
			if strings.EqualFold(strings.TrimSpace(artifact.ArtifactID), artifactID) {
				result.PeerAgentIDs = append(result.PeerAgentIDs, strings.TrimSpace(peer.PeerAgentID))
				break
			}
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
		return P2PArtifactAccess{}, fmt.Errorf("servidor de transferencia indisponivel")
	}
	return transfer.BuildArtifactAccess(artifactName, targetPeerID)
}
