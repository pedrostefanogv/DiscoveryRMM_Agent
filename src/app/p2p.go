package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	defaultP2PTempTTLHours             = 20 * 24
	defaultP2PSeedPercent              = 10
	defaultP2PMinSeeds                 = 2
	defaultP2PPortRangeStart           = 41080
	defaultP2PPortRangeEnd             = 41120
	defaultP2PTokenRotationMinutes     = 15
	p2pCoordinatorDiscoveryTickSeconds = 30
	p2pCoordinatorCleanupTickHours     = 1
	p2pReplicationWorkers              = 2
	p2pReplicationQueueSize            = 64
	p2pPeerReplicationCooldown         = 20 * time.Second
	p2pAuditLimit                      = 100
	p2pReplicationDedupTTL             = 24 * time.Hour
	p2pLANProbeWarmupDelay             = 12 * time.Second
	p2pLANProbeInterval                = 2 * time.Minute
)

var errP2PDuplicateReplication = errors.New("artifact ja distribuido recentemente para este peer")

// artifactSHA256CacheEntry guarda o SHA256 de um arquivo local com a mtime
// usada para calcular, permitindo invalidação barata.
type artifactSHA256CacheEntry struct {
	sum   string
	mtime time.Time
}

type p2pCoordinator struct {
	app *App

	mu                sync.RWMutex
	peers             map[string]p2pPeerState
	peerArtifacts     map[string]p2pPeerArtifactState
	metrics           P2PMetrics
	audit             []P2PAuditEvent
	peerLastAttempt   map[string]time.Time
	replicationDedup  map[string]time.Time
	knownPeers        int
	lastCleanupUTC    time.Time
	lastDiscoveryTick time.Time
	lastErr           string
	currentSeedPlan   P2PSeedPlan
	listenAddress     string
	discoveryProvider p2pDiscoveryProvider
	transferServer    *p2pTransferServer
	replicationQueue  chan p2pReplicationJob

	// sha256Cache evita recalcular SHA256 de artifacts locais a cada gossip tick.
	// A entrada é invalidada quando a mtime do arquivo muda.
	sha256CacheMu sync.Mutex
	sha256Cache   map[string]artifactSHA256CacheEntry

	// autoProvisioning rastreia agentes que este peer provisionou como configurador.
	autoProvisionedMu    sync.RWMutex
	autoProvisionedCount int64
	autoProvisionedAudit []P2POnboardingAuditEvent
}

type p2pPeerState struct {
	Peer        p2pDiscoveredPeer
	LastSeenUTC time.Time
}

type p2pPeerArtifactState struct {
	Artifacts      []P2PArtifactView
	LastUpdatedUTC time.Time
	Source         string
}

type p2pReplicationJob struct {
	ArtifactName string
	Checksum     string
	TargetPeerID string
	Source       string
	Result       chan error
}

func newP2PCoordinator(app *App) *p2pCoordinator {
	return &p2pCoordinator{
		app:              app,
		peers:            make(map[string]p2pPeerState),
		peerArtifacts:    make(map[string]p2pPeerArtifactState),
		peerLastAttempt:  make(map[string]time.Time),
		replicationDedup: make(map[string]time.Time),
		transferServer:   newP2PTransferServer(app),
		replicationQueue: make(chan p2pReplicationJob, p2pReplicationQueueSize),
		sha256Cache:      make(map[string]artifactSHA256CacheEntry),
	}
}

// cachedFileSHA256 retorna o SHA256 do arquivo, usando cache invalidado por mtime.
func (c *p2pCoordinator) cachedFileSHA256(path string, mtime time.Time) (string, error) {
	c.sha256CacheMu.Lock()
	defer c.sha256CacheMu.Unlock()
	if entry, ok := c.sha256Cache[path]; ok && entry.mtime.Equal(mtime) {
		return entry.sum, nil
	}
	sum, err := computeFileSHA256(path)
	if err != nil {
		return "", err
	}
	c.sha256Cache[path] = artifactSHA256CacheEntry{sum: sum, mtime: mtime}
	return sum, nil
}

