package app

import (
	"discovery/app/p2p"
)

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
