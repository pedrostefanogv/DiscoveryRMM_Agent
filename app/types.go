package app

import (
	"sync"
	"time"

	appstore "discovery/app/appstore"
	appautomation "discovery/app/automation"
	debug "discovery/app/debug"
	p2pmeta "discovery/app/p2pmeta"
	supportmeta "discovery/app/supportmeta"
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
	// TrayIcon holds the embedded ICO bytes for the system tray icon.
	// Pass the icon from the root package where //go:embed is allowed.
	TrayIcon []byte
}

// RuntimeFlags are exposed to the frontend to control runtime-only UI behavior.
type RuntimeFlags struct {
	DebugMode      bool `json:"debugMode"`
	StartMinimized bool `json:"startMinimized"`
}

const (
	P2PModeLibp2pOnly = p2pmeta.ModeLibp2pOnly
)

type P2PConfig = p2pmeta.Config

type P2PBootstrapConfig = p2pmeta.BootstrapConfig

type P2PSeedPlan = p2pmeta.SeedPlan

type P2PSeedPlanRecommendation = p2pmeta.SeedPlanRecommendation

type P2PDebugStatus = p2pmeta.DebugStatus

type P2PPeerView = p2pmeta.PeerView

type P2PPeerArtifactIndexView = p2pmeta.PeerArtifactIndexView

type P2PArtifactAvailabilityView = p2pmeta.ArtifactAvailabilityView

type P2PArtifactAccess = p2pmeta.ArtifactAccess

type P2PArtifactView = p2pmeta.ArtifactView

type DebugConfig = debug.Config

type InstallerConfig = debug.InstallerConfig

type AgentStatus = debug.AgentStatus

type RealtimeStatus = debug.RealtimeStatus

func CanonicalArtifactID(artifactID, artifactName, sourceURL string) string {
	return p2pmeta.CanonicalArtifactID(artifactID, artifactName, sourceURL)
}

type P2PMetrics = p2pmeta.Metrics

type P2PTelemetryPayload = p2pmeta.TelemetryPayload

type P2PDistributionStatus = p2pmeta.DistributionStatus

type P2PAuditEvent = p2pmeta.AuditEvent

type P2POnboardingRequest = p2pmeta.OnboardingRequest

type P2POnboardingResult = p2pmeta.OnboardingResult

type P2POnboardingAuditEvent = p2pmeta.OnboardingAuditEvent

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

func (c *inventoryCache) Get() (models.InventoryReport, bool) {
	return c.get()
}

func (c *inventoryCache) Set(r models.InventoryReport) {
	c.set(r)
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

type AgentInfo = supportmeta.AgentInfo

type APIWorkflowState = supportmeta.APIWorkflowState

type TicketPriority = supportmeta.TicketPriority

type APITicket = supportmeta.APITicket

type TicketComment = supportmeta.TicketComment

type CreateTicketInput = supportmeta.CreateTicketInput

type CloseTicketInput = supportmeta.CloseTicketInput

type KnowledgeArticle = supportmeta.KnowledgeArticle

// Automation* aliases keep the app public surface stable while types move into a dedicated subpackage.
type AutomationTaskView = appautomation.TaskView

type AutomationExecutionView = appautomation.ExecutionView

// AutomationStateView represents the current automation policy state in the UI.
type AutomationStateView = appautomation.StateView

// agentInfoCache caches the agent identifiers resolved from /api/agent-auth/me.
type agentInfoCache struct {
	inner supportmeta.AgentInfoCache
}

// AppStore* aliases keep the app surface stable while types move into a dedicated subpackage.
type AppStoreInstallationType = appstore.InstallationType

const (
	AppStoreInstallationWinget     = appstore.InstallationWinget
	AppStoreInstallationChocolatey = appstore.InstallationChocolatey
)

type AppStoreItem = appstore.Item

type AppStoreResponse = appstore.Response

type AppStoreEffectivePolicy = appstore.EffectivePolicy

type appStorePolicyCache struct {
	inner appstore.PolicyCache
}

func (c *appStorePolicyCache) get(maxAge time.Duration) (AppStoreEffectivePolicy, bool) {
	return c.inner.Get(maxAge)
}

func (c *appStorePolicyCache) set(policy AppStoreEffectivePolicy) {
	c.inner.Set(policy)
}

func (c *appStorePolicyCache) invalidate() {
	c.inner.Invalidate()
}

func (c *appStorePolicyCache) Get(maxAge time.Duration) (AppStoreEffectivePolicy, bool) {
	return c.inner.Get(maxAge)
}

func (c *appStorePolicyCache) Set(policy AppStoreEffectivePolicy) {
	c.inner.Set(policy)
}

func (c *appStorePolicyCache) Invalidate() {
	c.inner.Invalidate()
}

func (c *agentInfoCache) get() (AgentInfo, bool) {
	return c.inner.Get()
}

func (c *agentInfoCache) set(info AgentInfo) {
	c.inner.Set(info)
}

func (c *agentInfoCache) invalidate() {
	c.inner.Invalidate()
}

func (c *agentInfoCache) Get() (AgentInfo, bool) {
	return c.inner.Get()
}

func (c *agentInfoCache) Set(info AgentInfo) {
	c.inner.Set(info)
}

func (c *agentInfoCache) Invalidate() {
	c.inner.Invalidate()
}
