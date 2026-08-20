package p2p

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"discovery/app/agentconfig"
	"discovery/app/core/agentconn"
	debug "discovery/app/debug"
	"discovery/app/logs"
	"discovery/app/p2pmeta"
	supportmeta "discovery/app/supportmeta"
)

// mockDeps implementa AppDeps para testes do Coordinator.
type mockDeps struct {
	cfg       Config
	debugCfg  debug.Config
	agentCfg  agentconfig.AgentConfiguration
	tempDir   string
	dataDir   string
	logBuffer *logs.Buffer
}

func (m *mockDeps) GetP2PConfig() p2pmeta.Config { return m.cfg }
func (m *mockDeps) GetDebugConfig() debug.Config { return m.debugCfg }
func (m *mockDeps) GetAgentConfiguration() agentconfig.AgentConfiguration {
	return m.agentCfg
}
func (m *mockDeps) P2PTempDir() string { return m.tempDir }
func (m *mockDeps) CleanupExpiredP2PTempArtifacts(now time.Time) (int, error) {
	return 0, nil
}
func (m *mockDeps) GetDataDir() string                 { return m.dataDir }
func (m *mockDeps) EmitEvent(name string, data ...any) {}
func (m *mockDeps) Log(line string) {
	if m.logBuffer != nil {
		m.logBuffer.Append(line)
	}
}
func (m *mockDeps) GetHeartbeatMetrics() agentconn.AgentHeartbeatMetrics {
	return agentconn.AgentHeartbeatMetrics{}
}
func (m *mockDeps) GetAgentInfo() (supportmeta.AgentInfo, error) {
	return supportmeta.AgentInfo{}, nil
}
func (m *mockDeps) Context() context.Context { return context.Background() }
func (m *mockDeps) DebugMode() bool          { return false }
func (m *mockDeps) RequestProvisioningToken(ctx context.Context) (string, string, error) {
	return "", "", nil
}
func (m *mockDeps) ApplyOnboardingOffer(offer p2pmeta.OnboardingRequest) (p2pmeta.OnboardingResult, error) {
	return p2pmeta.OnboardingResult{}, nil
}
func (m *mockDeps) TriggerZeroTouchConfigRegistrationOnPeerDiscovery(ctx context.Context, peer p2pmeta.DiscoveredPeer) {
}
func (m *mockDeps) IsAgentConfigured() bool { return false }
func (m *mockDeps) LoadInstallerConfig() (debug.InstallerConfig, string, error) {
	return debug.InstallerConfig{}, "", nil
}
func (m *mockDeps) BuildOnboardingOffer(sourceAgentID, serverURL, deployKey string, ttl time.Duration) (p2pmeta.OnboardingRequest, error) {
	return p2pmeta.OnboardingRequest{}, nil
}

func TestP2PSeedCountRule(t *testing.T) {
	tests := []struct {
		name     string
		total    int
		percent  int
		minSeeds int
		expected int
	}{
		{name: "zero agents", total: 0, percent: 10, minSeeds: 2, expected: 0},
		{name: "one agent", total: 1, percent: 10, minSeeds: 2, expected: 1},
		{name: "two agents", total: 2, percent: 10, minSeeds: 2, expected: 2},
		{name: "ten agents", total: 10, percent: 10, minSeeds: 2, expected: 2},
		{name: "twenty five agents", total: 25, percent: 10, minSeeds: 2, expected: 3},
		{name: "fifty agents", total: 50, percent: 10, minSeeds: 2, expected: 5},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := p2pSeedCount(tc.total, tc.percent, tc.minSeeds); got != tc.expected {
				t.Fatalf("p2pSeedCount(%d, %d, %d) = %d, want %d", tc.total, tc.percent, tc.minSeeds, got, tc.expected)
			}
		})
	}
}

func TestNormalizeP2PConfigDefaults(t *testing.T) {
	cfg := normalizeP2PConfig(P2PConfig{})
	if cfg.TempTTLHours != defaultP2PTempTTLHours {
		t.Fatalf("temp ttl = %d, want %d", cfg.TempTTLHours, defaultP2PTempTTLHours)
	}
	if cfg.SeedPercent != defaultP2PSeedPercent {
		t.Fatalf("seed percent = %d, want %d", cfg.SeedPercent, defaultP2PSeedPercent)
	}
	if cfg.MinSeeds != defaultP2PMinSeeds {
		t.Fatalf("min seeds = %d, want %d", cfg.MinSeeds, defaultP2PMinSeeds)
	}
}

