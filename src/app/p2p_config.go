package app

import (
	"discovery/app/p2p"
)

// defaultInt returns def when v is not positive.
func defaultInt(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

// clampInt constrains v to [lo, hi].
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// defaultInt64 returns def when v is not positive.
func defaultInt64(v, def int64) int64 {
	if v <= 0 {
		return def
	}
	return v
}

// clampInt64 constrains v to [lo, hi].
func clampInt64(v, lo, hi int64) int64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func defaultP2PConfig() P2PConfig {
	return p2p.DefaultConfig()
}

func normalizeP2PConfig(cfg P2PConfig) P2PConfig {
	return p2p.NormalizeConfig(cfg)
}

func p2pSeedCount(totalAgents, seedPercent, minSeeds int) int {
	return p2p.SeedCount(totalAgents, seedPercent, minSeeds)
}

func buildP2PSeedPlan(totalAgents int, cfg P2PConfig) P2PSeedPlan {
	return p2p.BuildSeedPlan(totalAgents, cfg)
}
