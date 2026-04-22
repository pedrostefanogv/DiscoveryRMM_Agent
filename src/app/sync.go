package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"discovery/app/netutil"
	"discovery/internal/agentconn"
	"discovery/internal/selfupdate"
)

const (
	syncRevisionsCacheKey = "sync_manifest_revisions"
	defaultPollSeconds    = 900
	processedEventTTL     = 30 * time.Minute
)

type SyncManifestResource struct {
	Resource                 string `json:"resource"`
	Variant                  string `json:"variant"`
	Revision                 string `json:"revision"`
	RecommendedSyncInSeconds int    `json:"recommendedSyncInSeconds"`
	Endpoint                 string `json:"endpoint"`
}

type SyncManifestResponse struct {
	GeneratedAtUTC         string                 `json:"generatedAtUtc"`
	RecommendedPollSeconds int                    `json:"recommendedPollSeconds"`
	MaxStaleSeconds        int                    `json:"maxStaleSeconds"`
	Resources              []SyncManifestResource `json:"resources"`
}

type syncTrigger struct {
	Resource string
	Variant  string
	Revision string
	Source   string
}

type syncCoordinator struct {
	app *App

	updateTrigger chan struct{}

	mu              sync.Mutex
	queue           chan syncTrigger
	queuedByKey     map[string]syncTrigger
	processedEvents map[string]time.Time
	revisions       map[string]string
	pollEvery       time.Duration
}

func newSyncCoordinator(app *App, updateTrigger chan struct{}) *syncCoordinator {
	c := &syncCoordinator{
		app:             app,
		updateTrigger:   updateTrigger,
		queue:           make(chan syncTrigger, 64),
		queuedByKey:     make(map[string]syncTrigger),
		processedEvents: make(map[string]time.Time),
		revisions:       make(map[string]string),
		pollEvery:       time.Duration(defaultPollSeconds) * time.Second,
	}
	c.loadPersistedRevisions()
	return c
}

func syncResourceKey(resource, variant string) string {
	return strings.ToLower(strings.TrimSpace(resource)) + "|" + strings.ToLower(strings.TrimSpace(variant))
}

func (c *syncCoordinator) Run(ctx context.Context) {
	c.app.logs.append("[sync] coordinator iniciado")
	_ = c.reconcileFromManifest(ctx, "startup")

	ticker := time.NewTicker(c.getPollEvery())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.app.logs.append("[sync] coordinator finalizado")
			return
		case <-ticker.C:
			_ = c.reconcileFromManifest(ctx, "poll")
			ticker.Reset(c.getPollEvery())
		case trigger := <-c.queue:
			c.processTrigger(ctx, trigger)
		}
	}
}

func (c *syncCoordinator) HandlePing(ping agentconn.SyncPing) {
	eventType := strings.TrimSpace(ping.EventType)
	if eventType != "" && !strings.EqualFold(eventType, "sync.invalidated") {
		c.app.logs.append("[sync] ping ignorado: eventType=" + eventType)
		return
	}
	if strings.TrimSpace(ping.Resource) == "" {
		c.app.logs.append("[sync] ping ignorado: resource vazio")
		return
	}
	c.app.logs.append(fmt.Sprintf("[sync] ping recebido: eventId=%s resource=%s variant=%s revision=%s", strings.TrimSpace(ping.EventID), strings.TrimSpace(ping.Resource), strings.TrimSpace(ping.InstallationType), strings.TrimSpace(ping.Revision)))
	if c.isProcessedEvent(ping.EventID) {
		c.app.logs.append("[sync] ping ignorado: eventId duplicado=" + strings.TrimSpace(ping.EventID))
		return
	}
	trigger := syncTrigger{
		Resource: strings.TrimSpace(ping.Resource),
		Variant:  strings.TrimSpace(ping.InstallationType),
		Revision: strings.TrimSpace(ping.Revision),
		Source:   "ping",
	}
	c.enqueue(trigger)
}

func (c *syncCoordinator) isProcessedEvent(eventID string) bool {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return false
	}

	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, ts := range c.processedEvents {
		if now.Sub(ts) > processedEventTTL {
			delete(c.processedEvents, k)
		}
	}
	if _, ok := c.processedEvents[eventID]; ok {
		return true
	}
	c.processedEvents[eventID] = now
	return false
}

func (c *syncCoordinator) enqueue(trigger syncTrigger) {
	key := syncResourceKey(trigger.Resource, trigger.Variant)
	if key == "|" {
		return
	}

	c.mu.Lock()
	if existing, ok := c.queuedByKey[key]; ok {
		if strings.TrimSpace(trigger.Revision) != "" {
			existing.Revision = trigger.Revision
		}
		existing.Source = trigger.Source
		c.queuedByKey[key] = existing
		c.mu.Unlock()
		return
	}
	c.queuedByKey[key] = trigger
	c.mu.Unlock()

	select {
	case c.queue <- trigger:
	default:
		go func() { c.queue <- trigger }()
	}
}

