// Package p2p encapsula o domínio de transferência P2P do agente.
package p2p

import "time"

// Constantes de configuração P2P.
const (
	DefaultP2PTempTTLHours          = 20 * 24
	DefaultP2PSeedPercent           = 10
	DefaultP2PMinSeeds              = 2
	DefaultP2PPortRangeStart        = 41080
	DefaultP2PPortRangeEnd          = 41120
	DefaultP2PTokenRotationMinutes  = 15
	CoordinatorDiscoveryTickSeconds = 30
	CoordinatorCleanupTickHours     = 1
	ReplicationWorkers              = 2
	ReplicationQueueSize            = 64
	PeerReplicationCooldown         = 20 * time.Second
	AuditLimit                      = 100
	ReplicationDedupTTL             = 24 * time.Hour
	LANProbeWarmupDelay             = 12 * time.Second
	LANProbeInterval                = 2 * time.Minute
	PeerArtifactCacheTTL            = 72 * time.Hour
	MaxPeerArtifactEntries          = 500

	// Chunking
	DefaultChunkSizeBytes = 8 * 1024 * 1024  // 8 MB
	MinChunkSizeBytes     = 1 * 1024 * 1024  // 1 MB
	MaxChunkSizeBytes     = 64 * 1024 * 1024 // 64 MB teto seguro (evita OOM com config malformada)
	MinParallelChunks     = 2                // piso do paralelismo adaptativo
	MaxParallelChunks     = 4                // teto padrão (pode ser elevado dinamicamente até 8)
	MaxChunkRetries       = 3                // tentativas por chunk antes de desistir
	ChunkRetryBaseDelay   = 1 * time.Second  // backoff inicial entre retries
)
