package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	p2pDiscoveryMDNS = "mdns"
	p2pDiscoveryUDP  = "udp"

	defaultP2PTempTTLHours             = 7 * 24
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
)

var errP2PDuplicateReplication = errors.New("artifact ja distribuido recentemente para este peer")

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
	}
}

func defaultP2PConfig() P2PConfig {
	return P2PConfig{
		Enabled:                  true,
		DiscoveryMode:            p2pDiscoveryMDNS,
		TempTTLHours:             defaultP2PTempTTLHours,
		SeedPercent:              defaultP2PSeedPercent,
		MinSeeds:                 defaultP2PMinSeeds,
		HTTPListenPortRangeStart: defaultP2PPortRangeStart,
		HTTPListenPortRangeEnd:   defaultP2PPortRangeEnd,
		AuthTokenRotationMinutes: defaultP2PTokenRotationMinutes,
	}
}

func normalizeP2PConfig(cfg P2PConfig) P2PConfig {
	out := cfg
	defaults := defaultP2PConfig()

	out.DiscoveryMode = strings.TrimSpace(strings.ToLower(out.DiscoveryMode))
	if out.DiscoveryMode != p2pDiscoveryMDNS && out.DiscoveryMode != p2pDiscoveryUDP {
		out.DiscoveryMode = defaults.DiscoveryMode
	}
	if out.TempTTLHours <= 0 {
		out.TempTTLHours = defaults.TempTTLHours
	}
	if out.TempTTLHours < 24 {
		out.TempTTLHours = 24
	}
	if out.TempTTLHours > 24*30 {
		out.TempTTLHours = 24 * 30
	}
	if out.SeedPercent <= 0 {
		out.SeedPercent = defaults.SeedPercent
	}
	if out.SeedPercent > 100 {
		out.SeedPercent = 100
	}
	if out.MinSeeds <= 0 {
		out.MinSeeds = defaults.MinSeeds
	}
	if out.HTTPListenPortRangeStart <= 0 {
		out.HTTPListenPortRangeStart = defaults.HTTPListenPortRangeStart
	}
	if out.HTTPListenPortRangeEnd <= 0 {
		out.HTTPListenPortRangeEnd = defaults.HTTPListenPortRangeEnd
	}
	if out.HTTPListenPortRangeStart > out.HTTPListenPortRangeEnd {
		out.HTTPListenPortRangeStart = defaults.HTTPListenPortRangeStart
		out.HTTPListenPortRangeEnd = defaults.HTTPListenPortRangeEnd
	}
	if out.AuthTokenRotationMinutes <= 0 {
		out.AuthTokenRotationMinutes = defaults.AuthTokenRotationMinutes
	}
	out.SharedSecret = strings.TrimSpace(out.SharedSecret)

	// Validate ChunkSizeBytes.
	if out.ChunkSizeBytes == 0 {
		out.ChunkSizeBytes = defaultChunkSizeBytes
	}
	if out.ChunkSizeBytes < minChunkSizeBytes {
		out.ChunkSizeBytes = minChunkSizeBytes
	}

	// Validate P2PMode.
	switch strings.TrimSpace(strings.ToLower(out.P2PMode)) {
	case P2PModeLegacy, P2PModeHybrid, P2PModeLibp2pOnly:
		out.P2PMode = strings.TrimSpace(strings.ToLower(out.P2PMode))
	default:
		out.P2PMode = P2PModeLegacy
	}

	return out
}

func p2pSeedCount(totalAgents, seedPercent, minSeeds int) int {
	if totalAgents <= 0 {
		return 0
	}
	if seedPercent < 0 {
		seedPercent = 0
	}
	if minSeeds < 1 {
		minSeeds = 1
	}
	byPercent := int(math.Ceil(float64(totalAgents) * float64(seedPercent) / 100.0))
	selected := byPercent
	if selected < minSeeds {
		selected = minSeeds
	}
	if selected > totalAgents {
		selected = totalAgents
	}
	return selected
}

func buildP2PSeedPlan(totalAgents int, cfg P2PConfig) P2PSeedPlan {
	cfg = normalizeP2PConfig(cfg)
	return P2PSeedPlan{
		TotalAgents:       totalAgents,
		ConfiguredPercent: cfg.SeedPercent,
		MinSeeds:          cfg.MinSeeds,
		SelectedSeeds:     p2pSeedCount(totalAgents, cfg.SeedPercent, cfg.MinSeeds),
	}
}

func (a *App) applyP2PConfig(cfg P2PConfig) {
	a.p2pMu.Lock()
	a.p2pConfig = normalizeP2PConfig(cfg)
	a.p2pMu.Unlock()
}

func (a *App) GetP2PConfig() P2PConfig {
	a.p2pMu.RLock()
	cfg := a.p2pConfig
	a.p2pMu.RUnlock()
	return normalizeP2PConfig(cfg)
}

func (a *App) SetP2PConfig(cfg P2PConfig) error {
	cfg = normalizeP2PConfig(cfg)
	a.applyP2PConfig(cfg)

	inst, path, err := loadInstallerConfig()
	if err != nil {
		inst = InstallerConfig{}
		path = ""
	}
	inst.P2P = cfg
	if _, err := persistInstallerConfig(path, inst); err != nil {
		a.logs.append("[p2p] falha ao persistir configuracao em config.json: " + err.Error())
		return err
	}

	a.logs.append(fmt.Sprintf("[p2p] configuracao atualizada: enabled=%t mode=%s ttlHours=%d seedPercent=%d minSeeds=%d",
		cfg.Enabled, cfg.DiscoveryMode, cfg.TempTTLHours, cfg.SeedPercent, cfg.MinSeeds))
	return nil
}

func (a *App) GetP2PDebugStatus() P2PDebugStatus {
	if a.p2pCoord == nil {
		return P2PDebugStatus{}
	}
	return a.p2pCoord.GetStatus()
}

func (a *App) GetP2PPeers() []P2PPeerView {
	if a.p2pCoord == nil {
		return []P2PPeerView{}
	}
	return a.p2pCoord.GetPeers()
}

