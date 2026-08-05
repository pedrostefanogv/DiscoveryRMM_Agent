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

// newP2PCoordinator cria um Coordinator P2P.
func newP2PCoordinator(deps AppDeps) *p2pCoordinator {
	return p2p.NewCoordinator(deps)
}

// p2pCloudBootstrapTimeout é o timeout usado na chamada de cloud bootstrap.
const p2pCloudBootstrapTimeout = p2p.CloudBootstrapTimeout

// sanitizeArtifactName delega para p2p.SanitizeArtifactName.
// DIFERE de p2pmeta.SanitizeArtifactName: retorna "" para "..", "/" e "\\"
// (em vez de substituí-los por "-").
func sanitizeArtifactName(name string) string {
	return p2p.SanitizeArtifactName(name)
}
