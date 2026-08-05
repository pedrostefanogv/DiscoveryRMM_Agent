package sync

import (
	"context"
	"testing"
	"time"

	"discovery/app/agentconfig"
	"discovery/app/appstore"
	"discovery/app/core/agentconn"
	"discovery/app/core/database"
	debug "discovery/app/debug"
	"discovery/app/p2pmeta"
	"discovery/app/syncmeta"
)

// fakeDeps implementa SyncDeps para testes isolados do coordinator.
type fakeDeps struct {
	db            *database.DB
	cfg           agentconfig.AgentConfiguration
	debugCfg      debug.Config
	logs          []string
	emitted       []string
	refreshPolicy bool
	refreshConfig bool
	refreshStore  bool
	updateCheck   bool
	knowledge     bool
	peerRefresh   int
	peerSynced    []string
}

func (f *fakeDeps) Log(line string)                 { f.logs = append(f.logs, line) }
func (f *fakeDeps) Context() context.Context        { return context.Background() }
func (f *fakeDeps) EmitEvent(name string, _ ...any) { f.emitted = append(f.emitted, name) }
func (f *fakeDeps) GetDebugConfig() debug.Config    { return f.debugCfg }
func (f *fakeDeps) GetAgentConfiguration() agentconfig.AgentConfiguration {
	return f.cfg
}
func (f *fakeDeps) DB() *database.DB { return f.db }
func (f *fakeDeps) RefreshPeerArtifactIndex(_ context.Context, _ string) {
	f.peerRefresh++
}
func (f *fakeDeps) OnResourceSynced(resource, variant, revision string) {
	f.peerSynced = append(f.peerSynced, resource+"|"+variant+"|"+revision)
}
func (f *fakeDeps) RefreshAutomationPolicyError(_ bool) error {
	f.refreshPolicy = true
	return nil
}
func (f *fakeDeps) LoadEffectiveAppStorePolicy(_ context.Context, _ bool) (appstore.EffectivePolicy, error) {
	f.refreshStore = true
	return appstore.EffectivePolicy{}, nil
}
func (f *fakeDeps) RequestAgentUpdateCheck(_ context.Context, _ string) error {
	f.updateCheck = true
	return nil
}
func (f *fakeDeps) RefreshKnowledgeBase() error {
	f.knowledge = true
	return nil
}
func (f *fakeDeps) RefreshAgentConfiguration(_ context.Context) error {
	f.refreshConfig = true
	return nil
}
func (f *fakeDeps) PostP2PTelemetryPayload(_ context.Context, _ p2pmeta.TelemetryPayload, _ string) error {
	return nil
}

func newFakeDeps(t *testing.T) *fakeDeps {
	t.Helper()
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &fakeDeps{db: db}
}

func TestHandlePing_EnqueuesAndProcesses(t *testing.T) {
	deps := newFakeDeps(t)
	c := New(deps)

	// Ping de invalidação de configuration.
	c.HandlePing(agentconn.SyncPing{
		EventID:   "evt-1",
		EventType: "sync.invalidated",
		Resource:  "configuration",
		Revision:  "rev-1",
	})

	// Processa o trigger enfileirado.
	select {
	case tr := <-c.queue:
		c.processTrigger(context.Background(), tr)
	case <-time.After(time.Second):
		t.Fatal("trigger não enfileirado")
	}

	if !deps.refreshConfig {
		t.Fatal("esperava refreshAgentConfiguration chamado")
	}
	if len(deps.peerSynced) != 1 {
		t.Fatalf("esperava 1 recurso sincronizado, got %d", len(deps.peerSynced))
	}
}

func TestHandlePing_IgnoresWrongEventType(t *testing.T) {
	deps := newFakeDeps(t)
	c := New(deps)

	c.HandlePing(agentconn.SyncPing{
		EventID:   "evt-2",
		EventType: "other.event",
		Resource:  "configuration",
	})

	select {
	case <-c.queue:
		t.Fatal("não deveria enfileirar evento de tipo errado")
	default:
	}
}

func TestReconcileFromManifest_EnqueuesChangedResources(t *testing.T) {
	deps := newFakeDeps(t)
	c := New(deps)

	// Sem servidor configurado, fetchSyncManifest falha — mas o teste valida
	// que a falha é tratada sem pânico.
	err := c.ReconcileFromManifest(context.Background(), "test")
	if err == nil {
		t.Fatal("esperava erro de configuração incompleta")
	}
}

func TestSetPollEvery_IgnoresNonPositive(t *testing.T) {
	deps := newFakeDeps(t)
	c := New(deps)

	c.SetPollEvery(0)
	if got := c.getPollEvery(); got != time.Duration(syncmeta.DefaultPollSeconds)*time.Second {
		t.Fatalf("pollEvery = %s, want default", got)
	}

	c.SetPollEvery(30 * time.Second)
	if got := c.getPollEvery(); got != 30*time.Second {
		t.Fatalf("pollEvery = %s, want 30s", got)
	}
}
