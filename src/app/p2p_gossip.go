package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
)

// libp2pHostAndRegistry retorna o host e registry libp2p quando o provider
// ativo é libp2p (ou hybrid com libp2p). Retorna nil, nil nos demais modos.
func (c *p2pCoordinator) libp2pHostAndRegistry() (host.Host, *libp2pPeerRegistry) {
	c.mu.RLock()
	p := c.discoveryProvider
	c.mu.RUnlock()
	if p == nil {
		return nil, nil
	}
	if lp, ok := p.(*p2pLibP2PProvider); ok {
		return lp.h, lp.registry
	}
	return nil, nil
}

func (c *p2pCoordinator) pullPeerGossip(ctx context.Context) {
	c.RefreshPeerArtifactIndex(ctx, "gossip")
}

// refreshSinglePeer faz um fetch imediato de gossip em um único peer recém-descoberto.
// Requer libp2p stream para coletar o gossip.
func (c *p2pCoordinator) refreshSinglePeer(ctx context.Context, peer p2pDiscoveredPeer) {
	if strings.TrimSpace(peer.Address) == "" || peer.Port <= 0 {
		return
	}

	h, registry := c.libp2pHostAndRegistry()
	if h != nil && registry != nil {
		if peerID, ok := registry.Lookup(peer.AgentID); ok {
			if resp, err := libp2pFetchPeers(ctx, h, peerID); err == nil {
				c.applyGossipResponse(peer.AgentID, resp.KnownPeers, resp.Artifacts, "gossip-immediate")
				return
			}
		}
	}
}

// applyGossipResponse processa uma resposta de gossip (peers + artifacts) e atualiza o estado do coordinator.
func (c *p2pCoordinator) applyGossipResponse(sourceAgentID string, knownPeers []P2PPeerView, artifacts []P2PArtifactView, source string) {
	for _, p := range knownPeers {
		c.upsertPeer(p2pDiscoveredPeer{
			AgentID:      strings.TrimSpace(p.AgentID),
			Host:         strings.TrimSpace(p.Host),
			Address:      strings.TrimSpace(p.Address),
			Port:         p.Port,
			Source:       "gossip",
			KnownPeers:   p.KnownPeers,
			ConnectedVia: "gossip",
		})
	}
	if len(artifacts) > 0 {
		c.upsertPeerArtifacts(sourceAgentID, artifacts, source)
		c.app.logs.append(fmt.Sprintf("[p2p][gossip] peer=%s artifacts=%d transitivos=%d",
			strings.TrimSpace(sourceAgentID), len(artifacts), len(knownPeers)))
	}
}

func (c *p2pCoordinator) RefreshPeerArtifactIndex(ctx context.Context, source string) {
	peers := c.GetPeers()
	source = strings.TrimSpace(source)
	if source == "" {
		source = "refresh"
	}

	c.mu.Lock()
	c.metrics.CatalogRefreshRuns++
	c.mu.Unlock()

	h, registry := c.libp2pHostAndRegistry()

	for _, peer := range peers {
		if strings.TrimSpace(peer.Address) == "" || peer.Port <= 0 {
			continue
		}

		// Caminho libp2p: usar stream quando peer estiver no registry.
		if h != nil && registry != nil {
			if peerID, ok := registry.Lookup(peer.AgentID); ok {
				if resp, err := libp2pFetchPeers(ctx, h, peerID); err == nil {
					catalogSource := strings.TrimSpace(resp.CatalogSource)
					if catalogSource == "" {
						catalogSource = source
					}
					c.applyGossipResponse(peer.AgentID, resp.KnownPeers, resp.Artifacts, catalogSource)
					if len(resp.Artifacts) > 0 {
						c.app.logs.append(fmt.Sprintf("[p2p] catálogo via libp2p: peer=%s artifacts=%d source=%s",
							strings.TrimSpace(peer.AgentID), len(resp.Artifacts), catalogSource))
					}
					continue
				}
			}
		}
	}
}

func (c *p2pCoordinator) upsertPeerArtifacts(peerAgentID string, artifacts []P2PArtifactView, source string) {
	peerKey := strings.ToLower(strings.TrimSpace(peerAgentID))
	if peerKey == "" {
		return
	}
	if source == "" {
		source = "unknown"
	}
	clean := make([]P2PArtifactView, 0, len(artifacts))
	for _, artifact := range artifacts {
		name := sanitizeArtifactName(artifact.ArtifactName)
		if name == "" {
			continue
		}
		canonicalID := CanonicalArtifactID(artifact.ArtifactID, name, "")
		newChecksum := strings.TrimSpace(artifact.ChecksumSHA256)
		// Log mismatch vs previously known checksum for same artifactId (audit Epic 1).
		if canonicalID != "" && newChecksum != "" {
			c.mu.RLock()
			if prev, ok := c.peerArtifacts[peerKey]; ok {
				for _, pa := range prev.Artifacts {
					if strings.EqualFold(strings.TrimSpace(pa.ArtifactID), canonicalID) &&
						strings.TrimSpace(pa.ChecksumSHA256) != "" &&
						!strings.EqualFold(strings.TrimSpace(pa.ChecksumSHA256), newChecksum) {
						short := func(s string) string {
							if len(s) > 8 {
								return s[:8]
							}
							return s
						}
						c.app.logs.append(fmt.Sprintf("[p2p][audit] checksum divergente artifactId=%s peer=%s: anterior=%s... novo=%s...",
							canonicalID, peerAgentID, short(pa.ChecksumSHA256), short(newChecksum)))
					}
				}
			}
			c.mu.RUnlock()
		}
		heartbeat := strings.TrimSpace(artifact.LastHeartbeatUTC)
		if heartbeat == "" {
			heartbeat = formatTimeRFC3339(time.Now().UTC())
		}
		clean = append(clean, P2PArtifactView{
			ArtifactID:       canonicalID,
			ArtifactName:     name,
			Version:          strings.TrimSpace(artifact.Version),
			SizeBytes:        artifact.SizeBytes,
			ModifiedAtUTC:    strings.TrimSpace(artifact.ModifiedAtUTC),
			ChecksumSHA256:   newChecksum,
			Available:        true,
			LastHeartbeatUTC: heartbeat,
		})
	}
	sort.SliceStable(clean, func(i, j int) bool {
		return strings.ToLower(strings.TrimSpace(clean[i].ArtifactName)) < strings.ToLower(strings.TrimSpace(clean[j].ArtifactName))
	})

	c.mu.Lock()
	c.peerArtifacts[peerKey] = p2pPeerArtifactState{
		Artifacts:      clean,
		LastUpdatedUTC: time.Now().UTC(),
		Source:         source,
	}
	c.mu.Unlock()
}