func (a *App) RefreshP2PPeerCatalog() {
	if a.p2pCoord == nil {
		return
	}
	a.p2pCoord.RefreshPeerArtifactIndex(context.Background(), "manual")
}

func (a *App) GetP2PPeerArtifactIndex() []P2PPeerArtifactIndexView {
	if a.p2pCoord == nil {
		return []P2PPeerArtifactIndexView{}
	}
	return a.p2pCoord.GetPeerArtifactIndex()
}

func (a *App) FindP2PArtifactPeers(artifactName string) P2PArtifactAvailabilityView {
	if a.p2pCoord == nil {
		return P2PArtifactAvailabilityView{ArtifactName: sanitizeArtifactName(artifactName), PeerAgentIDs: []string{}}
	}
	return a.p2pCoord.FindArtifactPeers(artifactName)
}

func (a *App) GetP2PTempDir() string {
	return a.p2pTempDir()
}

func (a *App) CleanupP2PTempNow() (string, error) {
	removed, err := a.cleanupExpiredP2PTempArtifacts(time.Now())
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("limpeza concluida: %d item(ns) removido(s)", removed), nil
}

func (a *App) ComputeP2PSeedPlan(totalAgents int) P2PSeedPlan {
	cfg := a.GetP2PConfig()
	return buildP2PSeedPlan(totalAgents, cfg)
}

func (a *App) GetP2PArtifactAccess(artifactName, targetPeerID string) (P2PArtifactAccess, error) {
	if a.p2pCoord == nil {
		return P2PArtifactAccess{}, fmt.Errorf("coordinator P2P indisponivel")
	}
	return a.p2pCoord.GetArtifactAccess(artifactName, targetPeerID)
}

func (a *App) ListP2PArtifacts() ([]P2PArtifactView, error) {
	if a.p2pCoord == nil {
		return []P2PArtifactView{}, nil
	}
	return a.p2pCoord.ListArtifacts()
}

func (a *App) PublishP2PTestArtifact(artifactName, content string) (P2PArtifactView, error) {
	if a.p2pCoord == nil {
		return P2PArtifactView{}, fmt.Errorf("coordinator P2P indisponivel")
	}
	return a.p2pCoord.PublishTestArtifact(artifactName, content)
}

func (a *App) SelectAndPublishP2PArtifact() (P2PArtifactView, error) {
	if a.p2pCoord == nil {
		return P2PArtifactView{}, fmt.Errorf("coordinator P2P indisponivel")
	}
	if a.ctx == nil {
		return P2PArtifactView{}, fmt.Errorf("contexto de runtime indisponivel")
	}
	selectedPath, err := wailsRuntime.OpenFileDialog(a.ctx, wailsRuntime.OpenDialogOptions{})
	if err != nil {
		return P2PArtifactView{}, err
	}
	selectedPath = strings.TrimSpace(selectedPath)
	if selectedPath == "" {
		return P2PArtifactView{}, fmt.Errorf("selecao cancelada")
	}
	return a.p2pCoord.PublishFile(selectedPath)
}

func (a *App) ReplicateP2PArtifactToPeer(artifactName, targetPeerID string) (string, error) {
	return "", fmt.Errorf("modo push desabilitado: use transferencia pull sob demanda")
}

func (a *App) PullP2PArtifactFromPeer(artifactName, sourcePeerID string) (P2PArtifactView, error) {
	if a.p2pCoord == nil {
		return P2PArtifactView{}, fmt.Errorf("coordinator P2P indisponivel")
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return a.p2pCoord.DownloadArtifactFromPeer(ctx, artifactName, sourcePeerID)
}

// DownloadP2PArtifactSwarm finds all peers that have the artifact and performs
// a chunked swarm download when ≥2 peers are available; otherwise falls back
// to the single-peer path.
func (a *App) DownloadP2PArtifactSwarm(artifactName string) (P2PArtifactView, error) {
	if a.p2pCoord == nil {
		return P2PArtifactView{}, fmt.Errorf("coordinator P2P indisponivel")
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return a.p2pCoord.downloadArtifactSwarm(ctx, artifactName)
}

func (a *App) ListP2PAuditEvents() []P2PAuditEvent {
	if a.p2pCoord == nil {
		return []P2PAuditEvent{}
	}
	return a.p2pCoord.ListAuditEvents()
}

func (a *App) ListP2PAuditEventsFiltered(action, peerAgentID, status string) []P2PAuditEvent {
	if a.p2pCoord == nil {
		return []P2PAuditEvent{}
	}
	return a.p2pCoord.ListAuditEventsFiltered(action, peerAgentID, status)
}

func (c *p2pCoordinator) Run(ctx context.Context) {
	if c.app == nil {
		return
	}
	if !c.app.runtimeFlags.DebugMode {
		c.app.logs.append("[p2p] coordinator inativo: modo debug desabilitado")
		return
	}

	cfg := c.app.GetP2PConfig()
	if !cfg.Enabled {
		c.app.logs.append("[p2p] coordinator inativo: p2p.enabled=false")
		return
	}

	c.app.logs.append("[p2p] coordinator iniciado em modo debug")
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
	defer discoveryTicker.Stop()
	defer cleanupTicker.Stop()
	defer gossipTicker.Stop()

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

func (c *p2pCoordinator) GetStatus() P2PDebugStatus {
	cfg := c.app.GetP2PConfig()
	c.mu.RLock()
	active := c.app.runtimeFlags.DebugMode && cfg.Enabled
	listenAddress := c.listenAddress
	if strings.TrimSpace(listenAddress) == "" && c.transferServer != nil {
		listenAddress = c.transferServer.BaseURL()
	}
	lastDiscoveryTick := c.lastDiscoveryTick
	if lastDiscoveryTick.IsZero() && active {
		lastDiscoveryTick = time.Now().UTC()
	}
	defer c.mu.RUnlock()
	return P2PDebugStatus{
		Active:               active,
		DiscoveryMode:        cfg.DiscoveryMode,
		KnownPeers:           c.knownPeers,
		ListenAddress:        listenAddress,
		TempDir:              c.app.p2pTempDir(),
		TempTTLHours:         cfg.TempTTLHours,
		LastCleanupUTC:       formatTimeRFC3339(c.lastCleanupUTC),
		LastDiscoveryTickUTC: formatTimeRFC3339(lastDiscoveryTick),
		LastError:            c.lastErr,
		CurrentSeedPlan:      c.currentSeedPlan,
		Metrics:              c.metrics,
	}
}

func (c *p2pCoordinator) GetPeers() []P2PPeerView {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]P2PPeerView, 0, len(c.peers))
	for _, peer := range c.peers {
		out = append(out, P2PPeerView{
			AgentID:      peer.Peer.AgentID,
			Host:         peer.Peer.Host,
			Address:      peer.Peer.Address,
			Port:         peer.Peer.Port,
			Source:       peer.Peer.Source,
			LastSeenUTC:  formatTimeRFC3339(peer.LastSeenUTC),
			KnownPeers:   peer.Peer.KnownPeers,
			ConnectedVia: peer.Peer.ConnectedVia,
		})
	}
	return out
}

func (c *p2pCoordinator) GetPeerArtifactIndex() []P2PPeerArtifactIndexView {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]P2PPeerArtifactIndexView, 0, len(c.peerArtifacts))
	for key, state := range c.peerArtifacts {
		peerID := key
		if peer, ok := c.peers[key]; ok && strings.TrimSpace(peer.Peer.AgentID) != "" {
			peerID = strings.TrimSpace(peer.Peer.AgentID)
		}
		artifacts := make([]P2PArtifactView, len(state.Artifacts))
		copy(artifacts, state.Artifacts)
		out = append(out, P2PPeerArtifactIndexView{
			PeerAgentID:    peerID,
			LastUpdatedUTC: formatTimeRFC3339(state.LastUpdatedUTC),
			Source:         strings.TrimSpace(state.Source),
			Artifacts:      artifacts,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(strings.TrimSpace(out[i].PeerAgentID)) < strings.ToLower(strings.TrimSpace(out[j].PeerAgentID))
	})
	return out
}

