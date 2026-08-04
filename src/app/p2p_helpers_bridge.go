package app

import (
	"net"

	"discovery/app/p2p"
)

// computeFileSHA256 delega para p2p.ComputeFileSHA256.
func computeFileSHA256(path string) (string, error) {
	return p2p.ComputeFileSHA256(path)
}

// listenInRange delega para p2p.ListenInRange.
func listenInRange(start, end int) (net.Listener, int, error) {
	return p2p.ListenInRange(start, end)
}

// detectLocalAddressForPeers delega para p2p.DetectLocalAddressForPeers.
func detectLocalAddressForPeers() string {
	return p2p.DetectLocalAddressForPeers()
}

// signReplicationControl delega para p2p.SignReplicationControl.
func signReplicationControl(secret []byte, sourceAgentID string, access P2PArtifactAccess, timestamp string) string {
	return p2p.SignReplicationControl(secret, sourceAgentID, access, timestamp)
}

// parsePortFromURL delega para p2p.ParsePortFromURL.
func parsePortFromURL(raw string) (int, error) {
	return p2p.ParsePortFromURL(raw)
}
