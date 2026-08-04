package p2pmeta

import "strings"

// SelfEndpoint representa o endpoint local do agente na descoberta P2P.
type SelfEndpoint struct {
	AgentID  string
	Host     string
	Port     int
	ClientID string
}

// DiscoveredPeer representa um peer descoberto na rede P2P.
type DiscoveredPeer struct {
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

// NormalizeClientID normalizes a clientId for comparison.
func NormalizeClientID(value string) string {
	return strings.TrimSpace(strings.ToLower(value))
}