func (c *p2pCoordinator) FindArtifactPeers(artifactName string) P2PArtifactAvailabilityView {
	safeArtifact := sanitizeArtifactName(artifactName)
	artifactID := CanonicalArtifactID("", safeArtifact, "")
	target := strings.ToLower(strings.TrimSpace(safeArtifact))
	result := P2PArtifactAvailabilityView{
		ArtifactID:   artifactID,
		ArtifactName: strings.TrimSpace(safeArtifact),
		PeerAgentIDs: []string{},
	}
	if target == "" {
		return result
	}

	for _, peer := range c.GetPeerArtifactIndex() {
		for _, artifact := range peer.Artifacts {
			if artifactID != "" && strings.EqualFold(strings.TrimSpace(artifact.ArtifactID), artifactID) {
				result.PeerAgentIDs = append(result.PeerAgentIDs, strings.TrimSpace(peer.PeerAgentID))
				break
			}
			if strings.ToLower(strings.TrimSpace(artifact.ArtifactName)) != target {
				continue
			}
			result.PeerAgentIDs = append(result.PeerAgentIDs, strings.TrimSpace(peer.PeerAgentID))
			break
		}
	}
	sort.Strings(result.PeerAgentIDs)
	result.PeerCount = len(result.PeerAgentIDs)
	result.Found = result.PeerCount > 0
	return result
}

func (c *p2pCoordinator) ListAuditEvents() []P2PAuditEvent {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]P2PAuditEvent, len(c.audit))
	copy(out, c.audit)
	return out
}

func (c *p2pCoordinator) ListAuditEventsFiltered(action, peerAgentID, status string) []P2PAuditEvent {
	action = strings.ToLower(strings.TrimSpace(action))
	peerAgentID = strings.ToLower(strings.TrimSpace(peerAgentID))
	status = strings.ToLower(strings.TrimSpace(status))

	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]P2PAuditEvent, 0, len(c.audit))
	for _, event := range c.audit {
		if action != "" && action != "all" && strings.ToLower(strings.TrimSpace(event.Action)) != action {
			continue
		}
		if peerAgentID != "" && peerAgentID != "all" && strings.ToLower(strings.TrimSpace(event.PeerAgentID)) != peerAgentID {
			continue
		}
		if status == "success" && !event.Success {
			continue
		}
		if (status == "error" || status == "failed") && event.Success {
			continue
		}
		out = append(out, event)
	}
	return out
}

func (c *p2pCoordinator) GetArtifactAccess(artifactName, targetPeerID string) (P2PArtifactAccess, error) {
	c.mu.RLock()
	transfer := c.transferServer
	c.mu.RUnlock()
	if transfer == nil {
		return P2PArtifactAccess{}, fmt.Errorf("servidor de transferencia indisponivel")
	}
	return transfer.BuildArtifactAccess(artifactName, targetPeerID)
}

