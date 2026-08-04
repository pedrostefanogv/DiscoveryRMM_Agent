package p2p

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"discovery/app/core/agentconn"
)

func (c *Coordinator) HandlePeerOnlineEvent(event agentconn.PeerEventMessage) {
	if c == nil || c.deps == nil {
		return
	}

	// 1. Filtrar apenas peer.online
	if strings.TrimSpace(event.EventType) != "peer.online" {
		return
	}

	// 2. clientId deve ser igual ao local
	localClientID := normalizeClientID(strings.TrimSpace(c.deps.GetAgentConfiguration().ClientID))
	eventClientID := normalizeClientID(strings.TrimSpace(event.ClientID))
	if localClientID != "" && eventClientID != "" && !strings.EqualFold(eventClientID, localClientID) {
		return
	}

	// 3. Ignorar self
	selfAgentID := strings.TrimSpace(c.deps.GetDebugConfig().AgentID)
	if strings.EqualFold(strings.TrimSpace(event.AgentID), selfAgentID) {
		return
	}

	// 4. Validar peerId, addrs, port
	if strings.TrimSpace(event.PeerID) == "" || event.Port <= 0 || !hasValidAddr(event.Addrs) {
		return
	}

	// 5. Dedupe por agentId (5s)
	dedupeKey := "peer-online:" + strings.ToLower(strings.TrimSpace(event.AgentID))
	c.mu.Lock()
	if last, ok := c.peerLastAttempt[dedupeKey]; ok && time.Since(last) < 5*time.Second {
		c.mu.Unlock()
		return
	}
	c.peerLastAttempt[dedupeKey] = time.Now()
	c.mu.Unlock()

	// 6. Tentar conectar via libp2p
	h, registry := c.libp2pHostAndRegistry()
	if h == nil || registry == nil {
		return
	}

	addrInfo, err := buildAddrInfo(strings.TrimSpace(event.PeerID), event.Addrs, event.Port)
	if err != nil {
		c.deps.Log(fmt.Sprintf("[p2p][events] peer.online ignorado (addr inválido) agentId=%s: %v",
			strings.TrimSpace(event.AgentID), err))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), p2pLibP2PHandshakeTimeout)
	err = h.Connect(ctx, addrInfo)
	cancel()
	if err != nil {
		c.deps.Log(fmt.Sprintf("[p2p][events] connect falhou agentId=%s peerId=%s: %v",
			strings.TrimSpace(event.AgentID), strings.TrimSpace(event.PeerID), err))
		return
	}

	c.deps.Log(fmt.Sprintf("[p2p][events] conectado via peer.online: agentId=%s peerId=%s",
		strings.TrimSpace(event.AgentID), strings.TrimSpace(event.PeerID)))
}

