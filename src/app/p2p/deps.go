package p2p

import (
	"context"
	"time"

	"discovery/app/agentconfig"
	"discovery/app/core/agentconn"
	debug "discovery/app/debug"
	"discovery/app/p2pmeta"
	supportmeta "discovery/app/supportmeta"
)

// AppDeps expõe tudo que o domínio P2P usa do App (inversão de dependência).
// Permite que o Coordinator/TransferServer dependam de uma interface
// em vez do *App concreto, facilitando a migração para um pacote separado.
type AppDeps interface {
	// ── Configuração ──
	GetP2PConfig() p2pmeta.Config
	GetDebugConfig() debug.Config
	GetAgentConfiguration() agentconfig.AgentConfiguration

	// ── Diretório / limpeza ──
	P2PTempDir() string
	CleanupExpiredP2PTempArtifacts(now time.Time) (int, error)
	GetDataDir() string

	// ── Eventos / logging ──
	EmitEvent(name string, data ...any)
	Log(line string)

	// ── Métricas / identidade ──
	GetHeartbeatMetrics() agentconn.AgentHeartbeatMetrics
	GetAgentInfo() (supportmeta.AgentInfo, error)

	// ── Contexto / flags ──
	Context() context.Context
	DebugMode() bool

	// ── Onboarding / zero-touch ──
	RequestProvisioningToken(ctx context.Context) (deployKey, expiresAt string, err error)
	ApplyOnboardingOffer(offer p2pmeta.OnboardingRequest) (p2pmeta.OnboardingResult, error)
	TriggerZeroTouchConfigRegistrationOnPeerDiscovery(ctx context.Context, peer p2pmeta.DiscoveredPeer)

	// ── Configuração de instalação (onboarding HTTP) ──
	IsAgentConfigured() bool
	LoadInstallerConfig() (debug.InstallerConfig, string, error)
	BuildOnboardingOffer(sourceAgentID, serverURL, deployKey string, ttl time.Duration) (p2pmeta.OnboardingRequest, error)
}