func (c *p2pCoordinator) DownloadArtifactFromPeer(ctx context.Context, artifactName, sourcePeerID string) (P2PArtifactView, error) {
	artifactName = sanitizeArtifactName(artifactName)
	if artifactName == "" {
		return P2PArtifactView{}, fmt.Errorf("artifact invalido")
	}
	peer, err := c.findPeerByAgentID(sourcePeerID)
	if err != nil {
		return P2PArtifactView{}, err
	}

	requesterID := strings.TrimSpace(c.app.GetDebugConfig().AgentID)
	if requesterID == "" {
		requesterID = "peer-local"
	}

	endpoint := fmt.Sprintf("http://%s:%d/p2p/artifact/access", strings.TrimSpace(peer.Address), peer.Port)
	payload, _ := json.Marshal(map[string]string{
		"artifactName": artifactName,
		"requesterId":  requesterID,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return P2PArtifactView{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return P2PArtifactView{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return P2PArtifactView{}, fmt.Errorf("falha ao obter acesso remoto HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var access P2PArtifactAccess
	if err := json.NewDecoder(resp.Body).Decode(&access); err != nil {
		return P2PArtifactView{}, err
	}
	c.mu.RLock()
	transfer := c.transferServer
	c.mu.RUnlock()
	if transfer == nil {
		return P2PArtifactView{}, fmt.Errorf("servidor de transferencia indisponivel")
	}

	path, size, err := transfer.downloadArtifact(access)
	if err != nil {
		return P2PArtifactView{}, err
	}
	c.recordBytesDownloaded(size)
	info, statErr := os.Stat(path)
	if statErr != nil {
		return P2PArtifactView{}, statErr
	}
	checksum, checksumErr := computeFileSHA256(path)
	if checksumErr != nil {
		return P2PArtifactView{}, checksumErr
	}
	c.appendAudit("pull", artifactName, sourcePeerID, "automation", true, "artifact baixado do peer")
	return P2PArtifactView{
		ArtifactID:       CanonicalArtifactID(access.ArtifactID, artifactName, ""),
		ArtifactName:     artifactName,
		Version:          "",
		SizeBytes:        info.Size(),
		ModifiedAtUTC:    formatTimeRFC3339(info.ModTime()),
		ChecksumSHA256:   checksum,
		Available:        true,
		LastHeartbeatUTC: formatTimeRFC3339(time.Now().UTC()),
	}, nil
}

// downloadArtifactSwarm finds all peers that claim to have the artifact and
// performs a chunked swarm download when ≥2 peers are available.
// Falls back to the single-peer path when fewer peers are found.
func (c *p2pCoordinator) downloadArtifactSwarm(ctx context.Context, artifactName string) (P2PArtifactView, error) {
	artifactName = sanitizeArtifactName(artifactName)
	if artifactName == "" {
		return P2PArtifactView{}, fmt.Errorf("artifact invalido")
	}

	avail := c.FindArtifactPeers(artifactName)
	if !avail.Found || len(avail.PeerAgentIDs) == 0 {
		return P2PArtifactView{}, fmt.Errorf("nenhum peer possui o artifact %q", artifactName)
	}

	// Collect access tokens from all available peers (best-effort).
	var accesses []P2PArtifactAccess
	requesterID := strings.TrimSpace(c.app.GetDebugConfig().AgentID)
	if requesterID == "" {
		requesterID = "peer-local"
	}

	for _, peerID := range avail.PeerAgentIDs {
		peerView, err := c.findPeerByAgentID(peerID)
		if err != nil {
			continue
		}
		endpoint := fmt.Sprintf("http://%s:%d/p2p/artifact/access",
			strings.TrimSpace(peerView.Address), peerView.Port)
		payload, _ := json.Marshal(map[string]string{
			"artifactName": artifactName,
			"requesterId":  requesterID,
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
			strings.NewReader(string(payload)))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
		if err != nil {
			continue
		}
		var acc P2PArtifactAccess
		decErr := json.NewDecoder(resp.Body).Decode(&acc)
		resp.Body.Close()
		if decErr == nil && strings.TrimSpace(acc.URL) != "" {
			accesses = append(accesses, acc)
		}
	}

	if len(accesses) == 0 {
		return P2PArtifactView{}, fmt.Errorf("nenhum peer retornou token de acesso para %q", artifactName)
	}

	// Single peer → existing download path (no manifest overhead).
	c.mu.RLock()
	transfer := c.transferServer
	c.mu.RUnlock()
	if transfer == nil {
		return P2PArtifactView{}, fmt.Errorf("servidor de transferencia indisponivel")
	}

	cfg := c.app.GetP2PConfig()

	if len(accesses) < 2 || cfg.ChunkSizeBytes == 0 {
		path, size, err := transfer.downloadArtifact(accesses[0])
		if err != nil {
			return P2PArtifactView{}, err
		}
		c.recordBytesDownloaded(size)
		return c.buildArtifactView(artifactName, accesses[0].ArtifactID, path)
	}

	// Multi-peer → fetch manifest from primary peer and download in chunks.
	primaryURL := strings.Replace(accesses[0].URL,
		"/p2p/artifact/"+strings.ReplaceAll(artifactName, " ", "%20"),
		"/p2p/artifact/"+strings.ReplaceAll(artifactName, " ", "%20")+"/manifest", 1)

	manifestReq, err := http.NewRequestWithContext(ctx, http.MethodGet, primaryURL, nil)
	if err != nil {
		return P2PArtifactView{}, err
	}
	manifestResp, err := (&http.Client{Timeout: 20 * time.Second}).Do(manifestReq)
	if err != nil {
		return P2PArtifactView{}, fmt.Errorf("manifest indisponivel: %w", err)
	}
	var manifest P2PChunkManifest
	decErr := json.NewDecoder(manifestResp.Body).Decode(&manifest)
	manifestResp.Body.Close()
	if decErr != nil {
		return P2PArtifactView{}, fmt.Errorf("manifest invalido: %w", decErr)
	}

	destDir := c.app.p2pTempDir()
	path, totalBytes, err := downloadChunked(ctx, accesses, manifest, destDir)
	if err != nil {
		c.appendAudit("swarm-pull", artifactName, "", "automation", false, err.Error())
		return P2PArtifactView{}, err
	}
	c.recordBytesDownloaded(totalBytes)
	c.recordChunkedDownload(manifest.TotalChunks)
	c.appendAudit("swarm-pull", artifactName, fmt.Sprintf("%d peers", len(accesses)),
		"automation", true, fmt.Sprintf("download em %d chunks de %d peers", manifest.TotalChunks, len(accesses)))

	artifactID := CanonicalArtifactID(manifest.ArtifactID, artifactName, "")
	return c.buildArtifactView(artifactName, artifactID, path)
}

// buildArtifactView reads stat + checksum for a file and returns a P2PArtifactView.
func (c *p2pCoordinator) buildArtifactView(artifactName, artifactID, path string) (P2PArtifactView, error) {
	info, err := os.Stat(path)
	if err != nil {
		return P2PArtifactView{}, err
	}
	checksum, err := computeFileSHA256(path)
	if err != nil {
		return P2PArtifactView{}, err
	}
	return P2PArtifactView{
		ArtifactID:       CanonicalArtifactID(artifactID, artifactName, ""),
		ArtifactName:     artifactName,
		SizeBytes:        info.Size(),
		ModifiedAtUTC:    formatTimeRFC3339(info.ModTime()),
		ChecksumSHA256:   checksum,
		Available:        true,
		LastHeartbeatUTC: formatTimeRFC3339(time.Now().UTC()),
	}, nil
}

func (c *p2pCoordinator) ListArtifacts() ([]P2PArtifactView, error) {
	dir := c.app.p2pTempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	artifacts := make([]P2PArtifactView, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := sanitizeArtifactName(entry.Name())
		if name == "" {
			continue
		}
		path := filepath.Join(dir, name)
		info, err := entry.Info()
		if err != nil {
			continue
		}
		checksum, err := computeFileSHA256(path)
		if err != nil {
			continue
		}
		artifacts = append(artifacts, P2PArtifactView{
			ArtifactID:       CanonicalArtifactID("", name, ""),
			ArtifactName:     name,
			Version:          "",
			SizeBytes:        info.Size(),
			ModifiedAtUTC:    formatTimeRFC3339(info.ModTime()),
			ChecksumSHA256:   checksum,
			Available:        true,
			LastHeartbeatUTC: formatTimeRFC3339(time.Now().UTC()),
		})
	}
	return artifacts, nil
}

func (c *p2pCoordinator) PublishTestArtifact(artifactName, content string) (P2PArtifactView, error) {
	artifactName = sanitizeArtifactName(artifactName)
	if artifactName == "" {
		return P2PArtifactView{}, fmt.Errorf("nome de artifact invalido")
	}
	dir := c.app.p2pTempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return P2PArtifactView{}, err
	}
	path := filepath.Join(dir, artifactName)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return P2PArtifactView{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return P2PArtifactView{}, err
	}
	checksum, err := computeFileSHA256(path)
	if err != nil {
		return P2PArtifactView{}, err
	}
	c.mu.Lock()
	c.metrics.PublishedArtifacts++
	c.mu.Unlock()
	return P2PArtifactView{
		ArtifactID:       CanonicalArtifactID("", artifactName, ""),
		ArtifactName:     artifactName,
		Version:          "",
		SizeBytes:        info.Size(),
		ModifiedAtUTC:    formatTimeRFC3339(info.ModTime()),
		ChecksumSHA256:   checksum,
		Available:        true,
		LastHeartbeatUTC: formatTimeRFC3339(time.Now().UTC()),
	}, nil
}

func (c *p2pCoordinator) PublishFile(sourcePath string) (P2PArtifactView, error) {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return P2PArtifactView{}, fmt.Errorf("arquivo nao informado")
	}
	artifactName := sanitizeArtifactName(filepath.Base(sourcePath))
	if artifactName == "" {
		return P2PArtifactView{}, fmt.Errorf("nome de artifact invalido")
	}
	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		return P2PArtifactView{}, err
	}
	defer sourceFile.Close()

	dir := c.app.p2pTempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return P2PArtifactView{}, err
	}
	targetPath := filepath.Join(dir, artifactName)
	tmpPath := targetPath + ".importing"
	targetFile, err := os.Create(tmpPath)
	if err != nil {
		return P2PArtifactView{}, err
	}
	if _, err := io.Copy(targetFile, sourceFile); err != nil {
		targetFile.Close()
		_ = os.Remove(tmpPath)
		return P2PArtifactView{}, err
	}
	if err := targetFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return P2PArtifactView{}, err
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return P2PArtifactView{}, err
	}

	info, err := os.Stat(targetPath)
	if err != nil {
		return P2PArtifactView{}, err
	}
	checksum, err := computeFileSHA256(targetPath)
	if err != nil {
		return P2PArtifactView{}, err
	}
	c.mu.Lock()
	c.metrics.PublishedArtifacts++
	c.mu.Unlock()
	return P2PArtifactView{
		ArtifactID:       CanonicalArtifactID("", artifactName, ""),
		ArtifactName:     artifactName,
		Version:          "",
		SizeBytes:        info.Size(),
		ModifiedAtUTC:    formatTimeRFC3339(info.ModTime()),
		ChecksumSHA256:   checksum,
		Available:        true,
		LastHeartbeatUTC: formatTimeRFC3339(time.Now().UTC()),
	}, nil
}

func (c *p2pCoordinator) ReplicateArtifactToPeer(artifactName, targetPeerID string) (string, error) {
	return "", fmt.Errorf("modo push desabilitado: use transferencia pull sob demanda")
}

func (c *p2pCoordinator) replicateArtifactToPeerNow(artifactName, targetPeerID string) error {
	peer, err := c.findPeerByAgentID(targetPeerID)
	if err != nil {
		c.recordReplicationResult(false)
		return err
	}
	access, err := c.GetArtifactAccess(artifactName, targetPeerID)
	if err != nil {
		c.recordReplicationResult(false)
		return err
	}

	endpoint := fmt.Sprintf("http://%s:%d/p2p/replicate", peer.Address, peer.Port)
	body, err := json.Marshal(access)
	if err != nil {
		c.recordReplicationResult(false)
		return err
	}

	c.mu.Lock()
	c.metrics.ReplicationsStarted++
	c.mu.Unlock()

	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		c.recordReplicationResult(false)
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range c.transferServer.BuildReplicationHeaders(strings.TrimSpace(c.app.GetDebugConfig().AgentID), access) {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			continue
		}
		req.Header.Set(key, value)
	}

	resp, err := (&http.Client{Timeout: 45 * time.Second}).Do(req)
	if err != nil {
		c.recordReplicationResult(false)
		return err
	}
	defer resp.Body.Close()

	responseBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.recordReplicationResult(false)
		return fmt.Errorf("replicacao falhou HTTP %s: %s", resp.Status, strings.TrimSpace(string(responseBody)))
	}
	c.recordReplicationResult(true)
	return nil
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
	provider := pickDiscoveryProvider(cfg)

	selfHost, _ := os.Hostname()
	selfAgentID := strings.TrimSpace(c.app.GetDebugConfig().AgentID)
	baseURL := c.transferServer.BaseURL()
	port := 0
	if parsed, err := parsePortFromURL(baseURL); err == nil {
		port = parsed
	}

	if err := provider.Start(ctx, p2pSelfEndpoint{AgentID: selfAgentID, Host: selfHost, Port: port}, func(peer p2pDiscoveredPeer) {
		c.upsertPeer(peer)
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

func (c *p2pCoordinator) upsertPeer(peer p2pDiscoveredPeer) {
	if strings.TrimSpace(peer.AgentID) == "" {
		return
	}
	if strings.TrimSpace(peer.Address) == "" || peer.Port <= 0 {
		return
	}
	key := strings.ToLower(strings.TrimSpace(peer.AgentID))

	c.mu.Lock()
	previous, existed := c.peers[key]
	c.peers[key] = p2pPeerState{Peer: peer, LastSeenUTC: time.Now().UTC()}
	c.knownPeers = len(c.peers)
	c.mu.Unlock()

	if !existed {
		c.app.logs.append(fmt.Sprintf("[p2p] peer descoberto: agentId=%s source=%s addr=%s:%d", strings.TrimSpace(peer.AgentID), strings.TrimSpace(peer.Source), strings.TrimSpace(peer.Address), peer.Port))
		return
	}

	if strings.TrimSpace(previous.Peer.Address) != strings.TrimSpace(peer.Address) || previous.Peer.Port != peer.Port || strings.TrimSpace(previous.Peer.Source) != strings.TrimSpace(peer.Source) {
		c.app.logs.append(fmt.Sprintf("[p2p] peer atualizado: agentId=%s source=%s addr=%s:%d", strings.TrimSpace(peer.AgentID), strings.TrimSpace(peer.Source), strings.TrimSpace(peer.Address), peer.Port))
	}
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

func (c *p2pCoordinator) recordReplicationResult(success bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if success {
		c.metrics.ReplicationsSucceeded++
		return
	}
	c.metrics.ReplicationsFailed++
}

func (c *p2pCoordinator) recordBytesServed(size int64) {
	if size <= 0 {
		return
	}
	c.mu.Lock()
	c.metrics.BytesServed += size
	c.mu.Unlock()
}

func (c *p2pCoordinator) recordBytesDownloaded(size int64) {
	if size <= 0 {
		return
	}
	c.mu.Lock()
	c.metrics.BytesDownloaded += size
	c.mu.Unlock()
}

func (c *p2pCoordinator) enqueueReplicationJob(job p2pReplicationJob) error {
	job.ArtifactName = sanitizeArtifactName(job.ArtifactName)
	job.Checksum = strings.TrimSpace(job.Checksum)
	job.TargetPeerID = strings.TrimSpace(job.TargetPeerID)
	job.Source = strings.TrimSpace(job.Source)
	if job.Source == "" {
		job.Source = "manual"
	}
	if job.ArtifactName == "" {
		return fmt.Errorf("artifact invalido")
	}
	if job.TargetPeerID == "" {
		return fmt.Errorf("peer alvo nao informado")
	}
	if job.Checksum == "" {
		resolvedChecksum, err := c.resolveArtifactChecksum(job.ArtifactName)
		if err != nil {
			return err
		}
		job.Checksum = resolvedChecksum
	}

	now := time.Now().UTC()
	c.mu.Lock()
	c.pruneDedupLocked(now)
	if c.wasRecentlyReplicatedLocked(job.TargetPeerID, job.ArtifactName, job.Checksum, now) {
		c.mu.Unlock()
		c.appendAudit("skip-duplicate", job.ArtifactName, job.TargetPeerID, job.Source, true, errP2PDuplicateReplication.Error())
		return errP2PDuplicateReplication
	}
	c.mu.Unlock()

	select {
	case c.replicationQueue <- job:
		c.mu.Lock()
		c.metrics.QueuedReplications++
		c.mu.Unlock()
		c.appendAudit("queue", job.ArtifactName, job.TargetPeerID, job.Source, true, "replicacao enfileirada")
		return nil
	default:
		c.appendAudit("queue", job.ArtifactName, job.TargetPeerID, job.Source, false, "fila de replicacao cheia")
		return fmt.Errorf("fila de replicacao cheia")
	}
}

func (c *p2pCoordinator) replicationWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-c.replicationQueue:
			c.mu.Lock()
			if c.metrics.QueuedReplications > 0 {
				c.metrics.QueuedReplications--
			}
			c.metrics.ActiveReplications++
			c.mu.Unlock()

			err := c.processReplicationJob(job)
			if job.Result != nil {
				job.Result <- err
			}
		}
	}
}

