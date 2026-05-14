package app

import (
	"context"
	"strings"
)

type p2pSelfEndpoint struct {
	AgentID  string
	Host     string
	Port     int
	ClientID string
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
	ClientID     string
}

type p2pDiscoveryProvider interface {
	Name() string
	Start(ctx context.Context, self p2pSelfEndpoint, onPeer func(peer p2pDiscoveredPeer), onTrace func(message string)) error
}

// normalizeClientID normalizes a clientId for comparison.
func normalizeClientID(value string) string {
	return strings.TrimSpace(strings.ToLower(value))
}
