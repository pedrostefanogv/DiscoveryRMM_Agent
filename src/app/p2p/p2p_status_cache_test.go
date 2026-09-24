package p2p

import (
	"testing"
	"time"
)

// Regressão: hot paths de produção (automação/selfupdate) usavam
// GetPeerArtifactIndex, que faz fetch live de TODOS os peers. O índice em cache
// deve refletir o gossip sem tocar a rede.
func TestPeerArtifactIndexCachedReadsGossipCache(t *testing.T) {
	c := &Coordinator{
		peers: map[string]p2pPeerState{
			"peer-a": {Peer: p2pDiscoveredPeer{AgentID: "peer-a", Host: "10.0.0.2"}},
		},
		peerArtifacts: map[string]p2pPeerArtifactState{
			"peer-a": {
				Artifacts: []P2PArtifactView{{
					ArtifactID:   "winget:foxitfoxitreader",
					ArtifactName: "winget-foxitfoxitreader.exe",
				}},
				LastUpdatedUTC: time.Now().UTC(),
				Source:         "gossip",
			},
		},
	}
	idx := c.PeerArtifactIndexCached()
	if len(idx) != 1 {
		t.Fatalf("esperava 1 peer, obteve %d", len(idx))
	}
	if idx[0].PeerAgentID != "peer-a" || idx[0].PeerHost != "10.0.0.2" {
		t.Fatalf("peer inesperado: %+v", idx[0])
	}
	if len(idx[0].Artifacts) != 1 || idx[0].Artifacts[0].ArtifactID != "winget:foxitfoxitreader" {
		t.Fatalf("artifacts inesperados: %+v", idx[0].Artifacts)
	}
}