func (c *p2pCoordinator) processReplicationJob(job p2pReplicationJob) error {
	peerKey := strings.ToLower(strings.TrimSpace(job.TargetPeerID))
	now := time.Now().UTC()

	c.mu.Lock()
	lastAttempt := c.peerLastAttempt[peerKey]
	if !lastAttempt.IsZero() && now.Sub(lastAttempt) < p2pPeerReplicationCooldown {
		if c.metrics.ActiveReplications > 0 {
			c.metrics.ActiveReplications--
		}
		c.mu.Unlock()
		err := fmt.Errorf("peer em cooldown de replicacao")
		c.appendAudit("cooldown", job.ArtifactName, job.TargetPeerID, job.Source, false, err.Error())
		return err
	}
	c.peerLastAttempt[peerKey] = now
	c.mu.Unlock()

	err := c.replicateArtifactToPeerNow(job.ArtifactName, job.TargetPeerID)
	if err != nil {
		c.appendAudit("replicate", job.ArtifactName, job.TargetPeerID, job.Source, false, err.Error())
	} else {
		c.markReplicated(job.TargetPeerID, job.ArtifactName, job.Checksum)
		c.appendAudit("replicate", job.ArtifactName, job.TargetPeerID, job.Source, true, "replicacao concluida")
	}

	c.mu.Lock()
	if c.metrics.ActiveReplications > 0 {
		c.metrics.ActiveReplications--
	}
	c.mu.Unlock()
	return err
}