func TestBuildP2PSeedPlan(t *testing.T) {
	cfg := P2PConfig{SeedPercent: 10, MinSeeds: 2, TempTTLHours: 168}
	plan := buildP2PSeedPlan(25, cfg)
	if plan.SelectedSeeds != 3 {
		t.Fatalf("selected seeds = %d, want 3", plan.SelectedSeeds)
	}
	if plan.TotalAgents != 25 {
		t.Fatalf("total agents = %d, want 25", plan.TotalAgents)
	}
}

func TestListAuditEventsFiltered(t *testing.T) {
	c := &Coordinator{}
	c.audit = []P2PAuditEvent{
		{TimestampUTC: time.Now().UTC().Format(time.RFC3339), Action: "replicate", PeerAgentID: "peer-a", Success: true, Message: "ok"},
		{TimestampUTC: time.Now().UTC().Format(time.RFC3339), Action: "queue", PeerAgentID: "peer-b", Success: true, Message: "queued"},
		{TimestampUTC: time.Now().UTC().Format(time.RFC3339), Action: "replicate", PeerAgentID: "peer-a", Success: false, Message: "error"},
	}

	failedReplicate := c.ListAuditEventsFiltered("replicate", "peer-a", "error")
	if len(failedReplicate) != 1 {
		t.Fatalf("expected 1 failed replicate event, got %d", len(failedReplicate))
	}
	if failedReplicate[0].Success {
		t.Fatalf("expected filtered event to be failure")
	}
}

