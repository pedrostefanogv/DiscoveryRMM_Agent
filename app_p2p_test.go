package main

import "testing"

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
