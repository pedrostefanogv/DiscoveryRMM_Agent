package p2pmeta

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

const (
	ModeLibp2pOnly = "libp2p_only"
)

type BootstrapConfig struct {
	BootstrapPeers        []string `json:"bootstrapPeers,omitempty"`
	PreferLAN             bool     `json:"preferLan"`
	CloudBootstrapEnabled bool     `json:"cloudBootstrapEnabled,omitempty"`
}

type Config struct {
	Enabled                  bool            `json:"enabled"`
	DiscoveryMode            string          `json:"discoveryMode"`
	P2PMode                  string          `json:"p2pMode,omitempty"`
	TempTTLHours             int             `json:"tempTtlHours"`
	SeedPercent              int             `json:"seedPercent"`
	MinSeeds                 int             `json:"minSeeds"`
	HTTPListenPortRangeStart int             `json:"httpListenPortRangeStart"`
	HTTPListenPortRangeEnd   int             `json:"httpListenPortRangeEnd"`
	AuthTokenRotationMinutes int             `json:"authTokenRotationMinutes"`
	SharedSecret             string          `json:"sharedSecret,omitempty"`
	ChunkSizeBytes           int64           `json:"chunkSizeBytes,omitempty"`
	MaxBandwidthBytesPerSec  int64           `json:"maxBandwidthBytesPerSec,omitempty"`
	BootstrapConfig          BootstrapConfig `json:"bootstrapConfig,omitempty"`
}

type SeedPlan struct {
	TotalAgents       int `json:"totalAgents"`
	ConfiguredPercent int `json:"configuredPercent"`
	MinSeeds          int `json:"minSeeds"`
	SelectedSeeds     int `json:"selectedSeeds"`
}

// UnmarshalJSON accepts both the documented camelCase payload and the
// PascalCase payload currently returned by the server.
func (p *SeedPlan) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	var result SeedPlan
	if err := decodeP2PJSONField(raw, &result.TotalAgents, "totalAgents", "TotalAgents"); err != nil {
		return err
	}
	if err := decodeP2PJSONField(raw, &result.ConfiguredPercent, "configuredPercent", "ConfiguredPercent"); err != nil {
		return err
	}
	if err := decodeP2PJSONField(raw, &result.MinSeeds, "minSeeds", "MinSeeds"); err != nil {
		return err
	}
	if err := decodeP2PJSONField(raw, &result.SelectedSeeds, "selectedSeeds", "SelectedSeeds"); err != nil {
		return err
	}

	*p = result
	return nil
}

type SeedPlanRecommendation struct {
	SiteID         string   `json:"siteId,omitempty"`
	GeneratedAtUTC string   `json:"generatedAtUtc,omitempty"`
	Plan           SeedPlan `json:"plan"`
}

// UnmarshalJSON accepts both camelCase and PascalCase field names for the
// seed-plan recommendation envelope.
func (r *SeedPlanRecommendation) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	var result SeedPlanRecommendation
	if err := decodeP2PJSONField(raw, &result.SiteID, "siteId", "siteID", "SiteId", "SiteID"); err != nil {
		return err
	}
	if err := decodeP2PJSONField(raw, &result.GeneratedAtUTC, "generatedAtUtc", "generatedAtUTC", "GeneratedAtUtc", "GeneratedAtUTC"); err != nil {
		return err
	}
	if err := decodeP2PJSONField(raw, &result.Plan, "plan", "Plan"); err != nil {
		return err
	}

	*r = result
	return nil
}

type DebugStatus struct {
	Active               bool     `json:"active"`
	DiscoveryMode        string   `json:"discoveryMode"`
	KnownPeers           int      `json:"knownPeers"`
	ListenAddress        string   `json:"listenAddress"`
	TempDir              string   `json:"tempDir"`
	TempTTLHours         int      `json:"tempTtlHours"`
	LastCleanupUTC       string   `json:"lastCleanupUtc"`
	LastDiscoveryTickUTC string   `json:"lastDiscoveryTickUtc"`
	LastError            string   `json:"lastError"`
	CurrentSeedPlan      SeedPlan `json:"currentSeedPlan"`
	Metrics              Metrics  `json:"metrics"`
}

type PeerView struct {
	AgentID      string `json:"agentId"`
	Host         string `json:"host"`
	Address      string `json:"address"`
	Port         int    `json:"port"`
	Source       string `json:"source"`
	LastSeenUTC  string `json:"lastSeenUtc"`
	KnownPeers   int    `json:"knownPeers"`
	ConnectedVia string `json:"connectedVia"`
}

type ArtifactView struct {
	ArtifactID       string `json:"artifactId"`
	ArtifactName     string `json:"artifactName"`
	Version          string `json:"version,omitempty"`
	SizeBytes        int64  `json:"sizeBytes"`
	ModifiedAtUTC    string `json:"modifiedAtUtc"`
	ChecksumSHA256   string `json:"checksumSha256"`
	Available        bool   `json:"available"`
	LastHeartbeatUTC string `json:"lastHeartbeatUtc"`
}

type PeerArtifactIndexView struct {
	PeerAgentID    string         `json:"peerAgentId"`
	LastUpdatedUTC string         `json:"lastUpdatedUtc"`
	Source         string         `json:"source"`
	Artifacts      []ArtifactView `json:"artifacts"`
}

type ArtifactAvailabilityView struct {
	ArtifactID   string   `json:"artifactId"`
	ArtifactName string   `json:"artifactName"`
	Found        bool     `json:"found"`
	PeerAgentIDs []string `json:"peerAgentIds"`
	PeerCount    int      `json:"peerCount"`
}