func TestAppendAuditWritesAgentLogLine(t *testing.T) {
	buf := logs.New()
	a := &mockDeps{logBuffer: buf}
	c := &Coordinator{deps: a}

	c.appendAudit("pull", "agent.bin", "peer-a", "libp2p", false, "falha simulada")

	if len(c.audit) != 1 {
		t.Fatalf("expected 1 audit event, got %d", len(c.audit))
	}
	lines := buf.GetAll()
	if len(lines) != 1 {
		t.Fatalf("expected 1 log line, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "[p2p][audit]") || !strings.Contains(lines[0], "status=erro") {
		t.Fatalf("unexpected log line: %q", lines[0])
	}
}

func TestDownloadArtifactFromPeerAuditsFailureWhenPeerNotFound(t *testing.T) {
	a := &mockDeps{}
	c := &Coordinator{deps: a, peers: map[string]p2pPeerState{}, peerArtifacts: map[string]p2pPeerArtifactState{}}

	_, err := c.DownloadArtifactFromPeer(context.Background(), "agent.bin", "peer-missing")
	if err == nil {
		t.Fatal("expected error when peer is missing")
	}

	events := c.ListAuditEventsFiltered("pull", "peer-missing", "error")
	if len(events) == 0 {
		t.Fatal("expected pull failure to be written to audit")
	}
	if strings.TrimSpace(events[0].Message) == "" {
		t.Fatal("expected audit message to include failure reason")
	}
}

func TestArtifactPriorityByResource(t *testing.T) {
	c := &Coordinator{}
	high := c.artifactPriority("appstore", "stable", "appstore-catalog-v2.json")
	medium := c.artifactPriority("appstore", "stable", "agent-stable-package.bin")
	low := c.artifactPriority("appstore", "stable", "unrelated-backup.dat")

	if !(high < medium && medium < low) {
		t.Fatalf("unexpected priority order: high=%d medium=%d low=%d", high, medium, low)
	}
}

func TestFindArtifactPeersFromIndex(t *testing.T) {
	c := &Coordinator{
		peers:         make(map[string]p2pPeerState),
		peerArtifacts: make(map[string]p2pPeerArtifactState),
	}
	now := time.Now().UTC()
	c.peers["peer-a"] = p2pPeerState{Peer: p2pDiscoveredPeer{AgentID: "peer-a"}, LastSeenUTC: now}
	c.peers["peer-b"] = p2pPeerState{Peer: p2pDiscoveredPeer{AgentID: "peer-b"}, LastSeenUTC: now}
	// ArtifactID must be populated — name-only lookup was removed.
	c.peerArtifacts["peer-a"] = p2pPeerArtifactState{
		Artifacts: []P2PArtifactView{{
			ArtifactID:   CanonicalArtifactID("", "xyz.bin", ""),
			ArtifactName: "xyz.bin",
		}},
	}
	c.peerArtifacts["peer-b"] = p2pPeerArtifactState{
		Artifacts: []P2PArtifactView{{
			ArtifactID:   CanonicalArtifactID("", "other.bin", ""),
			ArtifactName: "other.bin",
		}},
	}

	availability := c.FindArtifactPeers("xyz.bin")
	if !availability.Found {
		t.Fatal("expected artifact availability to be found")
	}
	if availability.PeerCount != 1 {
		t.Fatalf("expected 1 peer, got %d", availability.PeerCount)
	}
	if len(availability.PeerAgentIDs) != 1 || availability.PeerAgentIDs[0] != "peer-a" {
		t.Fatalf("unexpected peer list: %+v", availability.PeerAgentIDs)
	}

	// Peer without explicit ArtifactID must NOT be found via name fallback.
	c.peerArtifacts["peer-c"] = p2pPeerArtifactState{
		Artifacts: []P2PArtifactView{{ArtifactName: "xyz.bin"}}, // no ArtifactID
	}
	availability2 := c.FindArtifactPeers("xyz.bin")
	if availability2.PeerCount != 1 {
		t.Fatalf("expected only peer-a (id-based), got %d peers: %v", availability2.PeerCount, availability2.PeerAgentIDs)
	}
}

func TestApplyP2PDiscoverySnapshot_UsesTTLAndSequence(t *testing.T) {
	c := &Coordinator{
		deps:  &mockDeps{},
		peers: make(map[string]p2pPeerState),
	}
	c.ApplyP2PDiscoverySnapshot(agentconn.P2PDiscoverySnapshot{
		Sequence:   7,
		TTLSeconds: 90,
		Peers: []agentconn.P2PDiscoveryPeer{{
			AgentID: "agent-a",
			PeerID:  "12D3KooWabc",
			Addrs:   []string{"192.168.1.15"},
			Port:    41080,
		}},
	})

	peer, ok := c.peers["agent-a"]
	if !ok {
		t.Fatal("expected peer from snapshot to be stored")
	}
	if peer.Peer.Source != "nats-discovery" {
		t.Fatalf("Source = %q", peer.Peer.Source)
	}
	if peer.Peer.TTLSeconds != 90 {
		t.Fatalf("TTLSeconds = %d", peer.Peer.TTLSeconds)
	}
	if c.lastP2PDiscoverySeq != 7 {
		t.Fatalf("lastP2PDiscoverySeq = %d", c.lastP2PDiscoverySeq)
	}

	c.ApplyP2PDiscoverySnapshot(agentconn.P2PDiscoverySnapshot{
		Sequence:   3,
		TTLSeconds: 30,
		Peers: []agentconn.P2PDiscoveryPeer{{
			AgentID: "agent-b",
			Addrs:   []string{"192.168.1.16"},
			Port:    41081,
		}},
	})
	if _, ok := c.peers["agent-b"]; ok {
		t.Fatal("expected older snapshot to be ignored")
	}
}

// ── Epic 1: ArtifactID canonicalization ──────────────────────────────────────

func TestCanonicalArtifactIDExplicit(t *testing.T) {
	id := CanonicalArtifactID("APP-123", "firefox.exe", "https://example.com/ff.exe")
	if id != "APP-123" {
		t.Fatalf("expected APP-123, got %s", id)
	}
}

func TestCanonicalArtifactIDFromURL(t *testing.T) {
	id := CanonicalArtifactID("", "", "https://example.com/firefox.exe")
	if !strings.HasPrefix(id, "urlsha256:") {
		t.Fatalf("expected urlsha256: prefix, got %s", id)
	}
	// Idempotent: same URL must produce same hash.
	id2 := CanonicalArtifactID("", "", "https://example.com/firefox.exe")
	if id != id2 {
		t.Fatalf("CanonicalArtifactID not idempotent: %s != %s", id, id2)
	}
	// Different URL → different hash.
	id3 := CanonicalArtifactID("", "", "https://example.com/chrome.exe")
	if id == id3 {
		t.Fatalf("different URLs should not produce same id")
	}
}

func TestCanonicalArtifactIDFromName(t *testing.T) {
	id := CanonicalArtifactID("", "firefox.exe", "")
	if !strings.HasPrefix(id, "name:") {
		t.Fatalf("expected name: prefix, got %s", id)
	}
}

func TestCanonicalArtifactIDEmpty(t *testing.T) {
	id := CanonicalArtifactID("", "", "")
	if id != "" {
		t.Fatalf("expected empty id, got %s", id)
	}
}

// ── Epic 2: Onboarding signature & offer ─────────────────────────────────────

// ── Epic 7: go-libp2p provider ────────────────────────────────────────────────

func TestPickDiscoveryProviderLibP2POnly(t *testing.T) {
	cfg := normalizeP2PConfig(P2PConfig{TempTTLHours: 168})
	p := pickDiscoveryProvider(cfg, nil, nil)
	if p.Name() != p2pDiscoveryLibP2P {
		t.Fatalf("expected libp2p provider, got %s", p.Name())
	}
}

// ── Progresso de chunks ──────────────────────────────────────────────────────

func TestCompletedChunksBytes(t *testing.T) {
	manifest := P2PChunkManifest{
		ChunkSize: 8,
		Chunks: []P2PChunk{
			{Index: 0, Size: 8},
			{Index: 1, Size: 8},
			{Index: 2, Size: 3}, // último chunk menor
		},
	}
	// Nenhum chunk concluído.
	if got := completedChunksBytes(manifest, map[int]bool{}); got != 0 {
		t.Fatalf("empty: got %d, want 0", got)
	}
	// Apenas chunk 0.
	if got := completedChunksBytes(manifest, map[int]bool{0: true}); got != 8 {
		t.Fatalf("chunk0: got %d, want 8", got)
	}
	// Chunks 0 e 1.
	if got := completedChunksBytes(manifest, map[int]bool{0: true, 1: true}); got != 16 {
		t.Fatalf("chunks0,1: got %d, want 16", got)
	}
	// Todos os chunks (0,1,2).
	if got := completedChunksBytes(manifest, map[int]bool{0: true, 1: true, 2: true}); got != 19 {
		t.Fatalf("all: got %d, want 19", got)
	}
	// Fora de ordem: chunk 2 (último, menor) concluído antes do 0.
	if got := completedChunksBytes(manifest, map[int]bool{2: true}); got != 3 {
		t.Fatalf("chunk2 only: got %d, want 3", got)
	}
	// Índice fora do range não deve estourar.
	if got := completedChunksBytes(manifest, map[int]bool{99: true}); got != 0 {
		t.Fatalf("chunk99: got %d, want 0", got)
	}
}

// ── Lock por artifact ────────────────────────────────────────────────────────

func TestLockDownloadSerializesAndCleansUp(t *testing.T) {
	c := &Coordinator{downloadLocks: make(map[string]*downloadLockEntry)}

	unlock1 := c.lockDownload("artifact-a")
	// Segundo lock no mesmo artifact deve bloquear (serialização).
	done := make(chan struct{})
	go func() {
		unlock2 := c.lockDownload("artifact-a")
		unlock2()
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("segundo lock nao deveria adquirir enquanto o primeiro esta ativo")
	case <-time.After(50 * time.Millisecond):
	}

	unlock1()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("segundo lock deveria adquirir apos o primeiro liberar")
	}

	// Após ambos liberarem, o lock deve ser removido do mapa (sem leak).
	c.downloadLocksMu.Lock()
	_, exists := c.downloadLocks["artifact-a"]
	c.downloadLocksMu.Unlock()
	if exists {
		t.Fatal("lock de artifact-a deveria ter sido removido do mapa")
	}
}

