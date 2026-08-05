package sync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"discovery/app/appstore"
	"discovery/app/core/agentconn"
	"discovery/app/core/selfupdate"
	"discovery/app/core/tlsutil"
	"discovery/app/netutil"
	"discovery/app/syncmeta"
)

// Coordinator orquestra a sincronização de recursos a partir do sync-manifest
// e de pings de invalidação recebidos via transporte.
type Coordinator struct {
	deps SyncDeps

	mu              sync.Mutex
	queue           chan syncmeta.SyncTrigger
	queuedByKey     map[string]syncmeta.SyncTrigger
	processedEvents map[string]time.Time
	revisions       map[string]string
	contentHashes   map[string]string
	pollEvery       time.Duration
}

// New cria um Coordinator com as dependências injetadas.
func New(deps SyncDeps) *Coordinator {
	c := &Coordinator{
		deps:            deps,
		queue:           make(chan syncmeta.SyncTrigger, 64),
		queuedByKey:     make(map[string]syncmeta.SyncTrigger),
		processedEvents: make(map[string]time.Time),
		revisions:       make(map[string]string),
		contentHashes:   make(map[string]string),
		pollEvery:       time.Duration(syncmeta.DefaultPollSeconds) * time.Second,
	}
	c.loadPersistedRevisions()
	return c
}

func syncResourceKey(resource, variant string) string {
	return strings.ToLower(strings.TrimSpace(resource)) + "|" + strings.ToLower(strings.TrimSpace(variant))
}

// Run inicia o loop de sincronização até o contexto ser cancelado.
func (c *Coordinator) Run(ctx context.Context) {
	c.deps.Log("[sync] coordinator iniciado")
	_ = c.ReconcileFromManifest(ctx, "startup")

	ticker := time.NewTicker(c.getPollEvery())
	cleanupTicker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	defer cleanupTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.deps.Log("[sync] coordinator finalizado")
			return
		case <-ticker.C:
			_ = c.ReconcileFromManifest(ctx, "poll")
			ticker.Reset(c.getPollEvery())
		case <-cleanupTicker.C:
			c.pruneProcessedEvents(time.Now())
		case trigger := <-c.queue:
			c.processTrigger(ctx, trigger)
		}
	}
}

func (c *Coordinator) pruneProcessedEvents(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, ts := range c.processedEvents {
		if now.Sub(ts) > syncmeta.ProcessedEventTTL {
			delete(c.processedEvents, k)
		}
	}
}

// HandlePing processa um ping de invalidação de sync recebido via transporte.
func (c *Coordinator) HandlePing(ping agentconn.SyncPing) {
	eventType := strings.TrimSpace(ping.EventType)
	resource := strings.TrimSpace(ping.Resource)
	eventID := strings.TrimSpace(ping.EventID)
	variant := strings.TrimSpace(ping.InstallationType)
	revision := strings.TrimSpace(ping.Revision)

	if eventType != "" && !strings.EqualFold(eventType, "sync.invalidated") {
		c.deps.Log("[sync] ping ignorado: eventType=" + eventType)
		return
	}
	if resource == "" {
		c.deps.Log("[sync] ping ignorado: resource vazio")
		return
	}
	c.deps.Log(fmt.Sprintf("[sync] ping recebido: eventId=%s resource=%s variant=%s revision=%s", eventID, resource, variant, revision))
	if c.isProcessedEvent(eventID) {
		c.deps.Log("[sync] ping ignorado: eventId duplicado=" + eventID)
		return
	}
	trigger := syncmeta.SyncTrigger{
		Resource: resource,
		Variant:  variant,
		Revision: revision,
		Source:   "ping",
	}
	c.enqueue(trigger)
}

func (c *Coordinator) isProcessedEvent(eventID string) bool {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return false
	}

	now := time.Now()
	c.mu.Lock()
	if _, ok := c.processedEvents[eventID]; ok {
		c.mu.Unlock()
		return true
	}
	c.processedEvents[eventID] = now
	c.mu.Unlock()
	return false
}

func (c *Coordinator) enqueue(trigger syncmeta.SyncTrigger) {
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
		c.deps.Log("[sync] enfileirado: " + key)
	default:
		c.mu.Lock()
		delete(c.queuedByKey, key)
		c.mu.Unlock()
		c.deps.Log("[sync] fila cheia, descartando: " + key)
	}
}