type ArtifactAccess struct {
	ArtifactID     string `json:"artifactId"`
	ArtifactName   string `json:"artifactName"`
	URL            string `json:"url"`
	ChecksumSHA256 string `json:"checksumSha256"`
	SizeBytes      int64  `json:"sizeBytes"`
	ExpiresAtUTC   string `json:"expiresAtUtc"`
}

type Metrics struct {
	PublishedArtifacts    int   `json:"publishedArtifacts"`
	ReplicationsStarted   int   `json:"replicationsStarted"`
	ReplicationsSucceeded int   `json:"replicationsSucceeded"`
	ReplicationsFailed    int   `json:"replicationsFailed"`
	BytesServed           int64 `json:"bytesServed"`
	BytesDownloaded       int64 `json:"bytesDownloaded"`
	QueuedReplications    int   `json:"queuedReplications"`
	ActiveReplications    int   `json:"activeReplications"`
	AutoDistributionRuns  int   `json:"autoDistributionRuns"`
	CatalogRefreshRuns    int   `json:"catalogRefreshRuns"`
	ChunkedDownloads      int   `json:"chunkedDownloads"`
	ChunksDownloaded      int64 `json:"chunksDownloaded"`
}

type TelemetryPayload struct {
	AgentID         string   `json:"agentId,omitempty"`
	SiteID          string   `json:"siteId,omitempty"`
	CollectedAtUTC  string   `json:"collectedAtUtc"`
	Metrics         Metrics  `json:"metrics"`
	CurrentSeedPlan SeedPlan `json:"currentSeedPlan"`
}

type DistributionStatus struct {
	ArtifactID     string   `json:"artifactId"`
	ArtifactName   string   `json:"artifactName,omitempty"`
	PeerCount      int      `json:"peerCount"`
	PeerAgentIDs   []string `json:"peerAgentIds,omitempty"`
	LastUpdatedUTC string   `json:"lastUpdatedUtc,omitempty"`
}

type AuditEvent struct {
	TimestampUTC string `json:"timestampUtc"`
	Action       string `json:"action"`
	ArtifactName string `json:"artifactName,omitempty"`
	PeerAgentID  string `json:"peerAgentId,omitempty"`
	Source       string `json:"source,omitempty"`
	Success      bool   `json:"success"`
	Message      string `json:"message"`
}

type OnboardingRequest struct {
	ServerURL    string `json:"serverUrl"`
	DeployKey    string `json:"deployKey"`
	ExpiresAtUTC string `json:"expiresAtUtc"`
	SourceAgent  string `json:"sourceAgent"`
	Nonce        string `json:"nonce"`
	Signature    string `json:"signature"`
}

type OnboardingResult struct {
	AgentID    string `json:"agentId"`
	Registered bool   `json:"registered"`
	Message    string `json:"message"`
}

type OnboardingAuditEvent struct {
	TimestampUTC  string `json:"timestampUtc"`
	SourceAgentID string `json:"sourceAgentId"`
	TargetAgentID string `json:"targetAgentId,omitempty"`
	ServerURL     string `json:"serverUrl"`
	Success       bool   `json:"success"`
	Message       string `json:"message"`
}

// ProvisioningTokenResponse é o payload retornado pelo servidor em
// POST /api/v1/agent-auth/me/zero-touch/deploy-token.
// O campo Token (prefixo mdz_zt_...) é o valor bruto single-use que o peer
// usa como Bearer token no endpoint de registro — nunca é armazenado no banco,
// apenas o seu hash SHA-256. TokenID é o GUID do registro na tabela
// deploy_tokens, útil para rastreamento/revogação administrativa.
type ProvisioningTokenResponse struct {
	Token     string `json:"token"`
	TokenID   string `json:"tokenId"`
	ExpiresAt string `json:"expiresAt"` // RFC3339
	MaxUses   int    `json:"maxUses"`
}

// AutoProvisioningStats agrega contadores e eventos de auditoria do lado do
// agente que realizou provisionamentos (peer configurado que entregou ofertas).
type AutoProvisioningStats struct {
	Enabled          bool                   `json:"enabled"`
	TotalProvisioned int64                  `json:"totalProvisioned"`
	RecentEvents     []OnboardingAuditEvent `json:"recentEvents"`
}

type CachedSeedPlan struct {
	Plan         SeedPlanRecommendation
	FetchedAtUTC time.Time
}

func decodeP2PJSONField[T any](raw map[string]json.RawMessage, out *T, keys ...string) error {
	for _, key := range keys {
		data, ok := raw[key]
		if !ok {
			continue
		}
		if err := json.Unmarshal(data, out); err != nil {
			return err
		}
		return nil
	}
	return nil
}

func CanonicalArtifactID(artifactID, artifactName, sourceURL string) string {
	if id := strings.TrimSpace(artifactID); id != "" {
		return id
	}
	if rawURL := strings.TrimSpace(strings.ToLower(sourceURL)); rawURL != "" {
		sum := sha256.Sum256([]byte(rawURL))
		return "urlsha256:" + hex.EncodeToString(sum[:])
	}
	name := sanitizeArtifactName(artifactName)
	if name != "" {
		return "name:" + strings.ToLower(name)
	}
	return ""
}

func sanitizeArtifactName(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return ""
	}
	replacer := strings.NewReplacer("\\", "-", "/", "-", ":", "-", "*", "-", "?", "-", "\"", "-", "<", "-", ">", "-", "|", "-")
	return strings.TrimSpace(replacer.Replace(trimmed))
}
