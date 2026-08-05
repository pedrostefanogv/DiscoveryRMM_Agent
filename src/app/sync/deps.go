// Package sync contém o núcleo do domínio Sync/offline (coordinator de
// sincronização de manifest). Depende de uma interface SyncDeps (inversão de
// dependência) em vez do *App concreto, permitindo que o domínio seja
// testado isoladamente e migrado para um Service Wails3 sem ciclo de
// importação com o package app.
package sync

import (
	"context"

	"discovery/app/agentconfig"
	"discovery/app/appstore"
	"discovery/app/core/agentconn"
	"discovery/app/core/database"
	debug "discovery/app/debug"
	"discovery/app/p2pmeta"
)

// SyncDeps expõe tudo que o domínio Sync usa do App (inversão de dependência).
// O *App implementa esta interface via sync_deps_adapter.go na raiz.
type SyncDeps interface {
	// ── Logging / contexto / eventos ──
	Log(line string)
	Context() context.Context
	EmitEvent(name string, data ...any)

	// ── Configuração ──
	GetDebugConfig() debug.Config
	GetAgentConfiguration() agentconfig.AgentConfiguration

	// ── Persistência (cache de revisões) ──
	DB() *database.DB

	// ── P2P (coordinator) ──
	RefreshPeerArtifactIndex(ctx context.Context, source string)
	OnResourceSynced(resource, variant, revision string)

	// ── Ações de sincronização por recurso ──
	RefreshAutomationPolicyError(includeScriptContent bool) error
	LoadEffectiveAppStorePolicy(ctx context.Context, forceRefresh bool) (appstore.EffectivePolicy, error)
	RequestAgentUpdateCheck(ctx context.Context, source string) error
	RefreshKnowledgeBase() error
	RefreshAgentConfiguration(ctx context.Context) error

	// ── Envio de telemetria P2P (usado pelo outbox) ──
	PostP2PTelemetryPayload(ctx context.Context, payload p2pmeta.TelemetryPayload, idempotencyKey string) error
}

// Compile-time check: agentconn.SyncPing é usado pelo coordinator.
var _ = agentconn.SyncPing{}