func (c *p2pCoordinator) appendAudit(action, artifactName, peerAgentID, source string, success bool, message string) {
	event := P2PAuditEvent{
		TimestampUTC: formatTimeRFC3339(time.Now().UTC()),
		Action:       strings.TrimSpace(action),
		ArtifactName: strings.TrimSpace(artifactName),
		PeerAgentID:  strings.TrimSpace(peerAgentID),
		Source:       strings.TrimSpace(source),
		Success:      success,
		Message:      strings.TrimSpace(message),
	}
	c.mu.Lock()
	c.audit = append([]P2PAuditEvent{event}, c.audit...)
	if len(c.audit) > p2pAuditLimit {
		c.audit = c.audit[:p2pAuditLimit]
	}
	c.mu.Unlock()
}

func (c *p2pCoordinator) autoDistributeLocalArtifacts(resource, variant, revision string) {
	artifacts, err := c.ListArtifacts()
	if err != nil {
		c.appendAudit("auto-distribute", "", "", "sync", false, err.Error())
		return
	}
	peers := c.selectAutoDistributionPeers(c.GetPeers())
	if len(artifacts) == 0 || len(peers) == 0 {
		c.appendAudit("auto-distribute", "", "", "sync", true, "sem artifacts ou peers elegiveis")
		return
	}

	c.mu.Lock()
	c.metrics.AutoDistributionRuns++
	c.mu.Unlock()

	sort.SliceStable(artifacts, func(i, j int) bool {
		pi := c.artifactPriority(resource, variant, artifacts[i].ArtifactName)
		pj := c.artifactPriority(resource, variant, artifacts[j].ArtifactName)
		if pi != pj {
			return pi < pj
		}
		return strings.ToLower(artifacts[i].ArtifactName) < strings.ToLower(artifacts[j].ArtifactName)
	})

	enqueued := 0
	skippedDuplicates := 0
	for _, artifact := range artifacts {
		for _, peer := range peers {
			err := c.enqueueReplicationJob(p2pReplicationJob{
				ArtifactName: artifact.ArtifactName,
				Checksum:     artifact.ChecksumSHA256,
				TargetPeerID: peer.AgentID,
				Source:       "sync",
			})
			if err == nil {
				enqueued++
				continue
			}
			if errors.Is(err, errP2PDuplicateReplication) {
				skippedDuplicates++
			}
		}
	}
	c.appendAudit("auto-distribute", "", "", "sync", true, fmt.Sprintf("resource=%s variant=%s revision=%s jobs=%d duplicates=%d", resource, variant, revision, enqueued, skippedDuplicates))
}

