package p2p

import (
	"context"

	"discovery/app/p2pmeta"
)

// Aliases de tipos P2P para p2pmeta. Mantêm a superfície pública estável.
type (
	BootstrapConfig           = p2pmeta.BootstrapConfig
	ChunkManifest             = p2pmeta.ChunkManifest
	Chunk                     = p2pmeta.Chunk
	SelfEndpoint              = p2pmeta.SelfEndpoint
	DiscoveredPeer            = p2pmeta.DiscoveredPeer
	SeedPlan                  = p2pmeta.SeedPlan
	SeedPlanRecommendation    = p2pmeta.SeedPlanRecommendation
	DebugStatus               = p2pmeta.DebugStatus
	PeerView                  = p2pmeta.PeerView
	PeerArtifactIndexView     = p2pmeta.PeerArtifactIndexView
	ArtifactAvailabilityView  = p2pmeta.ArtifactAvailabilityView
	ArtifactAccess            = p2pmeta.ArtifactAccess
	ArtifactView              = p2pmeta.ArtifactView
	Metrics                   = p2pmeta.Metrics
	TelemetryPayload          = p2pmeta.TelemetryPayload
	HostLoad                  = p2pmeta.HostLoad
	ArtifactPresenceItem      = p2pmeta.ArtifactPresenceItem
	DistributionStatus        = p2pmeta.DistributionStatus
	AuditEvent                = p2pmeta.AuditEvent
	OnboardingRequest         = p2pmeta.OnboardingRequest
	OnboardingResult          = p2pmeta.OnboardingResult
	OnboardingAuditEvent      = p2pmeta.OnboardingAuditEvent
	ProvisioningTokenResponse = p2pmeta.ProvisioningTokenResponse
	AutoProvisioningStats     = p2pmeta.AutoProvisioningStats
)

// Aliases com prefixo P2P mantidos para compatibilidade com o código migrado.
type (
	P2PConfig                    = p2pmeta.Config
	P2PBootstrapConfig           = p2pmeta.BootstrapConfig
	P2PChunkManifest             = p2pmeta.ChunkManifest
	P2PChunk                     = p2pmeta.Chunk
	P2PSelfEndpoint              = p2pmeta.SelfEndpoint
	P2PDiscoveredPeer            = p2pmeta.DiscoveredPeer
	P2PSeedPlan                  = p2pmeta.SeedPlan
	P2PSeedPlanRecommendation    = p2pmeta.SeedPlanRecommendation
	P2PDebugStatus               = p2pmeta.DebugStatus
	P2PPeerView                  = p2pmeta.PeerView
	P2PPeerArtifactIndexView     = p2pmeta.PeerArtifactIndexView
	P2PArtifactAvailabilityView  = p2pmeta.ArtifactAvailabilityView
	P2PArtifactAccess            = p2pmeta.ArtifactAccess
	P2PArtifactView              = p2pmeta.ArtifactView
	P2PMetrics                   = p2pmeta.Metrics
	P2PTelemetryPayload          = p2pmeta.TelemetryPayload
	P2PHostLoad                  = p2pmeta.HostLoad
	P2PArtifactPresenceItem      = p2pmeta.ArtifactPresenceItem
	P2PDistributionStatus        = p2pmeta.DistributionStatus
	P2PAuditEvent                = p2pmeta.AuditEvent
	P2POnboardingRequest         = p2pmeta.OnboardingRequest
	P2POnboardingResult          = p2pmeta.OnboardingResult
	P2POnboardingAuditEvent      = p2pmeta.OnboardingAuditEvent
	P2PProvisioningTokenResponse = p2pmeta.ProvisioningTokenResponse
	P2PAutoProvisioningStats     = p2pmeta.AutoProvisioningStats
)

// p2pSelfEndpoint é um alias para p2pmeta.SelfEndpoint.
type p2pSelfEndpoint = p2pmeta.SelfEndpoint

// p2pDiscoveredPeer é um alias para p2pmeta.DiscoveredPeer.
type p2pDiscoveredPeer = p2pmeta.DiscoveredPeer

// p2pDiscoveryProvider é a interface de descoberta de peers.
type p2pDiscoveryProvider interface {
	Name() string
	Start(ctx context.Context, self p2pSelfEndpoint, onPeer func(peer p2pDiscoveredPeer), onTrace func(message string)) error
}

// normalizeClientID normalizes a clientId for comparison.
func normalizeClientID(value string) string {
	return p2pmeta.NormalizeClientID(value)
}

// CanonicalArtifactID constrói o ID canônico de um artifact.
func CanonicalArtifactID(artifactID, artifactName, sourceURL string) string {
	return p2pmeta.CanonicalArtifactID(artifactID, artifactName, sourceURL)
}
