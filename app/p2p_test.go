package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
	if cfg.DiscoveryMode != p2pDiscoveryMDNS {
		t.Fatalf("discovery mode = %q, want %q", cfg.DiscoveryMode, p2pDiscoveryMDNS)
	}
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
	cfg := P2PConfig{SeedPercent: 10, MinSeeds: 2, DiscoveryMode: p2pDiscoveryMDNS, TempTTLHours: 168}
	plan := buildP2PSeedPlan(25, cfg)
	if plan.SelectedSeeds != 3 {
		t.Fatalf("selected seeds = %d, want 3", plan.SelectedSeeds)
	}
	if plan.TotalAgents != 25 {
		t.Fatalf("total agents = %d, want 25", plan.TotalAgents)
	}
}

func TestListAuditEventsFiltered(t *testing.T) {
	c := &p2pCoordinator{}
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

func TestArtifactPriorityByResource(t *testing.T) {
	c := &p2pCoordinator{}
	high := c.artifactPriority("appstore", "stable", "appstore-catalog-v2.json")
	medium := c.artifactPriority("appstore", "stable", "agent-stable-package.bin")
	low := c.artifactPriority("appstore", "stable", "unrelated-backup.dat")

	if !(high < medium && medium < low) {
		t.Fatalf("unexpected priority order: high=%d medium=%d low=%d", high, medium, low)
	}
}

func TestFindArtifactPeersFromIndex(t *testing.T) {
	c := &p2pCoordinator{
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

func TestResolveP2PTempDir(t *testing.T) {
	windowsPath := resolveP2PTempDir("windows")
	wantWindows := filepath.Join("C:\\", "Windows", "Temp", "Discovery", "P2P_Temp")
	if !strings.EqualFold(filepath.Clean(windowsPath), filepath.Clean(wantWindows)) {
		t.Fatalf("windows path = %q, want %q", windowsPath, wantWindows)
	}

	linuxPath := resolveP2PTempDir("linux")
	wantLinux := filepath.Join(getDataDir(), "TempP2P")
	if linuxPath != wantLinux {
		t.Fatalf("linux path = %q, want %q", linuxPath, wantLinux)
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

func TestComputeOnboardingSignatureConsistent(t *testing.T) {
	sig1 := computeOnboardingSignature("agent-a", "https://server.local", "key123", "2026-01-01T00:00:00Z", "nonce1")
	sig2 := computeOnboardingSignature("agent-a", "https://server.local", "key123", "2026-01-01T00:00:00Z", "nonce1")
	if sig1 != sig2 {
		t.Fatal("signature must be deterministic")
	}
	// Different nonce → different signature (replay prevention).
	sig3 := computeOnboardingSignature("agent-a", "https://server.local", "key123", "2026-01-01T00:00:00Z", "nonce2")
	if sig1 == sig3 {
		t.Fatal("different nonce must produce different signature")
	}
}

func TestBuildOnboardingOfferExpiry(t *testing.T) {
	offer, err := BuildOnboardingOffer("agent-src", "https://srv", "key", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	exp, err := time.Parse(time.RFC3339, offer.ExpiresAtUTC)
	if err != nil {
		t.Fatalf("invalid expiresAt: %v", err)
	}
	if time.Until(exp) < 4*time.Minute || time.Until(exp) > 6*time.Minute {
		t.Fatalf("expiry out of expected range: %s", offer.ExpiresAtUTC)
	}
	// Verify offer self-validates.
	expected := computeOnboardingSignature(offer.SourceAgent, offer.ServerURL, offer.DeployKey, offer.ExpiresAtUTC, offer.Nonce)
	if offer.Signature != expected {
		t.Fatal("offer signature mismatch")
	}
}

func TestApplyOnboardingOfferExpired(t *testing.T) {
	offer := P2POnboardingRequest{
		ServerURL:    "https://srv",
		DeployKey:    "key",
		ExpiresAtUTC: time.Now().UTC().Add(-1 * time.Minute).Format(time.RFC3339),
		SourceAgent:  "agent-src",
		Nonce:        "abc",
		Signature:    "irrelevant",
	}
	a := &App{}
	_, err := a.applyOnboardingOffer(offer)
	if err == nil {
		t.Fatal("expected error for expired offer")
	}
}

func TestApplyOnboardingOfferBadSignature(t *testing.T) {
	expiresAt := time.Now().UTC().Add(10 * time.Minute).Format(time.RFC3339)
	offer := P2POnboardingRequest{
		ServerURL:    "https://srv",
		DeployKey:    "key",
		ExpiresAtUTC: expiresAt,
		SourceAgent:  "agent-src",
		Nonce:        "abc",
		Signature:    "badsig",
	}
	a := &App{}
	_, err := a.applyOnboardingOffer(offer)
	if err == nil {
		t.Fatal("expected error for invalid signature")
	}
}

// ── Epic 3: P2PMode normalization ─────────────────────────────────────────────

func TestP2PModeNormalization(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", P2PModeLegacy},
		{"LEGACY", P2PModeLegacy},
		{"libp2p_only", P2PModeLibp2pOnly},
		{"hybrid", P2PModeHybrid},
		{"unknown_mode", P2PModeLegacy},
	}
	for _, tc := range tests {
		cfg := normalizeP2PConfig(P2PConfig{
			DiscoveryMode: p2pDiscoveryMDNS,
			TempTTLHours:  168,
			P2PMode:       tc.in,
		})
		if cfg.P2PMode != tc.want {
			t.Fatalf("P2PMode(%q) = %q, want %q", tc.in, cfg.P2PMode, tc.want)
		}
	}
}

// ── Epic 7: go-libp2p provider ────────────────────────────────────────────────

func TestPickDiscoveryProviderLegacyMDNS(t *testing.T) {
	cfg := normalizeP2PConfig(P2PConfig{DiscoveryMode: p2pDiscoveryMDNS, TempTTLHours: 168})
	p := pickDiscoveryProvider(cfg, nil, nil)
	if p.Name() != p2pDiscoveryMDNS {
		t.Fatalf("expected mdns provider, got %s", p.Name())
	}
}

func TestPickDiscoveryProviderLegacyUDP(t *testing.T) {
	cfg := normalizeP2PConfig(P2PConfig{DiscoveryMode: p2pDiscoveryUDP, TempTTLHours: 168})
	p := pickDiscoveryProvider(cfg, nil, nil)
	if p.Name() != p2pDiscoveryUDP {
		t.Fatalf("expected udp provider, got %s", p.Name())
	}
}

func TestPickDiscoveryProviderLibP2POnly(t *testing.T) {
	cfg := normalizeP2PConfig(P2PConfig{DiscoveryMode: p2pDiscoveryMDNS, TempTTLHours: 168, P2PMode: P2PModeLibp2pOnly})
	p := pickDiscoveryProvider(cfg, nil, nil)
	if p.Name() != p2pDiscoveryLibP2P {
		t.Fatalf("expected libp2p provider, got %s", p.Name())
	}
}

func TestPickDiscoveryProviderHybrid(t *testing.T) {
	cfg := normalizeP2PConfig(P2PConfig{DiscoveryMode: p2pDiscoveryMDNS, TempTTLHours: 168, P2PMode: P2PModeHybrid})
	p := pickDiscoveryProvider(cfg, nil, nil)
	if p.Name() != "multi" {
		t.Fatalf("expected multi provider in hybrid mode, got %s", p.Name())
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

func TestMultiProviderName(t *testing.T) {
	m := &p2pMultiProvider{providers: []p2pDiscoveryProvider{&p2pMDNSProvider{}, &p2pLibP2PProvider{}}}
	if m.Name() != "multi" {
		t.Fatalf("unexpected multi provider name: %s", m.Name())
	}
}

// ── Epic 8: chunking ──────────────────────────────────────────────────────────

func TestBuildChunkManifestSingleChunk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "small.bin")
	if err := writeTestFile(path, 512); err != nil {
		t.Fatal(err)
	}
	manifest, err := buildChunkManifest(path, "ARTID-1", defaultChunkSizeBytes)
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
	manifest, err := buildChunkManifest(path, "", int64(minChunkSizeBytes))
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
	manifest, err := buildChunkManifest(path, "", int64(minChunkSizeBytes))
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
	m1, _ := buildChunkManifest(path, "", int64(minChunkSizeBytes))
	m2, _ := buildChunkManifest(path, "", int64(minChunkSizeBytes))
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
