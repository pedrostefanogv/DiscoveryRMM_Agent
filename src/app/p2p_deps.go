package app

import (
	"context"
	"time"

	"discovery/app/agentconfig"
	"discovery/app/core/agentconn"
)

// AppDeps expõe tudo que o domínio P2P usa do App (inversão de dependência).
// Permite que o p2pCoordinator/p2pTransferServer dependam de uma interface
// em vez do *App concreto, facilitando a migração para um pacote separado.
type AppDeps interface {
	// ── Configuração ──
	GetP2PConfig() P2PConfig
	GetDebugConfig() DebugConfig
	GetAgentConfiguration() agentconfig.AgentConfiguration

	// ── Diretório / limpeza ──
	P2PTempDir() string
	CleanupExpiredP2PTempArtifacts(now time.Time) (int, error)

	// ── Eventos / logging ──
	EmitEvent(name string, data ...any)
	Log(line string)

	// ── Métricas / identidade ──
	GetHeartbeatMetrics() agentconn.AgentHeartbeatMetrics
	GetAgentInfo() (AgentInfo, error)

	// ── Contexto / flags ──
	Context() context.Context
	DebugMode() bool

	// ── Onboarding / zero-touch ──
	RequestProvisioningToken(ctx context.Context) (deployKey, expiresAt string, err error)
	ApplyOnboardingOffer(offer P2POnboardingRequest) (P2POnboardingResult, error)
	TriggerZeroTouchConfigRegistrationOnPeerDiscovery(ctx context.Context, peer p2pDiscoveredPeer)
}