func (c *Coordinator) processTrigger(ctx context.Context, trigger syncmeta.SyncTrigger) {
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
	c.deps.RefreshPeerArtifactIndex(ctx, "sync-trigger")

	var err error
	var appStorePolicy appstore.EffectivePolicy
	switch resource {
	case "appstore":
		appStorePolicy, err = c.deps.LoadEffectiveAppStorePolicy(ctx, true)
	case "agentupdate":
		policy := selfupdate.NormalizePolicy(c.deps.GetAgentConfiguration().AgentUpdate)
		if !policy.Enabled || !policy.CheckOnSyncManifest {
			c.deps.Log("[sync] agentupdate ignorado: policy sem gatilho por sync-manifest")
			break
		}
		err = c.deps.RequestAgentUpdateCheck(ctx, "sync:"+strings.TrimSpace(queued.Source))
	case "automationpolicy":
		err = c.deps.RefreshAutomationPolicyError(false)
	case "configuration":
		err = c.deps.RefreshAgentConfiguration(ctx)
	case "knowledge":
		err = c.deps.RefreshKnowledgeBase()
	case "zerotouchapproved":
		err = c.fullResync(ctx, "zero-touch")
	default:
		err = c.fullResync(ctx, "fallback")
	}
	if err != nil {
		c.deps.Log("[sync] falha ao sincronizar " + queued.Resource + ": " + err.Error())
		return
	}

	currentRev := c.getRevision(queued.Resource, variant)
	if revision != "" && currentRev == revision {
		c.deps.Log("[sync] recurso sem alterações na revisão, ignorando: resource=" + queued.Resource + " revision=" + revision)
		c.cleanupProcessedTrigger(queued)
		return
	}

	if revision != "" {
		c.setRevision(queued.Resource, variant, revision)
	}

	if resource == "appstore" && c.deps.Context() != nil {
		contentHash := computeAppStoreContentHash(appStorePolicy.Items)
		prevHash := c.getContentHash(key)

		if contentHash != "" && prevHash == contentHash {
			c.deps.Log("[sync] appstore sem alterações no conteúdo, ignorando toast: resource=" + queued.Resource + " hash=" + contentHash[:12])
			c.cleanupProcessedTrigger(queued)
			return
		}

		if contentHash != "" {
			c.setContentHash(key, contentHash)
		}

		c.deps.EmitEvent("store:catalog-updated", map[string]any{
			"resource":  queued.Resource,
			"variant":   variant,
			"revision":  revision,
			"source":    queued.Source,
			"itemCount": len(appStorePolicy.Items),
			"updatedAt": time.Now().UTC().Format(time.RFC3339),
		})
	} else if resource == "appstore" {
		// ctx nulo (ex.: before startup completo) — apenas registra sem emitir evento
		c.deps.Log("[sync] appstore atualizada mas sem contexto de UI para emitir evento: resource=" + queued.Resource)
	}

	c.cleanupProcessedTrigger(queued)
}

func (c *Coordinator) fullResync(ctx context.Context, source string) error {
	c.deps.Log("[sync] full resync iniciado: source=" + source)

	var firstErr error
	if err := c.deps.RefreshAgentConfiguration(ctx); err != nil {
		firstErr = err
	}
	if err := c.deps.RefreshAutomationPolicyError(false); err != nil && firstErr == nil {
		firstErr = err
	}
	if _, err := c.deps.LoadEffectiveAppStorePolicy(ctx, true); err != nil && firstErr == nil {
		firstErr = err
	}

	c.deps.RefreshPeerArtifactIndex(ctx, "sync-full-resync")

	if firstErr == nil {
		c.deps.Log("[sync] full resync concluído: source=" + source)
	}
	return firstErr
}

// ReconcileFromManifest busca o sync-manifest e enfileira recursos alterados.
func (c *Coordinator) ReconcileFromManifest(ctx context.Context, source string) error {
	manifest, err := c.fetchSyncManifest(ctx)
	if err != nil {
		c.deps.Log("[sync] manifesto indisponível (" + source + "): " + err.Error())
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
		c.enqueue(syncmeta.SyncTrigger{
			Resource: strings.TrimSpace(resource.Resource),
			Variant:  strings.TrimSpace(resource.Variant),
			Revision: nextRevision,
			Source:   source,
		})
	}

	return nil
}

// SetPollEvery ajusta o intervalo de polling (usado por config de inventário).
func (c *Coordinator) SetPollEvery(value time.Duration) {
	c.setPollEvery(value)
}

func (c *Coordinator) getPollEvery() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pollEvery <= 0 {
		return time.Duration(syncmeta.DefaultPollSeconds) * time.Second
	}
	return c.pollEvery
}

