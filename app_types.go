package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"discovery/internal/models"
)

// inventoryCache manages thread-safe caching of the last inventory report.
type inventoryCache struct {
	mu     sync.RWMutex
	report models.InventoryReport
	loaded bool
}

// AppStartupOptions controls transient runtime behavior for each execution.
type AppStartupOptions struct {
	DebugMode      bool
	StartMinimized bool
}

// RuntimeFlags are exposed to the frontend to control runtime-only UI behavior.
type RuntimeFlags struct {
	DebugMode      bool `json:"debugMode"`
	StartMinimized bool `json:"startMinimized"`
}

// P2PConfig controls debug-only peer discovery and local transfer behavior.
type P2PConfig struct {
	Enabled                  bool   `json:"enabled"`
	DiscoveryMode            string `json:"discoveryMode"`
	TempTTLHours             int    `json:"tempTtlHours"`
	SeedPercent              int    `json:"seedPercent"`
	MinSeeds                 int    `json:"minSeeds"`
	HTTPListenPortRangeStart int    `json:"httpListenPortRangeStart"`
	HTTPListenPortRangeEnd   int    `json:"httpListenPortRangeEnd"`
	AuthTokenRotationMinutes int    `json:"authTokenRotationMinutes"`
	SharedSecret             string `json:"sharedSecret,omitempty"`
}

// P2PSeedPlan summarizes how many agents should seed from external HTTP.
type P2PSeedPlan struct {
	TotalAgents       int `json:"totalAgents"`
	ConfiguredPercent int `json:"configuredPercent"`
	MinSeeds          int `json:"minSeeds"`
	SelectedSeeds     int `json:"selectedSeeds"`
}

// P2PDebugStatus contains coordinator state shown in debug UI.
type P2PDebugStatus struct {
	Active               bool        `json:"active"`
	DiscoveryMode        string      `json:"discoveryMode"`
	KnownPeers           int         `json:"knownPeers"`
	ListenAddress        string      `json:"listenAddress"`
	TempDir              string      `json:"tempDir"`
	TempTTLHours         int         `json:"tempTtlHours"`
	LastCleanupUTC       string      `json:"lastCleanupUtc"`
	LastDiscoveryTickUTC string      `json:"lastDiscoveryTickUtc"`
	LastError            string      `json:"lastError"`
	CurrentSeedPlan      P2PSeedPlan `json:"currentSeedPlan"`
	Metrics              P2PMetrics  `json:"metrics"`
}

// P2PPeerView is the frontend-facing snapshot of a discovered peer.
type P2PPeerView struct {
	AgentID      string `json:"agentId"`
	Host         string `json:"host"`
	Address      string `json:"address"`
	Port         int    `json:"port"`
	Source       string `json:"source"`
	LastSeenUTC  string `json:"lastSeenUtc"`
	KnownPeers   int    `json:"knownPeers"`
	ConnectedVia string `json:"connectedVia"`
}

// P2PPeerArtifactIndexView is the known artifact inventory announced by a peer.
type P2PPeerArtifactIndexView struct {
	PeerAgentID    string            `json:"peerAgentId"`
	LastUpdatedUTC string            `json:"lastUpdatedUtc"`
	Source         string            `json:"source"`
	Artifacts      []P2PArtifactView `json:"artifacts"`
}

// P2PArtifactAvailabilityView summarizes which peers can currently provide an artifact.
type P2PArtifactAvailabilityView struct {
	ArtifactID   string   `json:"artifactId"`
	ArtifactName string   `json:"artifactName"`
	Found        bool     `json:"found"`
	PeerAgentIDs []string `json:"peerAgentIds"`
	PeerCount    int      `json:"peerCount"`
}

// P2PArtifactAccess is an authenticated one-shot descriptor for peer downloads.
type P2PArtifactAccess struct {
	ArtifactID     string `json:"artifactId"`
	ArtifactName   string `json:"artifactName"`
	URL            string `json:"url"`
	ChecksumSHA256 string `json:"checksumSha256"`
	SizeBytes      int64  `json:"sizeBytes"`
	ExpiresAtUTC   string `json:"expiresAtUtc"`
}