func (c *p2pCoordinator) Run(ctx context.Context) {
	if c.app == nil {
		return
	}

	cfg := c.app.GetP2PConfig()
	if !cfg.Enabled {
		c.app.logs.append("[p2p] coordinator inativo: p2p.enabled=false")
		return
	}

	c.app.logs.append("[p2p] coordinator iniciado")
	_ = c.touchP2PTempDir()
	if err := c.startTransferServer(ctx); err != nil {
		c.setLastError(err)
		c.app.logs.append("[p2p] erro ao iniciar servidor local: " + err.Error())
	}
	if err := c.startDiscovery(ctx); err != nil {
		c.setLastError(err)
		c.app.logs.append("[p2p] erro ao iniciar descoberta de peers: " + err.Error())
	}
	for workerIndex := 0; workerIndex < p2pReplicationWorkers; workerIndex++ {
		go c.replicationWorker(ctx)
	}
	_ = c.discoveryTick(time.Now())

	discoveryTicker := time.NewTicker(p2pCoordinatorDiscoveryTickSeconds * time.Second)
	cleanupTicker := time.NewTicker(p2pCoordinatorCleanupTickHours * time.Hour)
	gossipTicker := time.NewTicker(45 * time.Second)
	cloudBootstrapTicker := time.NewTicker(1 * time.Hour)
	lanProbeWarmupTimer := time.NewTimer(p2pLANProbeWarmupDelay)
	lanProbeTicker := time.NewTicker(p2pLANProbeInterval)
	defer discoveryTicker.Stop()
	defer cleanupTicker.Stop()
	defer gossipTicker.Stop()
	defer cloudBootstrapTicker.Stop()
	defer lanProbeWarmupTimer.Stop()
	defer lanProbeTicker.Stop()

	go func() {
		_, _ = c.runLANDiscoveryProbe(ctx, "startup")
	}()

	// Disparar cloud bootstrap imediatamente no startup (carrega cache + chama API).
	if cfg.BootstrapConfig.CloudBootstrapEnabled {
		go func() {
			_, _ = c.runCloudBootstrap(ctx)
		}()
	}

	for {
		select {
		case <-ctx.Done():
			c.app.logs.append("[p2p] coordinator finalizado")
			return
		case <-discoveryTicker.C:
			_ = c.discoveryTick(time.Now())
		case <-gossipTicker.C:
			c.pullPeerGossip(ctx)
		case <-cleanupTicker.C:
			if _, err := c.app.cleanupExpiredP2PTempArtifacts(time.Now()); err != nil {
				c.setLastError(err)
			}
		case <-lanProbeWarmupTimer.C:
			go func() {
				_, _ = c.runLANDiscoveryProbe(ctx, "warmup")
			}()
		case <-lanProbeTicker.C:
			go func() {
				_, _ = c.runLANDiscoveryProbe(ctx, "periodic")
			}()
		case <-cloudBootstrapTicker.C:
			if c.app.GetP2PConfig().BootstrapConfig.CloudBootstrapEnabled {
				go func() {
					_, _ = c.runCloudBootstrap(ctx)
				}()
			}
		}
	}
}

func (c *p2pCoordinator) OnResourceSynced(resource, variant, revision string) {
	cfg := c.app.GetP2PConfig()
	totalAgents := c.currentAgentsEstimate()
	plan := buildP2PSeedPlan(totalAgents, cfg)

	c.mu.Lock()
	c.currentSeedPlan = plan
	c.mu.Unlock()

	c.app.logs.append(fmt.Sprintf("[p2p] plano calculado apos sync resource=%s variant=%s revision=%s totalAgents=%d seeds=%d",
		resource, variant, revision, plan.TotalAgents, plan.SelectedSeeds))
	c.appendAudit("auto-distribute", "", "", "sync", true, "modo pull-only: distribuicao forçada desabilitada")
}

func (c *p2pCoordinator) currentAgentsEstimate() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	// total = peers conhecidos + este agente
	return c.knownPeers + 1
}

func (c *p2pCoordinator) discoveryTick(now time.Time) error {
	cfg := c.app.GetP2PConfig()
	if !cfg.Enabled {
		return nil
	}
	c.mu.Lock()
	for key, peer := range c.peers {
		if now.Sub(peer.LastSeenUTC) > 2*time.Minute {
			delete(c.peers, key)
			delete(c.peerArtifacts, key)
		}
	}
	c.knownPeers = len(c.peers)
	c.lastDiscoveryTick = now.UTC()
	if c.currentSeedPlan.TotalAgents == 0 {
		c.currentSeedPlan = buildP2PSeedPlan(1, cfg)
	} else {
		c.currentSeedPlan = buildP2PSeedPlan(c.knownPeers+1, cfg)
	}
	c.mu.Unlock()
	return nil
}

