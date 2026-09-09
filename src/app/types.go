package app

import (
	appstore "discovery/app/appstore"
	appautomation "discovery/app/automation"
	"discovery/app/coreagent"
	debug "discovery/app/debug"
	p2pmeta "discovery/app/p2pmeta"
	supportmeta "discovery/app/supportmeta"
)

// inventoryCache/exportConfig/agentInfoCache/appStorePolicyCache/logBuffer/
// RuntimeFlags movidos para coreagent (lote 2, §0.8). RuntimeFlags mantém
// alias local com as mesmas tags json (exposto ao frontend).
type RuntimeFlags = coreagent.RuntimeFlags

// AppStartupOptions controls transient runtime behavior for each execution.
type AppStartupOptions struct {
	DebugMode      bool
	StartMinimized bool
	// ServiceMode (PLANO_AGENT_SERVICE_SYSTEM.md, Fase 1): roda o core do
	// agent como serviço SYSTEM — pula tray, janela, SSE de chat, debug HTTP
	// e persistência de logs da UI (usa agent-service.log).
	ServiceMode bool
	// TrayIcon holds the embedded ICO bytes for the system tray icon.
	// Pass the icon from the root package where //go:embed is allowed.
	TrayIcon []byte
	// TrayProvisioningIcon is shown while the agent is waiting for provisioning.
	TrayProvisioningIcon []byte
	// TrayOfflineIcon is shown when the provisioned agent is offline.
	TrayOfflineIcon []byte
}

// RuntimeFlags movido para coreagent.RuntimeFlags (alias acima, mesmas tags json).

const (
	P2PModeLibp2pOnly = p2pmeta.ModeLibp2pOnly
)

type P2PConfig = p2pmeta.Config

type P2PBootstrapConfig = p2pmeta.BootstrapConfig

type P2PChunkManifest = p2pmeta.ChunkManifest

type P2PChunk = p2pmeta.Chunk

type P2PSelfEndpoint = p2pmeta.SelfEndpoint

type P2PDiscoveredPeer = p2pmeta.DiscoveredPeer

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

type P2PHostLoad = p2pmeta.HostLoad

type P2PArtifactPresenceItem = p2pmeta.ArtifactPresenceItem

type P2PDistributionStatus = p2pmeta.DistributionStatus

type P2PAuditEvent = p2pmeta.AuditEvent

type P2POnboardingRequest = p2pmeta.OnboardingRequest

type P2POnboardingResult = p2pmeta.OnboardingResult

type P2POnboardingAuditEvent = p2pmeta.OnboardingAuditEvent

type P2PProvisioningTokenResponse = p2pmeta.ProvisioningTokenResponse

type P2PAutoProvisioningStats = p2pmeta.AutoProvisioningStats

// PsadtAlertAction define um botão de ação para alertas modais PSADT.
type PsadtAlertAction struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// PsadtAlertPayload representa o payload do commandType ShowPsadtAlert (9)
// enviado pelo servidor via ExecuteCommand.
type PsadtAlertPayload struct {
	AlertID         string             `json:"alertId"`
	Type            string             `json:"type"` // "toast" | "modal" | "update-progress"
	Title           string             `json:"title"`
	Message         string             `json:"message"`
	TimeoutSeconds  int                `json:"timeoutSeconds"`  // 0 = padrão
	Icon            string             `json:"icon"`            // "info" | "warning" | "error" | "success" | "question"
	Actions         []PsadtAlertAction `json:"actions"`         // apenas modal
	DefaultAction   string             `json:"defaultAction"`   // ação ao fechar sem clicar (modal)
	ProgressPercent int                `json:"progressPercent"` // 0-100 para update-progress
	StatusText      string             `json:"statusText"`      // texto de status para update-progress
	Subtitle        string             `json:"subtitle"`        // subtítulo para update-progress
}

// exportConfig holds the current export options.
// AppStore* aliases (mantidos do app — movidos para appstore; usados por store.go).
type AppStoreInstallationType = appstore.InstallationType

const (
	AppStoreInstallationWinget     = appstore.InstallationWinget
	AppStoreInstallationChocolatey = appstore.InstallationChocolatey
)

type AppStoreItem = appstore.Item

type AppStoreResponse = appstore.Response

type AppStoreEffectivePolicy = appstore.EffectivePolicy

type AgentInfo = supportmeta.AgentInfo

type APIWorkflowState = supportmeta.APIWorkflowState

type TicketPriority = supportmeta.TicketPriority

type APITicket = supportmeta.APITicket

type TicketComment = supportmeta.TicketComment

type CreateTicketInput = supportmeta.CreateTicketInput

type CloseTicketInput = supportmeta.CloseTicketInput

type KnowledgeArticle = supportmeta.KnowledgeArticle

type KnowledgePage = supportmeta.KnowledgePage

// Automation* aliases keep the app public surface stable while types move into a dedicated subpackage.
type AutomationTaskView = appautomation.TaskView

type AutomationExecutionView = appautomation.ExecutionView

// AutomationStateView represents the current automation policy state in the UI.
type AutomationStateView = appautomation.StateView