// ── GC de sessões de upload ─────────────────────────────────────────────────

func TestGCServingSessionsRemovesStale(t *testing.T) {
	c := &Coordinator{
		servingSessions: map[string]*servingSession{
			"a|peer1": {lastActive: time.Now().Add(-30 * time.Minute)},
			"b|peer2": {lastActive: time.Now()},
		},
	}
	c.gcServingSessions(time.Now())
	if _, ok := c.servingSessions["a|peer1"]; ok {
		t.Fatal("sessao stale deveria ter sido removida")
	}
	if _, ok := c.servingSessions["b|peer2"]; !ok {
		t.Fatal("sessao ativa nao deveria ser removida")
	}
}

func TestExtractIPFromMultiaddr(t *testing.T) {
	tests := []struct{ ma, want string }{
		{"/ip4/192.168.1.5/tcp/41080", "192.168.1.5"},
		{"/ip6/::1/tcp/41080", "::1"},
		{"/dns4/example.com/tcp/41080", ""},
		{"", ""},
	}
	for _, tc := range tests {
		got := extractIPFromMultiaddr(tc.ma)
		if got != tc.want {
			t.Fatalf("extractIPFromMultiaddr(%q) = %q, want %q", tc.ma, got, tc.want)
		}
	}
}

func TestLibP2PProviderName(t *testing.T) {
	p := &p2pLibP2PProvider{}
	if p.Name() != p2pDiscoveryLibP2P {
		t.Fatalf("unexpected provider name: %s", p.Name())
	}
}

