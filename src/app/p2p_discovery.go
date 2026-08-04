package app

import (
	"context"

	"discovery/app/p2pmeta"
)

type p2pSelfEndpoint = p2pmeta.SelfEndpoint

type p2pDiscoveredPeer = p2pmeta.DiscoveredPeer

type p2pDiscoveryProvider interface {
	Name() string
	Start(ctx context.Context, self p2pSelfEndpoint, onPeer func(peer p2pDiscoveredPeer), onTrace func(message string)) error
}

// normalizeClientID normalizes a clientId for comparison.
func normalizeClientID(value string) string {
	return p2pmeta.NormalizeClientID(value)
}