// P2PArtifactView describes an artifact available in the local temporary cache.
type P2PArtifactView struct {
	ArtifactID       string `json:"artifactId"`
	ArtifactName     string `json:"artifactName"`
	Version          string `json:"version,omitempty"`
	SizeBytes        int64  `json:"sizeBytes"`
	ModifiedAtUTC    string `json:"modifiedAtUtc"`
	ChecksumSHA256   string `json:"checksumSha256"`
	Available        bool   `json:"available"`
	LastHeartbeatUTC string `json:"lastHeartbeatUtc"`
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

// P2PMetrics captures debug-visible transfer and replication counters.
type P2PMetrics struct {
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
}

// P2PAuditEvent records important operational events for the P2P debug window.
type P2PAuditEvent struct {
	TimestampUTC string `json:"timestampUtc"`
	Action       string `json:"action"`
	ArtifactName string `json:"artifactName,omitempty"`
	PeerAgentID  string `json:"peerAgentId,omitempty"`
	Source       string `json:"source,omitempty"`
	Success      bool   `json:"success"`
	Message      string `json:"message"`
}

func (c *inventoryCache) get() (models.InventoryReport, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.report, c.loaded
}

func (c *inventoryCache) set(r models.InventoryReport) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.report = r
	c.loaded = true
}

// exportConfig holds the current export options.
type exportConfig struct {
	mu     sync.RWMutex
	redact bool
}

func (e *exportConfig) get() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.redact
}

func (e *exportConfig) set(v bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.redact = v
}

// AgentInfo holds key identifiers resolved from the server for the connected agent.
type AgentInfo struct {
	AgentID  string `json:"agentId"`
	ClientID string `json:"clientId"`
	SiteID   string `json:"siteId"`
	Hostname string `json:"hostname"`
	Name     string `json:"displayName"`
}

// APIWorkflowState is the workflow state embedded in a ticket response.
type APIWorkflowState struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Color        string `json:"color"`
	IsInitial    bool   `json:"isInitial"`
	IsFinal      bool   `json:"isFinal"`
	DisplayOrder int    `json:"displayOrder"`
}

func (w *APIWorkflowState) UnmarshalJSON(data []byte) error {
	type alias APIWorkflowState
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	var out alias
	out.ID = strings.TrimSpace(fmt.Sprint(raw["id"]))
	out.Name = strings.TrimSpace(fmt.Sprint(raw["name"]))
	out.Color = strings.TrimSpace(fmt.Sprint(raw["color"]))
	out.IsInitial = toBool(raw["isInitial"], raw["initial"])
	out.IsFinal = toBool(raw["isFinal"], raw["final"], raw["isTerminal"])
	out.DisplayOrder = toInt(raw["displayOrder"], raw["order"], raw["sortOrder"], raw["position"])
	*w = APIWorkflowState(out)
	return nil
}

// TicketPriority normalizes priority values from API responses.
// The backend may return integer (1..4) or enum strings (Low..Critical).
type TicketPriority int

func (p *TicketPriority) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*p = TicketPriority(0)
		return nil
	}

	var n int
	if err := json.Unmarshal(data, &n); err == nil {
		*p = TicketPriority(normalizePriority(n))
		return nil
	}

	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*p = TicketPriority(priorityLabelToInt(s))
		return nil
	}

	return fmt.Errorf("prioridade inválida")
}

// APITicket is the ticket representation returned by the remote API.
type APITicket struct {
	ID            string            `json:"id"`
	Title         string            `json:"title"`
	Description   string            `json:"description"`
	Priority      TicketPriority    `json:"priority"`
	Category      *string           `json:"category,omitempty"`
	AgentID       *string           `json:"agentId,omitempty"`
	ClientID      string            `json:"clientId"`
	SiteID        *string           `json:"siteId,omitempty"`
	CreatedAt     string            `json:"createdAt"`
	WorkflowState *APIWorkflowState `json:"workflowState,omitempty"`
	Rating        *int              `json:"rating,omitempty"`
	RatedAt       *string           `json:"ratedAt,omitempty"`
	RatedBy       *string           `json:"ratedBy,omitempty"`
}

// TicketComment is a comment on a ticket.
type TicketComment struct {
	ID         string `json:"id"`
	Author     string `json:"author"`
	Content    string `json:"content"`
	IsInternal bool   `json:"isInternal"`
	CreatedAt  string `json:"createdAt"`
}

