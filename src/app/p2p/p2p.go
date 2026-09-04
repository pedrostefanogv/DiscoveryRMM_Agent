package p2p

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"discovery/app/core/platform"
	"discovery/app/p2pmeta"
)

const (
	defaultP2PTempTTLHours         = 20 * 24
	defaultP2PSeedPercent          = 10
	defaultP2PMinSeeds             = 2
	defaultP2PPortRangeStart       = 41080
	defaultP2PPortRangeEnd         = 41120
	defaultP2PTokenRotationMinutes = 15
	p2pReplicationWorkers          = 2
	p2pReplicationQueueSize        = 64
	p2pPeerReplicationCooldown     = 20 * time.Second
	p2pAuditLimit                  = 100
	p2pReplicationDedupTTL         = 24 * time.Hour
	p2pLANProbeWarmupDelay         = 12 * time.Second
	p2pLANProbeInterval            = 2 * time.Minute
	peerArtifactCacheTTL           = 72 * time.Hour // cache de artifacts por peer expira em 72h
	maxPeerArtifactEntries         = 500            // cap máximo de entries no mapa peerArtifacts
)

var errP2PDuplicateReplication = errors.New("artifact ja distribuido recentemente para este peer")

// artifactSHA256CacheEntry guarda o SHA256 de um arquivo local com a mtime
// usada para calcular, permitindo invalidação barata.
type artifactSHA256CacheEntry struct {
	sum   string
	mtime time.Time
}

// ArtifactSHA256CacheEntry é exposto para o package app.
type ArtifactSHA256CacheEntry = artifactSHA256CacheEntry

type Coordinator struct {
	deps AppDeps

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
	transferServer    *TransferServer
	replicationQueue  chan p2pReplicationJob

	// sha256Cache evita recalcular SHA256 de artifacts locais a cada gossip tick.
	// A entrada é invalidada quando a mtime do arquivo muda.
	sha256CacheMu sync.Mutex
	sha256Cache   map[string]artifactSHA256CacheEntry

	// autoProvisioning rastreia agentes que este peer provisionou como configurador.
	autoProvisionedMu    sync.RWMutex
	autoProvisionedCount int64
	autoProvisionedAudit []P2POnboardingAuditEvent
	lastP2PDiscoverySeq  uint64

	// fetchStates gerencia o estado de eleição de fetcher por artifact.
	fetchStates *fetchStateMap

	// manifestInFlight deduplica gerações concorrentes de manifest para o mesmo
	// artifact. Cada chave (artifactName) aponta para um chan struct{} que é
	// fechado quando a geração termina.
	manifestInFlight sync.Map

	// cpuSampler mede CPU via GetSystemTimes com estado próprio, isolado
	// do heartbeat. Usado para throttling adaptativo durante build de manifest.
	cpuSampler *platform.CPUSampler

	// servingSessions rastreia bytes acumulados servidos por artifact,
	// para que o progresso de upload seja relativo ao arquivo completo,
	// não a cada chunk individual. Chave: "artifactName|requesterID".
	servingSessionsMu sync.Mutex
	servingSessions   map[string]*servingSession

	// downloadLocks serializa downloads do mesmo artifact para evitar que
	// dois downloads concorrentes escrevam no mesmo partsDir e corrompam
	// os chunks. Chave: artifactName.
	downloadLocksMu sync.Mutex
	downloadLocks   map[string]*downloadLockEntry

	// ── Lifecycle (Fase 4.1) ──
	// ctx é o contexto de ciclo de vida, cancelado no Shutdown.
	ctx    context.Context
	cancel context.CancelFunc
	// started indica se Startup foi chamado com sucesso.
	started bool

	// ── Readiness (warmup) ──
	// readyCh é fechado quando o discovery inicial completa: a primeira rodada
	// de lan-probe (startup) termina, OU um peer é confirmado antes disso.
	// Consumidores (ex.: router de automação) esperam este sinal antes de
	// declarar "artifact não encontrado na rede P2P" — evita fallback
	// desnecessário para winget/internet enquanto o discovery ainda não viu
	// os peers da LAN.
	readyOnce sync.Once
	readyCh   chan struct{}
}