func (c *Coordinator) setPollEvery(value time.Duration) {
	if value <= 0 {
		return
	}
	c.mu.Lock()
	c.pollEvery = value
	c.mu.Unlock()
}

func (c *Coordinator) getRevision(resource, variant string) string {
	key := syncResourceKey(resource, variant)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.revisions[key]
}

func (c *Coordinator) setRevision(resource, variant, revision string) {
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
	if db := c.deps.DB(); db != nil {
		_ = db.CacheSetJSON(syncmeta.SyncRevisionsCacheKey, persisted, 30*24*time.Hour)
	}
}

func (c *Coordinator) loadPersistedRevisions() {
	db := c.deps.DB()
	if db == nil {
		return
	}
	persisted := map[string]string{}
	found, err := db.CacheGetJSON(syncmeta.SyncRevisionsCacheKey, &persisted)
	if err != nil || !found {
		return
	}
	c.mu.Lock()
	for k, v := range persisted {
		c.revisions[k] = v
	}
	c.mu.Unlock()
}

func (c *Coordinator) getContentHash(key string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.contentHashes[key]
}

func (c *Coordinator) setContentHash(key, hash string) {
	c.mu.Lock()
	c.contentHashes[key] = hash
	c.mu.Unlock()
}

// cleanupProcessedTrigger remove o trigger da fila e notifica P2P.
func (c *Coordinator) cleanupProcessedTrigger(trigger syncmeta.SyncTrigger) {
	resource := strings.ToLower(strings.TrimSpace(trigger.Resource))
	variant := strings.TrimSpace(trigger.Variant)
	revision := strings.TrimSpace(trigger.Revision)
	c.deps.OnResourceSynced(resource, variant, revision)
	c.deps.Log("[sync] recurso sincronizado: " + resource + " variant=" + variant + " source=" + trigger.Source)
}

// fetchSyncManifest busca o sync-manifest do servidor.
func (c *Coordinator) fetchSyncManifest(ctx context.Context) (syncmeta.SyncManifestResponse, error) {
	cfg := c.deps.GetDebugConfig()
	apiScheme := strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
	apiServer := strings.TrimSpace(cfg.ApiServer)
	token := strings.TrimSpace(cfg.AuthToken)
	if apiServer == "" || token == "" {
		return syncmeta.SyncManifestResponse{}, fmt.Errorf("configuração de servidor API incompleta")
	}
	if apiScheme != "http" && apiScheme != "https" {
		return syncmeta.SyncManifestResponse{}, fmt.Errorf("apiScheme inválido")
	}

	target := apiScheme + "://" + apiServer + "/api/v1/agent-auth/me/sync-manifest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return syncmeta.SyncManifestResponse{}, err
	}
	req.Header.Set("Accept", "application/json")
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, token, cfg.AgentID); err != nil {
		return syncmeta.SyncManifestResponse{}, err
	}

	resp, err := tlsutil.NewHTTPClient(15 * time.Second).Do(req)
	if err != nil {
		return syncmeta.SyncManifestResponse{}, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return syncmeta.SyncManifestResponse{}, fmt.Errorf("sync-manifest retornou HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var manifest syncmeta.SyncManifestResponse
	if err := json.Unmarshal(body, &manifest); err != nil {
		return syncmeta.SyncManifestResponse{}, fmt.Errorf("resposta inválida do sync-manifest: %w", err)
	}
	if manifest.Resources == nil {
		manifest.Resources = []syncmeta.SyncManifestResource{}
	}
	return manifest, nil
}

// computeAppStoreContentHash calcula um hash determinístico do conteúdo real
// dos itens da app-store para detectar mudanças reais, independente da revision.
func computeAppStoreContentHash(items []appstore.Item) string {
	if len(items) == 0 {
		return ""
	}

	// Ordena os itens por chave composta para hash determinístico
	sorted := make([]appstore.Item, len(items))
	copy(sorted, items)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].PackageID < sorted[j].PackageID ||
			(sorted[i].PackageID == sorted[j].PackageID && sorted[i].InstallationType < sorted[j].InstallationType)
	})

	h := sha256.New()
	for _, item := range sorted {
		h.Write([]byte(item.PackageID))
		h.Write([]byte{0})
		h.Write([]byte(item.InstallationType))
		h.Write([]byte{0})
		h.Write([]byte(item.Name))
		h.Write([]byte{0})
		h.Write([]byte(item.Version))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
