package app

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ─── Fase 2.1/2.2: Eleição local de fetcher + Heartbeat de lease ────────────

// ArtifactFetchState representa o estado de um artifact no grupo local.
type ArtifactFetchState struct {
	ArtifactID  string
	ClientID    string
	Status      string // "missing", "fetching", "available", "failed"
	OwnerPeerID string
	LeaseUntil  time.Time
	ProgressPct float64
}

// ArtifactFetchCandidate representa a candidatura de um peer para ser fetcher.
type ArtifactFetchCandidate struct {
	ArtifactID string  `json:"artifactId"`
	ClientID   string  `json:"clientId"`
	AgentID    string  `json:"agentId"`
	CPUCores   int     `json:"cpuCores"`
	RAMGB      float64 `json:"ramGb"`
	CPUPercent float64 `json:"cpuPercent"`
	MemPercent float64 `json:"memPercent"`
}

// ArtifactFetchHeartbeat é publicado pelo fetcher no tópico scoped de gossip.
type ArtifactFetchHeartbeat struct {
	ArtifactID  string    `json:"artifactId"`
	ClientID    string    `json:"clientId"`
	OwnerPeerID string    `json:"ownerPeerId"`
	Status      string    `json:"status"`
	LeaseUntil  time.Time `json:"leaseUntil"`
	ProgressPct float64   `json:"progressPct"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

const (
	artifactFetchLeaseTTL       = 90 * time.Second
	artifactFetchHeartbeatEvery = 15 * time.Second
	// electionGracePeriod é o tempo que um peer aguarda após broadcast de
	// candidatura antes de se auto-eleger, permitindo que peers remotos
	// reivindiquem o lease e evitem fetchers duplicados.
	electionGracePeriod = 2 * time.Second
)

// fetchStateMap guarda o estado atual de cada artifact neste grupo.
type fetchStateMap struct {
	mu     sync.Mutex
	states map[string]*ArtifactFetchState // key = artifactID
}

func newFetchStateMap() *fetchStateMap {
	return &fetchStateMap{states: make(map[string]*ArtifactFetchState)}
}

func (f *fetchStateMap) getOrCreate(artifactID, clientID string) *ArtifactFetchState {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.states[artifactID]; ok {
		return s
	}
	s := &ArtifactFetchState{
		ArtifactID: artifactID,
		ClientID:   clientID,
		Status:     "missing",
	}
	f.states[artifactID] = s
	return s
}

func (f *fetchStateMap) set(artifactID string, state *ArtifactFetchState) {
	f.mu.Lock()
	f.states[artifactID] = state
	f.mu.Unlock()
}

// computeScore calcula o score de capacidade para eleição.
// Quanto maior, mais capaz o peer é de fazer o fetch.
func computeScore(candidate ArtifactFetchCandidate, maxCPUCores int, maxRAMGB float64) float64 {
	coresFree := float64(candidate.CPUCores) * (100 - candidate.CPUPercent) / 100
	ramFreeGB := candidate.RAMGB * (100 - candidate.MemPercent) / 100

	coreScore := coresFree / float64(maxCPUCores) * 0.5
	ramScore := ramFreeGB / maxRAMGB * 0.5

	return coreScore + ramScore
}

// canStartLocalElection verifica se é possível iniciar uma eleição local.
func canStartLocalElection(state *ArtifactFetchState, now time.Time, loadOK bool) bool {
	if state == nil {
		return loadOK
	}
	if state.Status == "available" {
		return false
	}
	if state.Status == "fetching" && now.Before(state.LeaseUntil) {
		return false
	}
	return loadOK
}

// canServePartsNow verifica se o host pode servir partes P2P baseado na carga.
func canServePartsNow(load P2PHostLoad) bool {
	return load.CPUPercent < 75 && load.MemoryPercent < 85 && load.DiskBusyPercent < 80
}

// isLoadOK verifica se o host está abaixo dos limiares de sobrecarga para participar de eleição.
func (c *p2pCoordinator) isLoadOK() bool {
	load := c.collectHostLoad()
	return load.CPUPercent < 70 && load.MemoryPercent < 85 && load.DiskBusyPercent < 80
}

// ─── Fase 2.5: Paralelismo dinâmico de download ─────────────────────────────

// TransferLoadSnapshot descreve a carga atual para decisão de paralelismo.
type TransferLoadSnapshot struct {
	CPUPercent      float64
	MemoryPercent   float64
	DiskBusyPercent float64
}

// decideDownloadParallelism define quantas partes baixar em paralelo baseado na carga.
func decideDownloadParallelism(load TransferLoadSnapshot) int {
	switch {
	case load.CPUPercent < 45 && load.MemoryPercent < 70 && load.DiskBusyPercent < 50:
		return 8
	case load.CPUPercent < 70 && load.MemoryPercent < 85 && load.DiskBusyPercent < 75:
		return 4
	default:
		return 2
	}
}

// dynamicMaxParallelChunks retorna o número de chunks paralelos com base na carga atual do host.
func (c *p2pCoordinator) dynamicMaxParallelChunks() int {
	if c.deps == nil {
		return maxParallelChunks
	}
	metrics := c.deps.GetHeartbeatMetrics()
	snapshot := TransferLoadSnapshot{
		CPUPercent:      math.Max(metrics.CpuPercent, 0),
		MemoryPercent:   math.Max(metrics.MemoryPercent, 0),
		DiskBusyPercent: math.Max(metrics.DiskReadPercent, 0) + math.Max(metrics.DiskWritePercent, 0),
	}
	return decideDownloadParallelism(snapshot)
}

// ─── Fase 2.7: Garbage Collection de manifests órfãos ───────────────────────

// collectOrphanArtifacts remove arquivos de cache de manifest cujo artifact original não existe mais.
func (c *p2pCoordinator) collectOrphanArtifacts() {
	if c.transferServer == nil {
		return
	}
	manifestDir := c.transferServer.manifestDir()
	if manifestDir == "" {
		return
	}

	entries, err := os.ReadDir(manifestDir)
	if err != nil {
		return
	}

	tempDir := c.deps.P2PTempDir()
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		// artifactName = nome do arquivo sem .json
		artifactName := strings.TrimSuffix(name, ".json")
		artifactPath := filepath.Join(tempDir, artifactName)

		// Se o artifact não existe mais, remove o manifest cacheado.
		if _, err := os.Stat(artifactPath); err != nil {
			manifestPath := filepath.Join(manifestDir, name)
			if rmErr := os.Remove(manifestPath); rmErr == nil {
				removed++
			}
		}
	}

	if removed > 0 && c.deps != nil {
		c.deps.Log(fmt.Sprintf("[p2p][gc] %d manifest(s) orfao(s) removido(s)", removed))
	}
}

// ─── restartProvider ────────────────────────────────────────────────────────

// restartProvider reinicia o provider de descoberta após mudança de configuração.
// Usado após zero-touch config registration para entrar na malha correta.
// O clientId é lido diretamente de c.deps.GetAgentConfiguration() via startDiscovery.
func (c *p2pCoordinator) restartProvider() {
	c.mu.Lock()
	oldProvider := c.discoveryProvider
	c.mu.Unlock()

	if oldProvider == nil {
		return
	}

	// Close old host if libp2p.
	if lp, ok := oldProvider.(*p2pLibP2PProvider); ok && lp.h != nil {
		_ = lp.h.Close()
	}

	// Start new provider — clientId será lido da config agente por startDiscovery.
	ctx := context.Background()
	if c.deps != nil && c.deps.Context() != nil {
		ctx = c.deps.Context()
	}

	_ = c.startDiscovery(ctx)
}

// valida se tem pelo menos um addr válido para peer.online
func hasValidAddr(addrs []string) bool {
	for _, addr := range addrs {
		if strings.TrimSpace(addr) != "" {
			return true
		}
	}
	return false
}
