package app

import (
	"math"
	"strings"
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
	return P2PConfig{
		Enabled:                  true,
		P2PMode:                  P2PModeLibp2pOnly,
		TempTTLHours:             defaultP2PTempTTLHours,
		SeedPercent:              defaultP2PSeedPercent,
		MinSeeds:                 defaultP2PMinSeeds,
		HTTPListenPortRangeStart: defaultP2PPortRangeStart,
		HTTPListenPortRangeEnd:   defaultP2PPortRangeEnd,
		AuthTokenRotationMinutes: defaultP2PTokenRotationMinutes,
	}
}

func normalizeP2PConfig(cfg P2PConfig) P2PConfig {
	out := cfg
	d := defaultP2PConfig()

	out.TempTTLHours = clampInt(defaultInt(out.TempTTLHours, d.TempTTLHours), 24, 24*30)
	out.SeedPercent = clampInt(defaultInt(out.SeedPercent, d.SeedPercent), 1, 100)
	out.MinSeeds = defaultInt(out.MinSeeds, d.MinSeeds)
	out.AuthTokenRotationMinutes = defaultInt(out.AuthTokenRotationMinutes, d.AuthTokenRotationMinutes)
	out.SharedSecret = strings.TrimSpace(out.SharedSecret)

	out.HTTPListenPortRangeStart = defaultInt(out.HTTPListenPortRangeStart, d.HTTPListenPortRangeStart)
	out.HTTPListenPortRangeEnd = defaultInt(out.HTTPListenPortRangeEnd, d.HTTPListenPortRangeEnd)
	if out.HTTPListenPortRangeStart > out.HTTPListenPortRangeEnd {
		out.HTTPListenPortRangeStart = d.HTTPListenPortRangeStart
		out.HTTPListenPortRangeEnd = d.HTTPListenPortRangeEnd
	}

	out.ChunkSizeBytes = clampInt64(defaultInt64(out.ChunkSizeBytes, defaultChunkSizeBytes), minChunkSizeBytes, math.MaxInt64)

	// MaxBandwidthBytesPerSec: < 0 → 0 (ilimitado); valor positivo abaixo do mínimo → eleva ao mínimo.
	if out.MaxBandwidthBytesPerSec < 0 {
		out.MaxBandwidthBytesPerSec = 0
	}
	const minBandwidthBytesPerSec = 64 * 1024
	if out.MaxBandwidthBytesPerSec > 0 && out.MaxBandwidthBytesPerSec < minBandwidthBytesPerSec {
		out.MaxBandwidthBytesPerSec = minBandwidthBytesPerSec
	}

	out.P2PMode = P2PModeLibp2pOnly
	return out
}

func p2pSeedCount(totalAgents, seedPercent, minSeeds int) int {
	if totalAgents <= 0 {
		return 0
	}
	if seedPercent < 0 {
		seedPercent = 0
	}
	if minSeeds < 1 {
		minSeeds = 1
	}
	byPercent := int(math.Ceil(float64(totalAgents) * float64(seedPercent) / 100.0))
	selected := byPercent
	if selected < minSeeds {
		selected = minSeeds
	}
	if selected > totalAgents {
		selected = totalAgents
	}
	return selected
}

func buildP2PSeedPlan(totalAgents int, cfg P2PConfig) P2PSeedPlan {
	cfg = normalizeP2PConfig(cfg)
	return P2PSeedPlan{
		TotalAgents:       totalAgents,
		ConfiguredPercent: cfg.SeedPercent,
		MinSeeds:          cfg.MinSeeds,
		SelectedSeeds:     p2pSeedCount(totalAgents, cfg.SeedPercent, cfg.MinSeeds),
	}
}
