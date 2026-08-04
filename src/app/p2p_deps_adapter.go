package app

import (
	"context"
	"time"

	"discovery/app/core/agentconn"
)

// Compile-time check: *App implementa AppDeps.
var _ AppDeps = (*App)(nil)

// P2PTempDir expõe p2pTempDir via interface.
func (a *App) P2PTempDir() string {
	return a.p2pTempDir()
}

// CleanupExpiredP2PTempArtifacts expõe cleanupExpiredP2PTempArtifacts via interface.
func (a *App) CleanupExpiredP2PTempArtifacts(now time.Time) (int, error) {
	return a.cleanupExpiredP2PTempArtifacts(now)
}

// Log expõe logs.append via interface.
func (a *App) Log(line string) {
	a.logs.append(line)
}

// GetHeartbeatMetrics expõe getHeartbeatMetrics via interface.
func (a *App) GetHeartbeatMetrics() agentconn.AgentHeartbeatMetrics {
	return a.getHeartbeatMetrics()
}

// Context expõe ctx via interface.
func (a *App) Context() context.Context {
	return a.ctx
}

// DebugMode expõe runtimeFlags.DebugMode via interface.
func (a *App) DebugMode() bool {
	return a.runtimeFlags.DebugMode
}

// RequestProvisioningToken expõe requestProvisioningToken via interface.
func (a *App) RequestProvisioningToken(ctx context.Context) (deployKey, expiresAt string, err error) {
	return a.requestProvisioningToken(ctx)
}

// ApplyOnboardingOffer expõe applyOnboardingOffer via interface.
func (a *App) ApplyOnboardingOffer(offer P2POnboardingRequest) (P2POnboardingResult, error) {
	return a.applyOnboardingOffer(offer)
}

// TriggerZeroTouchConfigRegistrationOnPeerDiscovery expõe o método via interface.
func (a *App) TriggerZeroTouchConfigRegistrationOnPeerDiscovery(ctx context.Context, peer p2pDiscoveredPeer) {
	a.triggerZeroTouchConfigRegistrationOnPeerDiscovery(ctx, peer)
}
