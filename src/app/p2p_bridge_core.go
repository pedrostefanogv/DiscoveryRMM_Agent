package app

import (
	"discovery/app/p2p"
)

// ── Bridge do núcleo P2P ─────────────────────────────────────────────────────
// Mantém os nomes originais (p2pCoordinator, newP2PCoordinator, p2pTransferServer)
// como aliases/wrappers para o pacote p2p, preservando as referências em app.go,
// p2p_bridge.go, sync.go, etc.

// p2pCoordinator é um alias para p2p.Coordinator.
type p2pCoordinator = p2p.Coordinator

// p2pTransferServer é um alias para p2p.TransferServer.
type p2pTransferServer = p2p.TransferServer

// newP2PCoordinator cria um Coordinator P2P.
func newP2PCoordinator(deps AppDeps) *p2pCoordinator {
	return p2p.NewCoordinator(deps)
}

// newP2PTransferServer cria um TransferServer P2P.
func newP2PTransferServer(deps AppDeps, coord *p2pCoordinator) *p2pTransferServer {
	return p2p.NewTransferServer(deps, coord)
}

// p2pCloudBootstrapTimeout é o timeout usado na chamada de cloud bootstrap.
const p2pCloudBootstrapTimeout = p2p.CloudBootstrapTimeout

// Constantes de configuração P2P (re-exportadas do pacote p2p).
const (
	defaultP2PTempTTLHours         = p2p.DefaultP2PTempTTLHours
	defaultP2PSeedPercent          = p2p.DefaultP2PSeedPercent
	defaultP2PMinSeeds             = p2p.DefaultP2PMinSeeds
	defaultP2PPortRangeStart       = p2p.DefaultP2PPortRangeStart
	defaultP2PPortRangeEnd         = p2p.DefaultP2PPortRangeEnd
	defaultP2PTokenRotationMinutes = p2p.DefaultP2PTokenRotationMinutes
)

// artifactSHA256CacheEntry é um alias para p2p.ArtifactSHA256CacheEntry.
type artifactSHA256CacheEntry = p2p.ArtifactSHA256CacheEntry

// sanitizeArtifactName delega para p2p.SanitizeArtifactName.
// DIFERE de p2pmeta.SanitizeArtifactName: retorna "" para "..", "/" e "\\"
// (em vez de substituí-los por "-").
func sanitizeArtifactName(name string) string {
	return p2p.SanitizeArtifactName(name)
}
