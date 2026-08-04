package app

import (
	"discovery/app/core/agentconn"
)

// handleP2PDiscoverySnapshot aplica um snapshot de descoberta P2P recebido via NATS.
func (a *App) handleP2PDiscoverySnapshot(snapshot agentconn.P2PDiscoverySnapshot) {
	if a == nil || a.p2pCoord == nil {
		return
	}
	a.p2pCoord.ApplyP2PDiscoverySnapshot(snapshot)
}

// handleP2PEvent processa eventos peer.online recebidos via NATS para descoberta acelerada.
func (a *App) handleP2PEvent(event agentconn.PeerEventMessage) {
	if a == nil || a.p2pCoord == nil {
		return
	}
	a.p2pCoord.HandlePeerOnlineEvent(event)
}