// ── Epic 8: chunking ──────────────────────────────────────────────────────────

func TestBuildChunkManifestSingleChunk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "small.bin")
	if err := writeTestFile(path, 512); err != nil {
		t.Fatal(err)
	}
	manifest, err := buildChunkManifest(context.Background(), path, "ARTID-1", defaultChunkSizeBytes, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.TotalChunks != 1 {
		t.Fatalf("expected 1 chunk, got %d", manifest.TotalChunks)
	}
	if manifest.TotalSize != 512 {
		t.Fatalf("expected totalSize=512, got %d", manifest.TotalSize)
	}
	if manifest.ArtifactID != "ARTID-1" {
		t.Fatalf("expected artifactId=ARTID-1, got %s", manifest.ArtifactID)
	}
}

func TestBuildChunkManifestMultipleChunks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.bin")
	if err := writeTestFile(path, 3*1024*1024); err != nil {
		t.Fatal(err)
	}
	manifest, err := buildChunkManifest(context.Background(), path, "", int64(minChunkSizeBytes), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.TotalChunks != 3 {
		t.Fatalf("expected 3 chunks, got %d", manifest.TotalChunks)
	}
	for i, c := range manifest.Chunks {
		if strings.TrimSpace(c.SHA256) == "" {
			t.Fatalf("chunk %d has empty SHA256", i)
		}
		if c.Index != i {
			t.Fatalf("chunk %d has wrong index %d", i, c.Index)
		}
	}
}

func TestBuildChunkManifestRemainder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "uneven.bin")
	size := int64(2*1024*1024 + 512*1024)
	if err := writeTestFile(path, size); err != nil {
		t.Fatal(err)
	}
	manifest, err := buildChunkManifest(context.Background(), path, "", int64(minChunkSizeBytes), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.TotalChunks != 3 {
		t.Fatalf("expected 3 chunks, got %d", manifest.TotalChunks)
	}
	lastChunk := manifest.Chunks[2]
	if lastChunk.Size != 512*1024 {
		t.Fatalf("last chunk size = %d, want %d", lastChunk.Size, 512*1024)
	}
}

func TestBuildChunkManifestChecksumConsistent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.bin")
	if err := writeTestFile(path, 4*1024*1024); err != nil {
		t.Fatal(err)
	}
	m1, _ := buildChunkManifest(context.Background(), path, "", int64(minChunkSizeBytes), nil, nil)
	m2, _ := buildChunkManifest(context.Background(), path, "", int64(minChunkSizeBytes), nil, nil)
	if m1.SHA256 != m2.SHA256 {
		t.Fatal("manifest SHA256 must be deterministic")
	}
}

func TestChunkSizeBytesNormalization(t *testing.T) {
	cfg := normalizeP2PConfig(P2PConfig{TempTTLHours: 168, ChunkSizeBytes: 0})
	if cfg.ChunkSizeBytes != defaultChunkSizeBytes {
		t.Fatalf("expected default %d, got %d", defaultChunkSizeBytes, cfg.ChunkSizeBytes)
	}
	cfg = normalizeP2PConfig(P2PConfig{TempTTLHours: 168, ChunkSizeBytes: 100})
	if cfg.ChunkSizeBytes != minChunkSizeBytes {
		t.Fatalf("expected min %d, got %d", minChunkSizeBytes, cfg.ChunkSizeBytes)
	}
	cfg = normalizeP2PConfig(P2PConfig{TempTTLHours: 168, ChunkSizeBytes: 4 * 1024 * 1024})
	if cfg.ChunkSizeBytes != 4*1024*1024 {
		t.Fatalf("expected 4MB, got %d", cfg.ChunkSizeBytes)
	}
}

// writeTestFile creates a file filled with sequential bytes of a given size.
func writeTestFile(path string, size int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, 4096)
	for i := range buf {
		buf[i] = byte(i)
	}
	var written int64
	for written < size {
		n := int64(len(buf))
		if written+n > size {
			n = size - written
		}
		if _, err := f.Write(buf[:n]); err != nil {
			return err
		}
		written += n
	}
	return nil
}