func (c *p2pCoordinator) setLastError(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	c.lastErr = err.Error()
	c.mu.Unlock()
}

func (c *p2pCoordinator) touchP2PTempDir() error {
	dir := c.app.p2pTempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		c.setLastError(err)
		return err
	}
	return nil
}

func (c *p2pCoordinator) startTransferServer(ctx context.Context) error {
	if c.transferServer == nil {
		return fmt.Errorf("servidor de transferencia nao inicializado")
	}
	cfg := c.app.GetP2PConfig()
	debugCfg := c.app.GetDebugConfig()
	if err := c.transferServer.Start(ctx, cfg, debugCfg.AgentID, c.app.p2pTempDir(), c.GetPeers); err != nil {
		return err
	}
	c.mu.Lock()
	c.listenAddress = c.transferServer.BaseURL()
	c.mu.Unlock()
	return nil
}

func (c *p2pCoordinator) startDiscovery(ctx context.Context) error {
	cfg := c.app.GetP2PConfig()
	provider := pickDiscoveryProvider(cfg, c, c.transferServer)

	selfHost, _ := os.Hostname()
	selfAgentID := strings.TrimSpace(c.app.GetDebugConfig().AgentID)
	baseURL := c.transferServer.BaseURL()
	port := 0
	if parsed, err := parsePortFromURL(baseURL); err == nil {
		port = parsed
	}

	if err := provider.Start(ctx, p2pSelfEndpoint{AgentID: selfAgentID, Host: selfHost, Port: port}, func(peer p2pDiscoveredPeer) {
		if c.upsertPeer(peer) {
			// Novo peer descoberto: busca imediata do catálogo e peers dele,
			// sem esperar o próximo tick do coordinador (propagação gossip).
			go c.refreshSinglePeer(ctx, peer)
		}
	}, func(message string) {
		if strings.TrimSpace(message) == "" {
			return
		}
		c.app.logs.append("[p2p][discovery] " + message)
	}); err != nil {
		return err
	}

	c.mu.Lock()
	c.discoveryProvider = provider
	c.lastDiscoveryTick = time.Now().UTC()
	c.mu.Unlock()
	c.app.logs.append("[p2p] descoberta iniciada via " + provider.Name())
	return nil
}

// upsertPeer inserts or updates a discovered peer. Returns true when the peer
// was not previously known (newly inserted), so callers can trigger an
// immediate gossip fetch for that peer.
func (c *p2pCoordinator) upsertPeer(peer p2pDiscoveredPeer) bool {
	if strings.TrimSpace(peer.AgentID) == "" {
		return false
	}
	if strings.TrimSpace(peer.Address) == "" || peer.Port <= 0 {
		return false
	}
	key := strings.ToLower(strings.TrimSpace(peer.AgentID))

	c.mu.Lock()
	previous, existed := c.peers[key]
	c.peers[key] = p2pPeerState{Peer: peer, LastSeenUTC: time.Now().UTC()}
	c.knownPeers = len(c.peers)
	c.mu.Unlock()

	if !existed {
		c.app.logs.append(fmt.Sprintf("[p2p] peer descoberto: agentId=%s source=%s addr=%s:%d", strings.TrimSpace(peer.AgentID), strings.TrimSpace(peer.Source), strings.TrimSpace(peer.Address), peer.Port))
		return true
	}

	if strings.TrimSpace(previous.Peer.Address) != strings.TrimSpace(peer.Address) || previous.Peer.Port != peer.Port || strings.TrimSpace(previous.Peer.Source) != strings.TrimSpace(peer.Source) {
		c.app.logs.append(fmt.Sprintf("[p2p] peer atualizado: agentId=%s source=%s addr=%s:%d", strings.TrimSpace(peer.AgentID), strings.TrimSpace(peer.Source), strings.TrimSpace(peer.Address), peer.Port))
	}
	return false
}

func (c *p2pCoordinator) findPeerByAgentID(agentID string) (P2PPeerView, error) {
	target := strings.ToLower(strings.TrimSpace(agentID))
	if target == "" {
		return P2PPeerView{}, fmt.Errorf("peer alvo nao informado")
	}
	for _, peer := range c.GetPeers() {
		if strings.ToLower(strings.TrimSpace(peer.AgentID)) == target {
			return peer, nil
		}
	}
	return P2PPeerView{}, fmt.Errorf("peer %s nao encontrado", agentID)
}
