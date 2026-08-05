package app

import (
	"discovery/app/p2pmeta"
)

type p2pSelfEndpoint = p2pmeta.SelfEndpoint

type p2pDiscoveredPeer = p2pmeta.DiscoveredPeer

// normalizeClientID normalizes a clientId for comparison.
func normalizeClientID(value string) string {
	return p2pmeta.NormalizeClientID(value)
}