func (c *p2pCoordinator) artifactPriority(resource, variant, artifactName string) int {
	name := strings.ToLower(strings.TrimSpace(artifactName))
	res := strings.ToLower(strings.TrimSpace(resource))
	varKey := strings.ToLower(strings.TrimSpace(variant))

	if res != "" && strings.Contains(name, res) {
		return 0
	}
	if varKey != "" && strings.Contains(name, varKey) {
		return 1
	}
	if res == "appstore" && (strings.Contains(name, "catalog") || strings.Contains(name, "store")) {
		return 0
	}
	if res == "automationpolicy" && strings.Contains(name, "automation") {
		return 0
	}
	if res == "configuration" && strings.Contains(name, "config") {
		return 0
	}
	return 2
}

func (c *p2pCoordinator) resolveArtifactChecksum(artifactName string) (string, error) {
	artifacts, err := c.ListArtifacts()
	if err != nil {
		return "", err
	}
	target := strings.ToLower(strings.TrimSpace(artifactName))
	for _, artifact := range artifacts {
		if strings.ToLower(strings.TrimSpace(artifact.ArtifactName)) != target {
			continue
		}
		if strings.TrimSpace(artifact.ChecksumSHA256) == "" {
			break
		}
		return strings.TrimSpace(artifact.ChecksumSHA256), nil
	}
	return "", fmt.Errorf("checksum do artifact nao encontrado")
}

func (c *p2pCoordinator) dedupKey(peerAgentID, artifactName, checksum string) string {
	return strings.ToLower(strings.TrimSpace(peerAgentID)) + "|" + strings.ToLower(strings.TrimSpace(artifactName)) + "|" + strings.ToLower(strings.TrimSpace(checksum))
}

func (c *p2pCoordinator) wasRecentlyReplicatedLocked(peerAgentID, artifactName, checksum string, now time.Time) bool {
	if strings.TrimSpace(checksum) == "" {
		return false
	}
	last, ok := c.replicationDedup[c.dedupKey(peerAgentID, artifactName, checksum)]
	if !ok {
		return false
	}
	return now.Sub(last) < p2pReplicationDedupTTL
}

func (c *p2pCoordinator) markReplicated(peerAgentID, artifactName, checksum string) {
	if strings.TrimSpace(checksum) == "" {
		return
	}
	now := time.Now().UTC()
	c.mu.Lock()
	c.pruneDedupLocked(now)
	c.replicationDedup[c.dedupKey(peerAgentID, artifactName, checksum)] = now
	c.mu.Unlock()
}

func (c *p2pCoordinator) pruneDedupLocked(now time.Time) {
	for key, ts := range c.replicationDedup {
		if now.Sub(ts) >= p2pReplicationDedupTTL {
			delete(c.replicationDedup, key)
		}
	}
}