func (c *syncCoordinator) processTrigger(ctx context.Context, trigger syncTrigger) {
	key := syncResourceKey(trigger.Resource, trigger.Variant)
	c.mu.Lock()
	queued := c.queuedByKey[key]
	delete(c.queuedByKey, key)
	c.mu.Unlock()

	if strings.TrimSpace(queued.Resource) == "" {
		queued = trigger
	}
	resource := strings.ToLower(strings.TrimSpace(queued.Resource))
	variant := strings.TrimSpace(queued.Variant)
	revision := strings.TrimSpace(queued.Revision)
	if c.app.p2pCoord != nil {
		c.app.p2pCoord.RefreshPeerArtifactIndex(ctx, "sync-trigger")
	}

	var err error
	var appStorePolicy AppStoreEffectivePolicy
	switch resource {
	case "appstore":
		appStorePolicy, err = c.app.loadEffectiveAppStorePolicy(ctx, true)
	case "agentupdate":
		policy := selfupdate.NormalizePolicy(c.app.GetAgentConfiguration().AgentUpdate)
		if !policy.Enabled || !policy.CheckOnSyncManifest {
			c.app.logs.append("[sync] agentupdate ignorado: policy sem gatilho por sync-manifest")
			break
		}
		err = c.app.requestAgentUpdateCheck(ctx, "sync:"+strings.TrimSpace(queued.Source))
	case "automationpolicy":
		_, err = c.app.RefreshAutomationPolicy(false)
	case "configuration":
		err = c.app.refreshAgentConfiguration(ctx)
	case "zerotouchapproved":
		err = c.fullResync(ctx, "zero-touch")
	default:
		err = c.fullResync(ctx, "fallback")
	}
	if err != nil {
		c.app.logs.append("[sync] falha ao sincronizar " + queued.Resource + ": " + err.Error())
		return
	}

	if revision != "" {
		c.setRevision(queued.Resource, variant, revision)
	}
	if resource == "appstore" && c.app.ctx != nil {
		wailsRuntime.EventsEmit(c.app.ctx, "store:catalog-updated", map[string]any{
			"resource":  queued.Resource,
			"variant":   variant,
			"revision":  revision,
			"source":    queued.Source,
			"itemCount": len(appStorePolicy.Items),
			"updatedAt": time.Now().UTC().Format(time.RFC3339),
		})
	}
	if c.app.p2pCoord != nil {
		c.app.p2pCoord.OnResourceSynced(queued.Resource, variant, revision)
	}
	c.app.logs.append("[sync] recurso sincronizado: " + queued.Resource + " variant=" + variant + " source=" + queued.Source)
}

func (c *syncCoordinator) fullResync(ctx context.Context, source string) error {
	c.app.logs.append("[sync] full resync iniciado: source=" + source)

	var firstErr error
	if err := c.app.refreshAgentConfiguration(ctx); err != nil {
		firstErr = err
	}
	if _, err := c.app.RefreshAutomationPolicy(false); err != nil && firstErr == nil {
		firstErr = err
	}
	if _, err := c.app.loadEffectiveAppStorePolicy(ctx, true); err != nil && firstErr == nil {
		firstErr = err
	}
	if c.app.supportSvc != nil && c.app.featureEnabled(c.app.GetAgentConfiguration().KnowledgeBaseEnabled) {
		if err := c.app.supportSvc.RefreshKnowledgeBase(); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	if c.app.p2pCoord != nil {
		c.app.p2pCoord.RefreshPeerArtifactIndex(ctx, "sync-full-resync")
	}

	if firstErr == nil {
		c.app.logs.append("[sync] full resync concluído: source=" + source)
	}
	return firstErr
}

func (c *syncCoordinator) reconcileFromManifest(ctx context.Context, source string) error {
	manifest, err := c.app.fetchSyncManifest(ctx)
	if err != nil {
		c.app.logs.append("[sync] manifesto indisponivel (" + source + "): " + err.Error())
		return err
	}

	if manifest.RecommendedPollSeconds > 0 {
		c.setPollEvery(time.Duration(manifest.RecommendedPollSeconds) * time.Second)
	}

	for _, resource := range manifest.Resources {
		key := syncResourceKey(resource.Resource, resource.Variant)
		if key == "|" {
			continue
		}
		currentRevision := c.getRevision(resource.Resource, resource.Variant)
		nextRevision := strings.TrimSpace(resource.Revision)
		if nextRevision != "" && currentRevision == nextRevision {
			continue
		}
		c.enqueue(syncTrigger{
			Resource: strings.TrimSpace(resource.Resource),
			Variant:  strings.TrimSpace(resource.Variant),
			Revision: nextRevision,
			Source:   source,
		})
	}

	return nil
}

func (c *syncCoordinator) getPollEvery() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pollEvery <= 0 {
		return time.Duration(defaultPollSeconds) * time.Second
	}
	return c.pollEvery
}