func (c *Coordinator) ApplyP2PDiscoverySnapshot(snapshot agentconn.P2PDiscoverySnapshot) {
	if c == nil || c.deps == nil {
		return
	}

	// Validar clientId: snapshot deve pertencer à mesma malha lógica.
	localClientID := normalizeClientID(strings.TrimSpace(c.deps.GetAgentConfiguration().ClientID))
	if localClientID != "" && !strings.EqualFold(normalizeClientID(strings.TrimSpace(snapshot.ClientID)), localClientID) {
		c.deps.Log(fmt.Sprintf("[p2p][nats-discovery] snapshot rejeitado por clientId divergente: snapshotClientId=%s localClientId=%s",
			strings.TrimSpace(snapshot.ClientID), localClientID))
		return
	}

	now := snapshot.ReceivedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	ttlSeconds := snapshot.TTLSeconds
	if ttlSeconds <= 0 {
		ttlSeconds = 120
	}

	c.mu.Lock()
	if snapshot.Sequence > 0 && snapshot.Sequence < c.lastP2PDiscoverySeq {
		c.mu.Unlock()
		c.deps.Log(fmt.Sprintf("[p2p][nats-discovery] snapshot ignorado: sequence=%d < ultimo=%d", snapshot.Sequence, c.lastP2PDiscoverySeq))
		return
	}
	if snapshot.Sequence > c.lastP2PDiscoverySeq {
		c.lastP2PDiscoverySeq = snapshot.Sequence
	}
	c.lastDiscoveryTick = now.UTC()
	c.mu.Unlock()

	selfAgentID := strings.TrimSpace(c.deps.GetDebugConfig().AgentID)
	newPeers := make([]p2pDiscoveredPeer, 0, len(snapshot.Peers))
	connectPeers := make([]agentconn.P2PDiscoveryPeer, 0, len(snapshot.Peers))
	knownPeers := len(snapshot.Peers)
	for _, peer := range snapshot.Peers {
		agentID := strings.TrimSpace(peer.AgentID)
		if agentID == "" || strings.EqualFold(agentID, selfAgentID) {
			continue
		}
		address := firstUsableSnapshotAddr(peer.Addrs)
		if address == "" || peer.Port <= 0 {
			continue
		}
		view := p2pDiscoveredPeer{
			AgentID:      agentID,
			ClientID:     strings.TrimSpace(snapshot.ClientID),
			Host:         address,
			Address:      address,
			Port:         peer.Port,
			Source:       "nats-discovery",
			KnownPeers:   knownPeers,
			ConnectedVia: "nats-discovery",
			TTLSeconds:   ttlSeconds,
		}
		inserted := c.upsertPeer(view)
		if inserted {
			newPeers = append(newPeers, view)
		}
		if strings.TrimSpace(peer.PeerID) != "" && len(peer.Addrs) > 0 {
			connectPeers = append(connectPeers, peer)
		}
	}

	for _, peer := range connectPeers {
		go c.connectP2PDiscoveryPeer(peer)
	}
	for _, peer := range newPeers {
		go c.refreshSinglePeer(context.Background(), peer)
	}

	c.deps.Log(fmt.Sprintf("[p2p][nats-discovery] snapshot aplicado: sequence=%d peers=%d ttl=%ds novos=%d",
		snapshot.Sequence, len(snapshot.Peers), ttlSeconds, len(newPeers)))
}

func firstUsableSnapshotAddr(addrs []string) string {
	for _, raw := range addrs {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		ip := net.ParseIP(trimmed)
		if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		return ip.String()
	}
	for _, raw := range addrs {
		trimmed := strings.TrimSpace(raw)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func (c *Coordinator) connectP2PDiscoveryPeer(peer agentconn.P2PDiscoveryPeer) {
	h, registry := c.libp2pHostAndRegistry()
	if h == nil || registry == nil {
		return
	}
	addrInfo, err := buildAddrInfo(strings.TrimSpace(peer.PeerID), peer.Addrs, peer.Port)
	if err != nil {
		c.deps.Log(fmt.Sprintf("[p2p][nats-discovery] peer ignorado (addr inválido) agentId=%s: %v", strings.TrimSpace(peer.AgentID), err))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), p2pLibP2PHandshakeTimeout)
	err = h.Connect(ctx, addrInfo)
	cancel()
	if err != nil {
		c.deps.Log(fmt.Sprintf("[p2p][nats-discovery] connect falhou agentId=%s peerId=%s: %v", strings.TrimSpace(peer.AgentID), strings.TrimSpace(peer.PeerID), err))
		return
	}
	if ok, existing, conflict := registry.RegisterStrict(strings.TrimSpace(peer.AgentID), addrInfo.ID); conflict {
		_ = h.Network().ClosePeer(addrInfo.ID)
		c.deps.Log(fmt.Sprintf("[p2p][nats-discovery] conflito de identidade agentId=%s peerAtual=%s peerRegistrado=%s", strings.TrimSpace(peer.AgentID), addrInfo.ID, existing))
		return
	} else if !ok {
		return
	}
	c.deps.Log(fmt.Sprintf("[p2p][nats-discovery] peer conectado via libp2p: agentId=%s peerId=%s", strings.TrimSpace(peer.AgentID), strings.TrimSpace(peer.PeerID)))
}