// CreateTicketInput is the frontend-facing request to create a ticket.
type CreateTicketInput struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Priority    int    `json:"priority"` // 1=Baixa 2=Média 3=Alta 4=Crítica
	Category    string `json:"category"`
}

// CloseTicketInput is the frontend-facing request to close a ticket.
type CloseTicketInput struct {
	Rating          *int   `json:"rating,omitempty"`
	Comment         string `json:"comment,omitempty"`
	WorkflowStateID string `json:"workflowStateId,omitempty"`
}

// KnowledgeArticle represents a knowledge base article for support guidance.
type KnowledgeArticle struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Category    string   `json:"category"`
	Summary     string   `json:"summary"`
	Content     string   `json:"content"`
	Tags        []string `json:"tags"`
	Author      string   `json:"author"`
	Scope       string   `json:"scope"`
	PublishedAt string   `json:"publishedAt"`
	Difficulty  string   `json:"difficulty"`
	ReadTimeMin int      `json:"readTimeMin"`
	UpdatedAt   string   `json:"updatedAt"`
}

// AutomationTaskView is the frontend representation of a resolved automation task.
type AutomationTaskView struct {
	CommandID             string   `json:"commandId,omitempty"`
	TaskID                string   `json:"taskId"`
	Name                  string   `json:"name"`
	Description           string   `json:"description,omitempty"`
	ActionType            string   `json:"actionType"`
	ActionLabel           string   `json:"actionLabel"`
	InstallationType      string   `json:"installationType,omitempty"`
	InstallationLabel     string   `json:"installationLabel,omitempty"`
	PackageID             string   `json:"packageId,omitempty"`
	ScriptID              string   `json:"scriptId,omitempty"`
	ScriptName            string   `json:"scriptName,omitempty"`
	ScriptVersion         string   `json:"scriptVersion,omitempty"`
	ScriptType            string   `json:"scriptType,omitempty"`
	ScriptTypeLabel       string   `json:"scriptTypeLabel,omitempty"`
	CommandPayload        string   `json:"commandPayload,omitempty"`
	ScopeType             string   `json:"scopeType"`
	ScopeLabel            string   `json:"scopeLabel"`
	RequiresApproval      bool     `json:"requiresApproval"`
	TriggerImmediate      bool     `json:"triggerImmediate"`
	TriggerRecurring      bool     `json:"triggerRecurring"`
	TriggerOnUserLogin    bool     `json:"triggerOnUserLogin"`
	TriggerOnAgentCheckIn bool     `json:"triggerOnAgentCheckIn"`
	ScheduleCron          string   `json:"scheduleCron,omitempty"`
	IncludeTags           []string `json:"includeTags,omitempty"`
	ExcludeTags           []string `json:"excludeTags,omitempty"`
	LastUpdatedAt         string   `json:"lastUpdatedAt,omitempty"`
}

type AutomationExecutionView struct {
	ExecutionID        string `json:"executionId"`
	CommandID          string `json:"commandId,omitempty"`
	TaskID             string `json:"taskId,omitempty"`
	TaskName           string `json:"taskName,omitempty"`
	ActionType         string `json:"actionType,omitempty"`
	ActionLabel        string `json:"actionLabel,omitempty"`
	InstallationType   string `json:"installationType,omitempty"`
	InstallationLabel  string `json:"installationLabel,omitempty"`
	SourceType         string `json:"sourceType,omitempty"`
	SourceLabel        string `json:"sourceLabel,omitempty"`
	TriggerType        string `json:"triggerType,omitempty"`
	TriggerLabel       string `json:"triggerLabel,omitempty"`
	Status             string `json:"status"`
	StatusLabel        string `json:"statusLabel"`
	Success            bool   `json:"success"`
	ExitCode           int    `json:"exitCode"`
	ExitCodeSet        bool   `json:"exitCodeSet"`
	ErrorMessage       string `json:"errorMessage,omitempty"`
	Output             string `json:"output,omitempty"`
	PackageID          string `json:"packageId,omitempty"`
	ScriptID           string `json:"scriptId,omitempty"`
	CorrelationID      string `json:"correlationId,omitempty"`
	StartedAt          string `json:"startedAt,omitempty"`
	FinishedAt         string `json:"finishedAt,omitempty"`
	MetadataJSON       string `json:"metadataJson,omitempty"`
	DurationLabel      string `json:"durationLabel,omitempty"`
	SummaryLine        string `json:"summaryLine,omitempty"`
	HasPendingCallback bool   `json:"hasPendingCallback"`
}