// ReadyCh retorna um canal fechado quando o discovery inicial do P2P
// (primeira rodada de lan-probe) foi concluído. Nunca retorna nil.
// Fechado imediatamente se o Coordinator nunca subiu (consumidores não
// devem bloquear quando P2P está desabilitado).
func (c *Coordinator) ReadyCh() <-chan struct{} {
	if c == nil {
		never := make(chan struct{})
		close(never)
		return never
	}
	c.mu.Lock()
	if c.readyCh == nil {
		c.readyCh = make(chan struct{})
	}
	ch := c.readyCh
	c.mu.Unlock()
	return ch
}

// IsReady informa se o discovery inicial já foi concluído.
func (c *Coordinator) IsReady() bool {
	select {
	case <-c.ReadyCh():
		return true
	default:
		return false
	}
}

// markReady fecha o canal de readiness exatamente uma vez (idempotente).
func (c *Coordinator) markReady() {
	c.mu.Lock()
	if c.readyCh == nil {
		c.readyCh = make(chan struct{})
	}
	ch := c.readyCh
	c.mu.Unlock()
	c.readyOnce.Do(func() { close(ch) })
}

// downloadLockEntry guarda o mutex de um artifact e o número de esperadores
// ativos, permitindo remover o lock do mapa com segurança quando o último
// esperador termina.
type downloadLockEntry struct {
	mu      *sync.Mutex
	waiters int
}

