package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestP2PSeedCountRule(t *testing.T) {
	tests := []struct {
		name     string
		total    int
		percent  int
		minSeeds int
		expected int
	}{
		{name: "zero agents", total: 0, percent: 10, minSeeds: 2, expected: 0},
		{name: "one agent", total: 1, percent: 10, minSeeds: 2, expected: 1},
		{name: "two agents", total: 2, percent: 10, minSeeds: 2, expected: 2},
		{name: "ten agents", total: 10, percent: 10, minSeeds: 2, expected: 2},
		{name: "twenty five agents", total: 25, percent: 10, minSeeds: 2, expected: 3},
		{name: "fifty agents", total: 50, percent: 10, minSeeds: 2, expected: 5},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := p2pSeedCount(tc.total, tc.percent, tc.minSeeds); got != tc.expected {
				t.Fatalf("p2pSeedCount(%d, %d, %d) = %d, want %d", tc.total, tc.percent, tc.minSeeds, got, tc.expected)
			}
		})
	}
}

func TestNormalizeP2PConfigDefaults(t *testing.T) {
	cfg := normalizeP2PConfig(P2PConfig{})
	if cfg.DiscoveryMode != p2pDiscoveryMDNS {
		t.Fatalf("discovery mode = %q, want %q", cfg.DiscoveryMode, p2pDiscoveryMDNS)
	}
	if cfg.TempTTLHours != defaultP2PTempTTLHours {
		t.Fatalf("temp ttl = %d, want %d", cfg.TempTTLHours, defaultP2PTempTTLHours)
	}
	if cfg.SeedPercent != defaultP2PSeedPercent {
		t.Fatalf("seed percent = %d, want %d", cfg.SeedPercent, defaultP2PSeedPercent)
	}
	if cfg.MinSeeds != defaultP2PMinSeeds {
		t.Fatalf("min seeds = %d, want %d", cfg.MinSeeds, defaultP2PMinSeeds)
	}
}

func TestBuildP2PSeedPlan(t *testing.T) {
	cfg := P2PConfig{SeedPercent: 10, MinSeeds: 2, DiscoveryMode: p2pDiscoveryMDNS, TempTTLHours: 168}
	plan := buildP2PSeedPlan(25, cfg)
	if plan.SelectedSeeds != 3 {
		t.Fatalf("selected seeds = %d, want 3", plan.SelectedSeeds)
	}
	if plan.TotalAgents != 25 {
		t.Fatalf("total agents = %d, want 25", plan.TotalAgents)
	}
}

func TestListAuditEventsFiltered(t *testing.T) {
	c := &p2pCoordinator{}
	c.audit = []P2PAuditEvent{
		{TimestampUTC: time.Now().UTC().Format(time.RFC3339), Action: "replicate", PeerAgentID: "peer-a", Success: true, Message: "ok"},
		{TimestampUTC: time.Now().UTC().Format(time.RFC3339), Action: "queue", PeerAgentID: "peer-b", Success: true, Message: "queued"},
		{TimestampUTC: time.Now().UTC().Format(time.RFC3339), Action: "replicate", PeerAgentID: "peer-a", Success: false, Message: "error"},
	}

	failedReplicate := c.ListAuditEventsFiltered("replicate", "peer-a", "error")
	if len(failedReplicate) != 1 {
		t.Fatalf("expected 1 failed replicate event, got %d", len(failedReplicate))
	}
	if failedReplicate[0].Success {
		t.Fatalf("expected filtered event to be failure")
	}
}

func TestArtifactPriorityByResource(t *testing.T) {
	c := &p2pCoordinator{}
	high := c.artifactPriority("appstore", "stable", "appstore-catalog-v2.json")
	medium := c.artifactPriority("appstore", "stable", "agent-stable-package.bin")
	low := c.artifactPriority("appstore", "stable", "unrelated-backup.dat")

	if !(high < medium && medium < low) {
		t.Fatalf("unexpected priority order: high=%d medium=%d low=%d", high, medium, low)
	}
}

func TestFindArtifactPeersFromIndex(t *testing.T) {
	c := &p2pCoordinator{
		peers:         make(map[string]p2pPeerState),
		peerArtifacts: make(map[string]p2pPeerArtifactState),
	}
	now := time.Now().UTC()
	c.peers["peer-a"] = p2pPeerState{Peer: p2pDiscoveredPeer{AgentID: "peer-a"}, LastSeenUTC: now}
	c.peers["peer-b"] = p2pPeerState{Peer: p2pDiscoveredPeer{AgentID: "peer-b"}, LastSeenUTC: now}
	c.peerArtifacts["peer-a"] = p2pPeerArtifactState{
		Artifacts: []P2PArtifactView{{ArtifactName: "xyz.bin"}},
	}
	c.peerArtifacts["peer-b"] = p2pPeerArtifactState{
		Artifacts: []P2PArtifactView{{ArtifactName: "other.bin"}},
	}

	availability := c.FindArtifactPeers("xyz.bin")
	if !availability.Found {
		t.Fatal("expected artifact availability to be found")
	}
	if availability.PeerCount != 1 {
		t.Fatalf("expected 1 peer, got %d", availability.PeerCount)
	}
	if len(availability.PeerAgentIDs) != 1 || availability.PeerAgentIDs[0] != "peer-a" {
		t.Fatalf("unexpected peer list: %+v", availability.PeerAgentIDs)
	}
}

func TestResolveP2PTempDir(t *testing.T) {
	windowsPath := resolveP2PTempDir("windows")
	wantWindows := filepath.Join("C:\\", "Windows", "Temp", "Discovery", "P2P_Temp")
	if !strings.EqualFold(filepath.Clean(windowsPath), filepath.Clean(wantWindows)) {
		t.Fatalf("windows path = %q, want %q", windowsPath, wantWindows)
	}

	linuxPath := resolveP2PTempDir("linux")
	wantLinux := filepath.Join(getDataDir(), "TempP2P")
	if linuxPath != wantLinux {
		t.Fatalf("linux path = %q, want %q", linuxPath, wantLinux)
	}
}