func (c *syncCoordinator) setPollEvery(value time.Duration) {
	if value <= 0 {
		return
	}
	c.mu.Lock()
	c.pollEvery = value
	c.mu.Unlock()
}

func (c *syncCoordinator) getRevision(resource, variant string) string {
	key := syncResourceKey(resource, variant)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.revisions[key]
}

func (c *syncCoordinator) setRevision(resource, variant, revision string) {
	key := syncResourceKey(resource, variant)
	if key == "|" {
		return
	}
	c.mu.Lock()
	c.revisions[key] = strings.TrimSpace(revision)
	persisted := make(map[string]string, len(c.revisions))
	for k, v := range c.revisions {
		persisted[k] = v
	}
	c.mu.Unlock()
	if c.app.db != nil {
		_ = c.app.db.CacheSetJSON(syncRevisionsCacheKey, persisted, 30*24*time.Hour)
	}
}

func (c *syncCoordinator) loadPersistedRevisions() {
	if c.app.db == nil {
		return
	}
	persisted := map[string]string{}
	found, err := c.app.db.CacheGetJSON(syncRevisionsCacheKey, &persisted)
	if err != nil || !found {
		return
	}
	c.mu.Lock()
	for k, v := range persisted {
		c.revisions[k] = v
	}
	c.mu.Unlock()
}

func (a *App) fetchSyncManifest(ctx context.Context) (SyncManifestResponse, error) {
	cfg := a.GetDebugConfig()
	apiScheme := strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
	apiServer := strings.TrimSpace(cfg.ApiServer)
	token := strings.TrimSpace(cfg.AuthToken)
	if apiServer == "" || token == "" {
		return SyncManifestResponse{}, fmt.Errorf("configuração de servidor API incompleta")
	}
	if apiScheme != "http" && apiScheme != "https" {
		return SyncManifestResponse{}, fmt.Errorf("apiScheme inválido")
	}

	target := apiScheme + "://" + apiServer + "/api/agent-auth/me/sync-manifest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return SyncManifestResponse{}, err
	}
	req.Header.Set("Accept", "application/json")
	netutil.SetAgentAuthHeaders(req, token)
	if agentID := strings.TrimSpace(cfg.AgentID); agentID != "" {
		req.Header.Set("X-Agent-ID", agentID)
	}

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return SyncManifestResponse{}, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return SyncManifestResponse{}, fmt.Errorf("sync-manifest retornou HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var manifest SyncManifestResponse
	if err := json.Unmarshal(body, &manifest); err != nil {
		return SyncManifestResponse{}, fmt.Errorf("resposta inválida do sync-manifest: %w", err)
	}
	if manifest.Resources == nil {
		manifest.Resources = []SyncManifestResource{}
	}
	return manifest, nil
}

func (a *App) refreshAgentConfiguration(ctx context.Context) error {
	cfg := a.GetDebugConfig()
	apiScheme := strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
	apiServer := strings.TrimSpace(cfg.ApiServer)
	token := strings.TrimSpace(cfg.AuthToken)
	if apiServer == "" || token == "" {
		return fmt.Errorf("configuração de servidor API incompleta")
	}
	if apiScheme != "http" && apiScheme != "https" {
		return fmt.Errorf("apiScheme inválido")
	}

	target := apiScheme + "://" + apiServer + "/api/agent-auth/me/configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	netutil.SetAgentAuthHeaders(req, token)

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		// fallback to cached config when request fails
		_ = a.loadCachedAgentConfiguration()
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = a.loadCachedAgentConfiguration()
		return fmt.Errorf("configuration retornou HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	if a.db != nil {
		_ = a.db.CacheSet("agent_configuration_raw", body, 30*24*time.Hour)
	}

	cfgParsed, err := parseAgentConfiguration(body)
	if err != nil {
		a.logs.append("[sync] falha ao parsear configuration do agent: " + err.Error())
		return nil
	}
	a.setAgentConfiguration(cfgParsed)
	if a.debugSvc != nil {
		changed, applyErr := a.debugSvc.ApplyRemoteConnectionSecurity(
			cfgParsed.NatsServerHost,
			cfgParsed.NatsUseWssExternal,
			cfgParsed.EnforceTlsHashValidation,
			cfgParsed.HandshakeEnabled,
			cfgParsed.ApiTlsCertHash,
			cfgParsed.NatsTlsCertHash,
		)
		if applyErr != nil {
			a.logs.append("[sync] falha ao aplicar seguranca remota de transporte: " + applyErr.Error())
		} else if changed {
			a.logs.append("[sync] seguranca de transporte aplicada e reconexao solicitada")
		}
	}
	a.logs.append("[sync] configuração do agent atualizada")
	return nil
}
