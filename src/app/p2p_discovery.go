package app

import "context"

type p2pSelfEndpoint struct {
	AgentID string
	Host    string
	Port    int
}

type p2pDiscoveredPeer struct {
	AgentID      string
	Host         string
	Address      string
	Port         int
	Source       string
	KnownPeers   int
	ConnectedVia string
	TTLSeconds   int
}

type p2pDiscoveryProvider interface {
	Name() string
	Start(ctx context.Context, self p2pSelfEndpoint, onPeer func(peer p2pDiscoveredPeer), onTrace func(message string)) error
}