func (c *p2pCoordinator) selectAutoDistributionPeers(peers []P2PPeerView) []P2PPeerView {
	status := c.GetStatus()
	leechers := len(peers) - maxInt(status.CurrentSeedPlan.SelectedSeeds-1, 0)
	if leechers <= 0 {
		return []P2PPeerView{}
	}
	if leechers > len(peers) {
		leechers = len(peers)
	}
	return peers[:leechers]
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (c *p2pCoordinator) pullPeerGossip(ctx context.Context) {
	c.RefreshPeerArtifactIndex(ctx, "gossip")
}

func (c *p2pCoordinator) RefreshPeerArtifactIndex(ctx context.Context, source string) {
	peers := c.GetPeers()
	client := &http.Client{Timeout: 3 * time.Second}
	source = strings.TrimSpace(source)
	if source == "" {
		source = "refresh"
	}

	c.mu.Lock()
	c.metrics.CatalogRefreshRuns++
	c.mu.Unlock()

	for _, peer := range peers {
		if strings.TrimSpace(peer.Address) == "" || peer.Port <= 0 {
			continue
		}
		url := fmt.Sprintf("http://%s:%d/p2p/peers", peer.Address, peer.Port)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			continue
		}
		var payload struct {
			KnownPeers    []P2PPeerView     `json:"knownPeers"`
			Artifacts     []P2PArtifactView `json:"artifacts"`
			CatalogSource string            `json:"catalogSource"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err == nil {
			for _, p := range payload.KnownPeers {
				c.upsertPeer(p2pDiscoveredPeer{
					AgentID:      strings.TrimSpace(p.AgentID),
					Host:         strings.TrimSpace(p.Host),
					Address:      strings.TrimSpace(p.Address),
					Port:         p.Port,
					Source:       "gossip",
					KnownPeers:   p.KnownPeers,
					ConnectedVia: "gossip",
				})
			}
			catalogSource := strings.TrimSpace(payload.CatalogSource)
			if catalogSource == "" {
				catalogSource = source
			}
			c.upsertPeerArtifacts(peer.AgentID, payload.Artifacts, catalogSource)
			if len(payload.Artifacts) > 0 {
				c.app.logs.append(fmt.Sprintf("[p2p] catalogo atualizado: peer=%s artifacts=%d source=%s", strings.TrimSpace(peer.AgentID), len(payload.Artifacts), catalogSource))
			}
		}
		resp.Body.Close()
	}
}

func (c *p2pCoordinator) upsertPeerArtifacts(peerAgentID string, artifacts []P2PArtifactView, source string) {
	peerKey := strings.ToLower(strings.TrimSpace(peerAgentID))
	if peerKey == "" {
		return
	}
	if source == "" {
		source = "unknown"
	}
	clean := make([]P2PArtifactView, 0, len(artifacts))
	for _, artifact := range artifacts {
		name := sanitizeArtifactName(artifact.ArtifactName)
		if name == "" {
			continue
		}
		canonicalID := CanonicalArtifactID(artifact.ArtifactID, name, "")
		newChecksum := strings.TrimSpace(artifact.ChecksumSHA256)
		// Log mismatch vs previously known checksum for same artifactId (audit Epic 1).
		if canonicalID != "" && newChecksum != "" {
			c.mu.RLock()
			if prev, ok := c.peerArtifacts[peerKey]; ok {
				for _, pa := range prev.Artifacts {
					if strings.EqualFold(strings.TrimSpace(pa.ArtifactID), canonicalID) &&
						strings.TrimSpace(pa.ChecksumSHA256) != "" &&
						!strings.EqualFold(strings.TrimSpace(pa.ChecksumSHA256), newChecksum) {
						short := func(s string) string {
							if len(s) > 8 {
								return s[:8]
							}
							return s
						}
						c.app.logs.append(fmt.Sprintf("[p2p][audit] checksum divergente artifactId=%s peer=%s: anterior=%s... novo=%s...",
							canonicalID, peerAgentID, short(pa.ChecksumSHA256), short(newChecksum)))
					}
				}
			}
			c.mu.RUnlock()
		}
		heartbeat := strings.TrimSpace(artifact.LastHeartbeatUTC)
		if heartbeat == "" {
			heartbeat = formatTimeRFC3339(time.Now().UTC())
		}
		clean = append(clean, P2PArtifactView{
			ArtifactID:       canonicalID,
			ArtifactName:     name,
			Version:          strings.TrimSpace(artifact.Version),
			SizeBytes:        artifact.SizeBytes,
			ModifiedAtUTC:    strings.TrimSpace(artifact.ModifiedAtUTC),
			ChecksumSHA256:   newChecksum,
			Available:        true,
			LastHeartbeatUTC: heartbeat,
		})
	}
	sort.SliceStable(clean, func(i, j int) bool {
		return strings.ToLower(strings.TrimSpace(clean[i].ArtifactName)) < strings.ToLower(strings.TrimSpace(clean[j].ArtifactName))
	})

	c.mu.Lock()
	c.peerArtifacts[peerKey] = p2pPeerArtifactState{
		Artifacts:      clean,
		LastUpdatedUTC: time.Now().UTC(),
		Source:         source,
	}
	c.mu.Unlock()
}

func parsePortFromURL(raw string) (int, error) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) < 2 {
		return 0, fmt.Errorf("url sem porta")
	}
	portPart := strings.TrimSpace(parts[len(parts)-1])
	if strings.Contains(portPart, "/") {
		chunks := strings.Split(portPart, "/")
		portPart = chunks[0]
	}
	return strconv.Atoi(portPart)
}

func resolveP2PTempDir(goos string) string {
	if strings.EqualFold(strings.TrimSpace(goos), "windows") {
		// Usar pasta temporária do Windows para permitir limpeza automática pelo sistema.
		windowsDir := strings.TrimSpace(os.Getenv("WINDIR"))
		if windowsDir == "" {
			windowsDir = filepath.Join("C:\\", "Windows")
		}
		return filepath.Join(windowsDir, "Temp", "Discovery", "P2P_Temp")
	}
	return filepath.Join(getDataDir(), "TempP2P")
}

func (a *App) p2pTempDir() string {
	return resolveP2PTempDir(runtime.GOOS)
}

func (a *App) cleanupExpiredP2PTempArtifacts(now time.Time) (int, error) {
	cfg := a.GetP2PConfig()
	ttl := time.Duration(cfg.TempTTLHours) * time.Hour
	dir := a.p2pTempDir()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}

	removed := 0
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if now.Sub(info.ModTime()) < ttl {
			return nil
		}
		if err := os.Remove(path); err != nil {
			return nil
		}
		removed++
		return nil
	})
	if err != nil {
		return removed, err
	}

	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if !d.IsDir() || path == dir {
			return nil
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil
		}
		if len(entries) == 0 {
			_ = os.Remove(path)
		}
		return nil
	})

	if a.p2pCoord != nil {
		a.p2pCoord.mu.Lock()
		a.p2pCoord.lastCleanupUTC = now.UTC()
		a.p2pCoord.mu.Unlock()
	}

	if removed > 0 {
		a.logs.append(fmt.Sprintf("[p2p] limpeza de temp concluida: %d item(ns) removido(s)", removed))
	}
	return removed, nil
}

func formatTimeRFC3339(v time.Time) string {
	if v.IsZero() {
		return ""
	}
	return v.UTC().Format(time.RFC3339)
}