// AutomationStateView represents the current automation policy state in the UI.
type AutomationStateView struct {
	Available            bool                      `json:"available"`
	Connected            bool                      `json:"connected"`
	LoadedFromCache      bool                      `json:"loadedFromCache"`
	UpToDate             bool                      `json:"upToDate"`
	IncludeScriptContent bool                      `json:"includeScriptContent"`
	PolicyFingerprint    string                    `json:"policyFingerprint,omitempty"`
	GeneratedAt          string                    `json:"generatedAt,omitempty"`
	LastSyncAt           string                    `json:"lastSyncAt,omitempty"`
	LastAttemptAt        string                    `json:"lastAttemptAt,omitempty"`
	LastError            string                    `json:"lastError,omitempty"`
	CorrelationID        string                    `json:"correlationId,omitempty"`
	TaskCount            int                       `json:"taskCount"`
	PendingCallbacks     int                       `json:"pendingCallbacks"`
	Tasks                []AutomationTaskView      `json:"tasks,omitempty"`
	RecentExecutions     []AutomationExecutionView `json:"recentExecutions,omitempty"`
}

// agentInfoCache caches the agent identifiers resolved from /api/agent-auth/me.
type agentInfoCache struct {
	mu     sync.RWMutex
	info   AgentInfo
	loaded bool
}

// AppStoreInstallationType representa os tipos suportados no app-store do agent.
type AppStoreInstallationType string

const (
	AppStoreInstallationWinget     AppStoreInstallationType = "Winget"
	AppStoreInstallationChocolatey AppStoreInstallationType = "Chocolatey"
)

// AppStoreItem representa um item permitido retornado por /api/agent-auth/me/app-store.
type AppStoreItem struct {
	InstallationType    string            `json:"installationType"`
	PackageID           string            `json:"packageId"`
	Name                string            `json:"name"`
	Description         string            `json:"description"`
	IconURL             string            `json:"iconUrl"`
	Publisher           string            `json:"publisher"`
	Version             string            `json:"version"`
	InstallCommand      string            `json:"installCommand"`
	InstallerURLsByArch map[string]string `json:"installerUrlsByArch"`
	AutoUpdateEnabled   bool              `json:"autoUpdateEnabled"`
	SourceScope         string            `json:"sourceScope"`
}

// AppStoreResponse representa o envelope do endpoint /api/agent-auth/me/app-store.
type AppStoreResponse struct {
	InstallationType string         `json:"installationType"`
	Count            int            `json:"count"`
	Items            []AppStoreItem `json:"items"`
}

// AppStoreEffectivePolicy consolida os itens permitidos para todos os tipos suportados.
type AppStoreEffectivePolicy struct {
	Items     []AppStoreItem `json:"items"`
	FetchedAt string         `json:"fetchedAt"`
}

type appStorePolicyCache struct {
	mu       sync.RWMutex
	policy   AppStoreEffectivePolicy
	loadedAt time.Time
	loaded   bool
}

func (c *appStorePolicyCache) get(maxAge time.Duration) (AppStoreEffectivePolicy, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.loaded {
		return AppStoreEffectivePolicy{}, false
	}
	if maxAge > 0 && time.Since(c.loadedAt) > maxAge {
		return AppStoreEffectivePolicy{}, false
	}
	return c.policy, true
}

func (c *appStorePolicyCache) set(policy AppStoreEffectivePolicy) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.policy = policy
	c.loadedAt = time.Now()
	c.loaded = true
}

func (c *appStorePolicyCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.policy = AppStoreEffectivePolicy{}
	c.loadedAt = time.Time{}
	c.loaded = false
}

func (c *agentInfoCache) get() (AgentInfo, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.info, c.loaded
}

func (c *agentInfoCache) set(info AgentInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.info = info
	c.loaded = true
}

func (c *agentInfoCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loaded = false
}