type servingSession struct {
	artifactName string
	requesterID  string
	totalSize    int64
	servedBytes  int64
	lastReported int64     // evita emitir progresso redundante (mesmo byte count)
	lastActive   time.Time // última atividade; usado para GC de sessões órfãs
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

func NewCoordinator(deps AppDeps) *Coordinator {
	c := &Coordinator{
		deps:             deps,
		peers:            make(map[string]p2pPeerState),
		peerArtifacts:    make(map[string]p2pPeerArtifactState),
		peerLastAttempt:  make(map[string]time.Time),
		replicationDedup: make(map[string]time.Time),
		replicationQueue: make(chan p2pReplicationJob, p2pReplicationQueueSize),
		sha256Cache:      make(map[string]artifactSHA256CacheEntry),
		fetchStates:      newFetchStateMap(),
		cpuSampler:       platform.NewCPUSampler(),
		servingSessions:  make(map[string]*servingSession),
		downloadLocks:    make(map[string]*downloadLockEntry),
	}
	c.transferServer = NewTransferServer(deps, c)
	return c
}

// Startup prepara o contexto de ciclo de vida do Coordinator P2P.
// É chamado pelo *App antes de iniciar o loop Run.
//
// NOTA: Este método NÃO implementa a interface ServiceStartup do Wails v3
// (que requer application.ServiceOptions) para evitar dependência do pacote
// p2p (domínio puro) no Wails. O *App chama este método explicitamente.
func (c *Coordinator) Startup(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if c.started {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	c.ctx = ctx
	c.cancel = cancel
	c.started = true
	log.Println("[p2p] Startup concluído")
	return nil
}

// Shutdown cancela o contexto de ciclo de vida do Coordinator e fecha
// explicitamente o discoveryProvider (libp2p host + mDNS) e o TransferServer
// (HTTP listener). É chamado pelo *App em seu shutdown.
func (c *Coordinator) Shutdown() error {
	if c == nil || !c.started {
		return nil
	}
	if c.cancel != nil {
		c.cancel()
	}
	// Cleanup síncrono do discoveryProvider (libp2p host + mDNS service).
	c.mu.Lock()
	provider := c.discoveryProvider
	c.discoveryProvider = nil
	c.mu.Unlock()
	if provider != nil {
		provider.Close()
	}
	// Cleanup síncrono do TransferServer (HTTP listener).
	if c.transferServer != nil {
		c.transferServer.Close()
	}
	c.started = false
	log.Println("[p2p] Shutdown concluído")
	return nil
}

// Ctx retorna o contexto de ciclo de vida do Coordinator.
// Retorna context.Background() se Startup ainda não foi chamado.
func (c *Coordinator) Ctx() context.Context {
	if c == nil || c.ctx == nil {
		return context.Background()
	}
	return c.ctx
}

// lockDownload serializa downloads do mesmo artifact. Retorna uma função de
// unlock que também remove o lock do mapa quando não há mais esperadores.
// Usa um contador de esperadores (em vez de TryLock) para detectar de forma
// confiável quando o lock pode ser removido sem janela de race.
func (c *Coordinator) lockDownload(artifactName string) func() {
	c.downloadLocksMu.Lock()
	entry, ok := c.downloadLocks[artifactName]
	if !ok {
		entry = &downloadLockEntry{mu: &sync.Mutex{}}
		c.downloadLocks[artifactName] = entry
	}
	entry.waiters++
	c.downloadLocksMu.Unlock()

	entry.mu.Lock()

	return func() {
		entry.mu.Unlock()
		c.downloadLocksMu.Lock()
		entry.waiters--
		// Remove do mapa apenas quando não há mais esperadores.
		if entry.waiters == 0 {
			delete(c.downloadLocks, artifactName)
		}
		c.downloadLocksMu.Unlock()
	}
}

// gcServingSessions remove sessões de upload órfãs (sem atividade recente).
// Ocorre quando um peer desconecta no meio de um stream sem encerrar a sessão.
func (c *Coordinator) gcServingSessions(now time.Time) {
	const servingSessionTTL = 10 * time.Minute
	c.servingSessionsMu.Lock()
	defer c.servingSessionsMu.Unlock()
	for key, sess := range c.servingSessions {
		if now.Sub(sess.lastActive) > servingSessionTTL {
			delete(c.servingSessions, key)
		}
	}
}

// cachedFileSHA256 retorna o SHA256 do arquivo, usando cache invalidado por mtime.
func (c *Coordinator) cachedFileSHA256(path string, mtime time.Time) (string, error) {
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

func (c *Coordinator) Run(ctx context.Context) {
	if c.deps == nil {
		return
	}

	// Usa o contexto de lifecycle se Startup foi chamado; senão, usa o ctx
	// passado como argumento (compat).
	runCtx := ctx
	if c.ctx != nil {
		runCtx = c.ctx
	}

	cfg := c.deps.GetP2PConfig()
	if !cfg.Enabled {
		c.deps.Log("[p2p] coordinator inativo: p2p.enabled=false")
		return
	}

	c.deps.Log("[p2p] coordinator iniciado")
	_ = c.touchP2PTempDir()
	if err := c.startTransferServer(runCtx); err != nil {
		c.setLastError(err)
		c.deps.Log("[p2p] erro ao iniciar servidor local: " + err.Error())
	}
	if err := c.startDiscovery(runCtx); err != nil {
		c.setLastError(err)
		c.deps.Log("[p2p] erro ao iniciar descoberta de peers: " + err.Error())
	}
	for workerIndex := 0; workerIndex < p2pReplicationWorkers; workerIndex++ {
		go c.replicationWorker(runCtx)
	}
	// Re-seed automático: mantém cobertura de artifacts em todos os peers.
	go c.startReseedLoop(runCtx)
	_ = c.discoveryTick(time.Now())

	discoveryTicker := time.NewTicker(CoordinatorDiscoveryTickSeconds * time.Second)
	cleanupTicker := time.NewTicker(CoordinatorCleanupTickHours * time.Hour)
	gossipTicker := time.NewTicker(45 * time.Second)
	fetchHeartbeatTicker := time.NewTicker(artifactFetchHeartbeatEvery)
	electionTicker := time.NewTicker(60 * time.Second)
	lanProbeWarmupTimer := time.NewTimer(p2pLANProbeWarmupDelay)
	lanProbeTicker := time.NewTicker(p2pLANProbeInterval)
	contentGCTicker := time.NewTicker(CoordinatorCleanupTickHours * time.Hour)
	defer discoveryTicker.Stop()
	defer cleanupTicker.Stop()
	defer gossipTicker.Stop()
	defer fetchHeartbeatTicker.Stop()
	defer electionTicker.Stop()
	defer lanProbeWarmupTimer.Stop()
	defer lanProbeTicker.Stop()
	defer contentGCTicker.Stop()

	go func() {
		_, _ = c.RunLANDiscoveryProbe(runCtx, "startup")
		// Discovery inicial concluído: libera consumidores que aguardam
		// readiness (ex.: router de automação antes do fallback winget).
		c.markReady()
	}()

	lanProbeSem := make(chan struct{}, 2)

	for {
		select {
		case <-runCtx.Done():
			c.deps.Log("[p2p] coordinator finalizado")
			return
		case <-discoveryTicker.C:
			_ = c.discoveryTick(time.Now())
		case <-gossipTicker.C:
			c.pullPeerGossip(runCtx)
		case <-fetchHeartbeatTicker.C:
			c.publishFetchHeartbeats(runCtx)
		case <-electionTicker.C:
			c.runPendingElections(runCtx)
		case <-cleanupTicker.C:
			if _, err := c.deps.CleanupExpiredP2PTempArtifacts(time.Now()); err != nil {
				c.setLastError(err)
			}
			c.gcServingSessions(time.Now())
		case <-contentGCTicker.C:
			c.CollectOrphanArtifacts()
		case <-lanProbeWarmupTimer.C:
			lanProbeSem <- struct{}{}
			go func() {
				defer func() { <-lanProbeSem }()
				_, _ = c.RunLANDiscoveryProbe(runCtx, "warmup")
			}()
		case <-lanProbeTicker.C:
			select {
			case lanProbeSem <- struct{}{}:
				go func() {
					defer func() { <-lanProbeSem }()
					_, _ = c.RunLANDiscoveryProbe(runCtx, "periodic")
				}()
			default:
			}
		}
	}
}

func (c *Coordinator) OnResourceSynced(resource, variant, revision string) {
	cfg := c.deps.GetP2PConfig()
	totalAgents := c.currentAgentsEstimate()
	plan := buildP2PSeedPlan(totalAgents, cfg)

	c.mu.Lock()
	c.currentSeedPlan = plan
	c.mu.Unlock()

	c.deps.Log(fmt.Sprintf("[p2p] plano calculado apos sync resource=%s variant=%s revision=%s totalAgents=%d seeds=%d",
		resource, variant, revision, plan.TotalAgents, plan.SelectedSeeds))
	c.appendAudit("auto-distribute", "", "", "sync", true, "modo pull-only: distribuicao forçada desabilitada")
}

func (c *Coordinator) currentAgentsEstimate() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	// total = peers conhecidos + este agente
	return c.knownPeers + 1
}

func (c *Coordinator) discoveryTick(now time.Time) error {
	cfg := c.deps.GetP2PConfig()
	if !cfg.Enabled {
		return nil
	}
	c.mu.Lock()
	for key, peer := range c.peers {
		expireAfter := 2 * time.Minute
		if peer.Peer.TTLSeconds > 0 {
			expireAfter = time.Duration(peer.Peer.TTLSeconds) * time.Second
		}
		if now.Sub(peer.LastSeenUTC) > expireAfter {
			delete(c.peers, key)
			delete(c.peerArtifacts, key)
		}
	}
	// Expurga cache de artifacts stale (72h TTL independente do TTL de peer).
	c.pruneStalePeerArtifactsLocked(now)
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

// pruneStalePeerArtifactsLocked remove entradas de peerArtifacts cujo TTL expirou
// ou quando o mapa excede o número máximo de entries (remove os mais antigos).
// Deve ser chamada com c.mu já adquirido.
func (c *Coordinator) pruneStalePeerArtifactsLocked(now time.Time) {
	// Remove entradas com TTL expirado.
	for key, state := range c.peerArtifacts {
		if now.Sub(state.LastUpdatedUTC) > peerArtifactCacheTTL {
			delete(c.peerArtifacts, key)
		}
	}
	// Se ainda exceder o cap, remove as N entradas mais antigas.
	if len(c.peerArtifacts) > maxPeerArtifactEntries {
		type entry struct {
			key     string
			updated time.Time
		}
		entries := make([]entry, 0, len(c.peerArtifacts))
		for key, state := range c.peerArtifacts {
			entries = append(entries, entry{key, state.LastUpdatedUTC})
		}
		sort.SliceStable(entries, func(i, j int) bool {
			return entries[i].updated.Before(entries[j].updated)
		})
		toRemove := len(c.peerArtifacts) - maxPeerArtifactEntries
		for i := 0; i < toRemove && i < len(entries); i++ {
			delete(c.peerArtifacts, entries[i].key)
		}
	}
}

func (c *Coordinator) setLastError(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	c.lastErr = err.Error()
	c.mu.Unlock()
}

func (c *Coordinator) touchP2PTempDir() error {
	dir := c.deps.P2PTempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		c.setLastError(err)
		return err
	}
	// Garante que os diretórios temporários tenham acesso irrestrito a todos
	// os usuários da máquina, pois arquivos podem ser escritos pelo serviço
	// (SYSTEM) e lidos pelo agente (usuário comum).
	_ = platform.EnsureWorldAccess(platform.TempDir())
	_ = platform.EnsureWorldAccess(dir)
	return nil
}

func (c *Coordinator) startTransferServer(ctx context.Context) error {
	if c.transferServer == nil {
		return fmt.Errorf("servidor de transferência não inicializado")
	}
	cfg := c.deps.GetP2PConfig()
	debugCfg := c.deps.GetDebugConfig()
	if err := c.transferServer.Start(ctx, cfg, debugCfg.AgentID, c.deps.P2PTempDir(), c.GetPeers); err != nil {
		return err
	}
	c.mu.Lock()
	c.listenAddress = c.transferServer.BaseURL()
	c.mu.Unlock()
	return nil
}

func (c *Coordinator) startDiscovery(ctx context.Context) error {
	cfg := c.deps.GetP2PConfig()
	provider := pickDiscoveryProvider(cfg, c, c.transferServer)

	selfHost, _ := os.Hostname()
	selfAgentID := strings.TrimSpace(c.deps.GetDebugConfig().AgentID)
	selfClientID := strings.TrimSpace(c.deps.GetAgentConfiguration().ClientID)
	baseURL := c.transferServer.BaseURL()
	port := 0
	if parsed, err := parsePortFromURL(baseURL); err == nil {
		port = parsed
	}

	if err := provider.Start(ctx, p2pSelfEndpoint{AgentID: selfAgentID, Host: selfHost, Port: port, ClientID: selfClientID}, func(peer p2pDiscoveredPeer) {
		if c.upsertPeer(peer) {
			// Novo peer descoberto: busca imediata do catálogo e peers dele,
			// sem esperar o próximo tick do coordinador (propagação gossip).
			go c.refreshSinglePeer(ctx, peer)
			if c.deps != nil {
				go c.deps.TriggerZeroTouchConfigRegistrationOnPeerDiscovery(ctx, peer)
			}
		}
	}, func(message string) {
		if strings.TrimSpace(message) == "" {
			return
		}
		c.deps.Log("[p2p][discovery] " + message)
	}); err != nil {
		return err
	}

	c.mu.Lock()
	c.discoveryProvider = provider
	c.lastDiscoveryTick = time.Now().UTC()
	c.mu.Unlock()
	c.deps.Log("[p2p] descoberta iniciada via " + provider.Name())
	return nil
}

// upsertPeer inserts or updates a discovered peer. Returns true when the peer
// was not previously known (newly inserted), so callers can trigger an
// immediate gossip fetch for that peer.
func (c *Coordinator) upsertPeer(peer p2pDiscoveredPeer) bool {
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
		c.deps.Log(fmt.Sprintf("[p2p] peer descoberto: agentId=%s source=%s addr=%s:%d", strings.TrimSpace(peer.AgentID), strings.TrimSpace(peer.Source), strings.TrimSpace(peer.Address), peer.Port))
		return true
	}

	if strings.TrimSpace(previous.Peer.Address) != strings.TrimSpace(peer.Address) || previous.Peer.Port != peer.Port || strings.TrimSpace(previous.Peer.Source) != strings.TrimSpace(peer.Source) {
		c.deps.Log(fmt.Sprintf("[p2p] peer atualizado: agentId=%s source=%s addr=%s:%d", strings.TrimSpace(peer.AgentID), strings.TrimSpace(peer.Source), strings.TrimSpace(peer.Address), peer.Port))
	}
	return false
}

func (c *Coordinator) findPeerByAgentID(agentID string) (P2PPeerView, error) {
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

// UpsertPeer insere ou atualiza um peer conhecido. Retorna true se o peer
// era novo (não existia antes). Exportado para uso externo/testes.
func (c *Coordinator) UpsertPeer(peer p2pmeta.DiscoveredPeer) bool {
	return c.upsertPeer(peer)
}
