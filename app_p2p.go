package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
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
)

type p2pCoordinator struct {
	app *App

	mu                sync.RWMutex
	peers             map[string]p2pPeerState
	metrics           P2PMetrics
	knownPeers        int
	lastCleanupUTC    time.Time
	lastDiscoveryTick time.Time
	lastErr           string
	currentSeedPlan   P2PSeedPlan
	listenAddress     string
	discoveryProvider p2pDiscoveryProvider
	transferServer    *p2pTransferServer
}

type p2pPeerState struct {
	Peer        p2pDiscoveredPeer
	LastSeenUTC time.Time
}

func newP2PCoordinator(app *App) *p2pCoordinator {
	return &p2pCoordinator{
		app:            app,
		peers:          make(map[string]p2pPeerState),
		transferServer: newP2PTransferServer(app),
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
	if a.p2pCoord == nil {
		return "", fmt.Errorf("coordinator P2P indisponivel")
	}
	return a.p2pCoord.ReplicateArtifactToPeer(artifactName, targetPeerID)
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
}

func (c *p2pCoordinator) GetStatus() P2PDebugStatus {
	cfg := c.app.GetP2PConfig()
	c.mu.RLock()
	defer c.mu.RUnlock()
	return P2PDebugStatus{
		Active:               c.app.runtimeFlags.DebugMode && cfg.Enabled,
		DiscoveryMode:        cfg.DiscoveryMode,
		KnownPeers:           c.knownPeers,
		ListenAddress:        c.listenAddress,
		TempDir:              c.app.p2pTempDir(),
		TempTTLHours:         cfg.TempTTLHours,
		LastCleanupUTC:       formatTimeRFC3339(c.lastCleanupUTC),
		LastDiscoveryTickUTC: formatTimeRFC3339(c.lastDiscoveryTick),
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

func (c *p2pCoordinator) GetArtifactAccess(artifactName, targetPeerID string) (P2PArtifactAccess, error) {
	c.mu.RLock()
	transfer := c.transferServer
	c.mu.RUnlock()
	if transfer == nil {
		return P2PArtifactAccess{}, fmt.Errorf("servidor de transferencia indisponivel")
	}
	return transfer.BuildArtifactAccess(artifactName, targetPeerID)
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
			ArtifactName:   name,
			SizeBytes:      info.Size(),
			ModifiedAtUTC:  formatTimeRFC3339(info.ModTime()),
			ChecksumSHA256: checksum,
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
		ArtifactName:   artifactName,
		SizeBytes:      info.Size(),
		ModifiedAtUTC:  formatTimeRFC3339(info.ModTime()),
		ChecksumSHA256: checksum,
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
		ArtifactName:   artifactName,
		SizeBytes:      info.Size(),
		ModifiedAtUTC:  formatTimeRFC3339(info.ModTime()),
		ChecksumSHA256: checksum,
	}, nil
}

func (c *p2pCoordinator) ReplicateArtifactToPeer(artifactName, targetPeerID string) (string, error) {
	peer, err := c.findPeerByAgentID(targetPeerID)
	if err != nil {
		c.recordReplicationResult(false)
		return "", err
	}
	access, err := c.GetArtifactAccess(artifactName, targetPeerID)
	if err != nil {
		c.recordReplicationResult(false)
		return "", err
	}

	endpoint := fmt.Sprintf("http://%s:%d/p2p/replicate", peer.Address, peer.Port)
	body, err := json.Marshal(access)
	if err != nil {
		c.recordReplicationResult(false)
		return "", err
	}

	c.mu.Lock()
	c.metrics.ReplicationsStarted++
	c.mu.Unlock()

	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		c.recordReplicationResult(false)
		return "", err
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
		return "", err
	}
	defer resp.Body.Close()

	responseBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.recordReplicationResult(false)
		return "", fmt.Errorf("replicacao falhou HTTP %s: %s", resp.Status, strings.TrimSpace(string(responseBody)))
	}
	c.recordReplicationResult(true)
	return fmt.Sprintf("artifact %s replicado para %s", artifactName, targetPeerID), nil
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
	var provider p2pDiscoveryProvider
	if cfg.DiscoveryMode == p2pDiscoveryUDP {
		provider = &p2pUDPProvider{}
	} else {
		provider = &p2pMDNSProvider{}
	}

	selfHost, _ := os.Hostname()
	selfAgentID := strings.TrimSpace(c.app.GetDebugConfig().AgentID)
	baseURL := c.transferServer.BaseURL()
	port := 0
	if parsed, err := parsePortFromURL(baseURL); err == nil {
		port = parsed
	}

	if err := provider.Start(ctx, p2pSelfEndpoint{AgentID: selfAgentID, Host: selfHost, Port: port}, func(peer p2pDiscoveredPeer) {
		c.upsertPeer(peer)
	}); err != nil {
		return err
	}

	c.mu.Lock()
	c.discoveryProvider = provider
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
	c.peers[key] = p2pPeerState{Peer: peer, LastSeenUTC: time.Now().UTC()}
	c.knownPeers = len(c.peers)
	c.mu.Unlock()
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

func (c *p2pCoordinator) pullPeerGossip(ctx context.Context) {
	peers := c.GetPeers()
	client := &http.Client{Timeout: 3 * time.Second}

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
			KnownPeers []P2PPeerView `json:"knownPeers"`
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
		}
		resp.Body.Close()
	}
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

func (a *App) p2pTempDir() string {
	base := getDataDir()
	if runtime.GOOS == "windows" {
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			base = filepath.Join(localAppData, "Discovery")
		}
	}
	return filepath.Join(base, "TempP2P")
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
