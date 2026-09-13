package p2p

import (
	"math"
	"strings"

	"discovery/app/p2pmeta"
)

// Config é um alias para p2pmeta.Config.
type Config = p2pmeta.Config

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

// DefaultConfig retorna a configuração P2P padrão.
func DefaultConfig() Config {
	return Config{
		Enabled:                  true,
		P2PMode:                  p2pmeta.ModeLibp2pOnly,
		TempTTLHours:             DefaultP2PTempTTLHours,
		SeedPercent:              DefaultP2PSeedPercent,
		MinSeeds:                 DefaultP2PMinSeeds,
		HTTPListenPortRangeStart: DefaultP2PPortRangeStart,
		HTTPListenPortRangeEnd:   DefaultP2PPortRangeEnd,
		AuthTokenRotationMinutes: DefaultP2PTokenRotationMinutes,
		// TCPOnly default true (ponteiro nil = true via TCPOnlyEnabled()).
		// Não setar aqui: o default é aplicado pelo helper, e configs
		// persistidos sem o campo também devem herdar true.
	}
}

// NormalizeConfig normaliza a configuração P2P.
func NormalizeConfig(cfg Config) Config {
	out := cfg
	d := DefaultConfig()

	out.TempTTLHours = clampInt(defaultInt(out.TempTTLHours, d.TempTTLHours), 24, 24*30)
	out.SeedPercent = clampInt(defaultInt(out.SeedPercent, d.SeedPercent), 1, 100)
	out.MinSeeds = defaultInt(out.MinSeeds, d.MinSeeds)
	out.AuthTokenRotationMinutes = defaultInt(out.AuthTokenRotationMinutes, d.AuthTokenRotationMinutes)
	out.SharedSecret = strings.TrimSpace(out.SharedSecret)

	out.HTTPListenPortRangeStart = defaultInt(out.HTTPListenPortRangeStart, d.HTTPListenPortRangeStart)
	out.HTTPListenPortRangeEnd = defaultInt(out.HTTPListenPortRangeEnd, d.HTTPListenPortRangeEnd)
	out.ChunkSizeBytes = clampInt64(defaultInt64(out.ChunkSizeBytes, DefaultChunkSizeBytes), MinChunkSizeBytes, MaxChunkSizeBytes)
	out.MaxBandwidthBytesPerSec = clampBandwidthBytesPerSec(out.MaxBandwidthBytesPerSec)
	// M15: aplica o limite no bucket global (0 = sem limite). O throttle atua
	// no sender (bytes servidos) e no receiver (chunks baixados).
	ConfigureP2PBandwidth(out.MaxBandwidthBytesPerSec)
	out.MaxParallelChunks = clampInt(defaultInt(out.MaxParallelChunks, 0), 0, 16) // 0 = adaptativo

	if out.P2PMode == "" {
		out.P2PMode = p2pmeta.ModeLibp2pOnly
	}

	return out
}

// SeedCount calcula o número de seeds com base em percentual e mínimo.
func SeedCount(totalAgents, seedPercent, minSeeds int) int {
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

// BuildSeedPlan constrói o plano de seeds a partir da configuração.
func BuildSeedPlan(totalAgents int, cfg Config) p2pmeta.SeedPlan {
	cfg = NormalizeConfig(cfg)
	return p2pmeta.SeedPlan{
		TotalAgents:       totalAgents,
		ConfiguredPercent: cfg.SeedPercent,
		MinSeeds:          cfg.MinSeeds,
		SelectedSeeds:     SeedCount(totalAgents, cfg.SeedPercent, cfg.MinSeeds),
	}
}

// ── M15: limite de bandwidth configurável via servidor ─────────────────────

const (
	// bwStepBytes é a granularidade do limite (configurável de 10 em 10 MB/s).
	bwStepBytes = int64(10 << 20)
	// bwMinBytes/bwMaxBytes: piso e teto do limite configurável.
	bwMinBytes = bwStepBytes            // 10 MB/s
	bwMaxBytes = 100 * bwStepBytes      // 100 MB/s
)

// clampBandwidthBytesPerSec normaliza o limite informado pelo servidor:
//   <= 0 → 0 (sem limite, default);
//   > 0  → arredondado para BAIXO ao múltiplo de 10 MB/s mais próximo,
//          dentro do intervalo [10 MB/s, 100 MB/s] (infra ainda não gigabit).
func clampBandwidthBytesPerSec(v int64) int64 {
	if v <= 0 {
		return 0
	}
	if v < bwMinBytes {
		return bwMinBytes
	}
	if v > bwMaxBytes {
		return bwMaxBytes
	}
	return (v / bwStepBytes) * bwStepBytes
}
