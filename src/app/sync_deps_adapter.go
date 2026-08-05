package app

import (
	"context"

	"discovery/app/appstore"
	"discovery/app/core/database"
	"discovery/app/p2pmeta"
	"discovery/app/sync"
)

// Compile-time check: *App implementa sync.SyncDeps.
var _ sync.SyncDeps = (*App)(nil)

// DB expõe a.db via interface.
func (a *App) DB() *database.DB {
	return a.db
}

// RefreshPeerArtifactIndex expõe p2pCoord.RefreshPeerArtifactIndex via interface.
func (a *App) RefreshPeerArtifactIndex(ctx context.Context, source string) {
	if a.p2pCoord != nil {
		a.p2pCoord.RefreshPeerArtifactIndex(ctx, source)
	}
}

// OnResourceSynced expõe p2pCoord.OnResourceSynced via interface.
func (a *App) OnResourceSynced(resource, variant, revision string) {
	if a.p2pCoord != nil {
		a.p2pCoord.OnResourceSynced(resource, variant, revision)
	}
}

// RefreshAutomationPolicyError expõe RefreshAutomationPolicy via interface,
// descartando a view (o coordinator só precisa do erro).
func (a *App) RefreshAutomationPolicyError(includeScriptContent bool) error {
	_, err := a.RefreshAutomationPolicy(includeScriptContent)
	return err
}

// LoadEffectiveAppStorePolicy expõe loadEffectiveAppStorePolicy via interface.
func (a *App) LoadEffectiveAppStorePolicy(ctx context.Context, forceRefresh bool) (appstore.EffectivePolicy, error) {
	return a.loadEffectiveAppStorePolicy(ctx, forceRefresh)
}

// RequestAgentUpdateCheck expõe requestAgentUpdateCheck via interface.
func (a *App) RequestAgentUpdateCheck(ctx context.Context, source string) error {
	return a.requestAgentUpdateCheck(ctx, source)
}

// RefreshAgentConfiguration expõe refreshAgentConfiguration via interface.
func (a *App) RefreshAgentConfiguration(ctx context.Context) error {
	return a.refreshAgentConfiguration(ctx)
}

// PostP2PTelemetryPayload expõe postP2PTelemetryPayload via interface.
func (a *App) PostP2PTelemetryPayload(ctx context.Context, payload p2pmeta.TelemetryPayload, idempotencyKey string) error {
	return a.postP2PTelemetryPayload(ctx, payload, idempotencyKey)
}
