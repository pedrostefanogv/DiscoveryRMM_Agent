package automation

import (
	"context"
	"encoding/json"
)

type AppApprovalScopeType string

const (
	ApprovalScopeGlobal AppApprovalScopeType = "Global"
	ApprovalScopeClient AppApprovalScopeType = "Client"
	ApprovalScopeSite   AppApprovalScopeType = "Site"
	ApprovalScopeAgent  AppApprovalScopeType = "Agent"
)

type AppInstallationType string

const (
	InstallationWinget             AppInstallationType = "Winget"
	InstallationChocolatey         AppInstallationType = "Chocolatey"
	InstallationPSAppDeployToolkit AppInstallationType = "PSAppDeployToolkit"
	InstallationCustom             AppInstallationType = "Custom"
)

type AutomationTaskActionType string

const (
	ActionInstallPackage         AutomationTaskActionType = "InstallPackage"
	ActionUpdatePackage          AutomationTaskActionType = "UpdatePackage"
	ActionRemovePackage          AutomationTaskActionType = "RemovePackage"
	ActionUpdateOrInstallPackage AutomationTaskActionType = "UpdateOrInstallPackage"
	ActionRunScript              AutomationTaskActionType = "RunScript"
	ActionCustomCommand          AutomationTaskActionType = "CustomCommand"
)

type AutomationScriptType string

const (
	ScriptPowerShell AutomationScriptType = "PowerShell"
	ScriptShell      AutomationScriptType = "Shell"
	ScriptPython     AutomationScriptType = "Python"
	ScriptBatch      AutomationScriptType = "Batch"
	ScriptCustom     AutomationScriptType = "Custom"
)

type AutomationExecutionSourceType string

const (
	ExecutionSourceRunNow      AutomationExecutionSourceType = "RunNow"
	ExecutionSourceScheduled   AutomationExecutionSourceType = "Scheduled"
	ExecutionSourceForceSync   AutomationExecutionSourceType = "ForceSync"
	ExecutionSourceAgentManual AutomationExecutionSourceType = "AgentManual"
)

type AutomationExecutionStatus string

const (
	ExecutionStatusDispatched   AutomationExecutionStatus = "Dispatched"
	ExecutionStatusAcknowledged AutomationExecutionStatus = "Acknowledged"
	ExecutionStatusCompleted    AutomationExecutionStatus = "Completed"
	ExecutionStatusFailed       AutomationExecutionStatus = "Failed"
)

type AutomationScriptChangeType string

const (
	ScriptChangeCreated     AutomationScriptChangeType = "Created"
	ScriptChangeUpdated     AutomationScriptChangeType = "Updated"
	ScriptChangeDeleted     AutomationScriptChangeType = "Deleted"
	ScriptChangeConsumed    AutomationScriptChangeType = "Consumed"
	ScriptChangeActivated   AutomationScriptChangeType = "Activated"
	ScriptChangeDeactivated AutomationScriptChangeType = "Deactivated"
)

type AutomationTaskChangeType string

const (
	TaskChangeCreated     AutomationTaskChangeType = "Created"
	TaskChangeUpdated     AutomationTaskChangeType = "Updated"
	TaskChangeDeleted     AutomationTaskChangeType = "Deleted"
	TaskChangeActivated   AutomationTaskChangeType = "Activated"
	TaskChangeDeactivated AutomationTaskChangeType = "Deactivated"
	TaskChangeSynced      AutomationTaskChangeType = "Synced"
)

type PolicySyncRequest struct {
	KnownPolicyFingerprint *string `json:"KnownPolicyFingerprint,omitempty"`
	IncludeScriptContent   *bool   `json:"IncludeScriptContent,omitempty"`
}

type PolicySyncResponse struct {
	UpToDate          bool             `json:"UpToDate"`
	PolicyFingerprint string           `json:"PolicyFingerprint"`
	GeneratedAt       string           `json:"GeneratedAt"`
	TaskCount         int              `json:"TaskCount"`
	Tasks             []AutomationTask `json:"Tasks"`
}

type AutomationTask struct {
	CommandID             string                   `json:"CommandId,omitempty"`
	TaskID                string                   `json:"TaskId"`
	Name                  string                   `json:"Name"`
	Description           string                   `json:"Description,omitempty"`
	ActionType            AutomationTaskActionType `json:"ActionType"`
	InstallationType      AppInstallationType      `json:"InstallationType,omitempty"`
	PackageID             string                   `json:"PackageId,omitempty"`
	ScriptID              string                   `json:"ScriptId,omitempty"`
	CommandPayload        string                   `json:"CommandPayload,omitempty"`
	ScopeType             AppApprovalScopeType     `json:"ScopeType"`
	RequiresApproval      bool                     `json:"RequiresApproval"`
	TriggerImmediate      bool                     `json:"TriggerImmediate"`
	TriggerRecurring      bool                     `json:"TriggerRecurring"`
	TriggerOnUserLogin    bool                     `json:"TriggerOnUserLogin"`
	TriggerOnAgentCheckIn bool                     `json:"TriggerOnAgentCheckIn"`
	ScheduleCron          string                   `json:"ScheduleCron,omitempty"`
	IncludeTags           []string                 `json:"IncludeTags"`
	ExcludeTags           []string                 `json:"ExcludeTags"`
	LastUpdatedAt         string                   `json:"LastUpdatedAt"`
	Script                *AutomationScript        `json:"Script,omitempty"`
}

type AutomationScript struct {
	ScriptID             string               `json:"ScriptId"`
	Name                 string               `json:"Name"`
	Version              string               `json:"Version"`
	Summary              string               `json:"Summary"`
	ScriptType           AutomationScriptType `json:"ScriptType"`
	LastUpdatedAt        string               `json:"LastUpdatedAt"`
	ContentHashSHA256    string               `json:"ContentHashSha256"`
	Content              string               `json:"Content,omitempty"`
	ParametersSchemaJSON string               `json:"ParametersSchemaJson,omitempty"`
	MetadataJSON         string               `json:"MetadataJson,omitempty"`
}

type RuntimeConfig struct {
	BaseURL string
	Token   string
	AgentID string
}

type PersistedPolicy struct {
	Policy               PolicySyncResponse `json:"policy"`
	SavedAt              string             `json:"savedAt"`
	IncludeScriptContent bool               `json:"includeScriptContent"`
}

type State struct {
	Available            bool
	Connected            bool
	LoadedFromCache      bool
	UpToDate             bool
	IncludeScriptContent bool
	PolicyFingerprint    string
	GeneratedAt          string
	LastSyncAt           string
	LastAttemptAt        string
	LastError            string
	CorrelationID        string
	TaskCount            int
	Tasks                []AutomationTask
	PendingCallbacks     int
	RecentExecutions     []ExecutionRecord
}

type CallbackType string

const (
	CallbackTypeAck    CallbackType = "ack"
	CallbackTypeResult CallbackType = "result"
)

type TriggerType string

const (
	TriggerTypeImmediate    TriggerType = "Immediate"
	TriggerTypeRecurring    TriggerType = "Recurring"
	TriggerTypeUserLogin    TriggerType = "UserLogin"
	TriggerTypeAgentCheckIn TriggerType = "AgentCheckIn"
	TriggerTypeManual       TriggerType = "Manual"
)

type ExecutionRecord struct {
	ExecutionID      string                        `json:"executionId"`
	CommandID        string                        `json:"commandId,omitempty"`
	TaskID           string                        `json:"taskId,omitempty"`
	TaskName         string                        `json:"taskName,omitempty"`
	ActionType       AutomationTaskActionType      `json:"actionType,omitempty"`
	InstallationType AppInstallationType           `json:"installationType,omitempty"`
	SourceType       AutomationExecutionSourceType `json:"sourceType,omitempty"`
	TriggerType      TriggerType                   `json:"triggerType,omitempty"`
	Status           AutomationExecutionStatus     `json:"status"`
	Success          bool                          `json:"success"`
	ExitCode         int                           `json:"exitCode"`
	ExitCodeSet      bool                          `json:"exitCodeSet"`
	ErrorMessage     string                        `json:"errorMessage,omitempty"`
	Output           string                        `json:"output,omitempty"`
	PackageID        string                        `json:"packageId,omitempty"`
	ScriptID         string                        `json:"scriptId,omitempty"`
	CorrelationID    string                        `json:"correlationId,omitempty"`
	StartedAt        string                        `json:"startedAt,omitempty"`
	FinishedAt       string                        `json:"finishedAt,omitempty"`
	MetadataJSON     string                        `json:"metadataJson,omitempty"`
}

type AckRequest struct {
	TaskID       string                        `json:"TaskId,omitempty"`
	ScriptID     string                        `json:"ScriptId,omitempty"`
	SourceType   AutomationExecutionSourceType `json:"SourceType,omitempty"`
	MetadataJSON string                        `json:"MetadataJson,omitempty"`
}

type ResultRequest struct {
	TaskID       string                        `json:"TaskId,omitempty"`
	ScriptID     string                        `json:"ScriptId,omitempty"`
	SourceType   AutomationExecutionSourceType `json:"SourceType,omitempty"`
	Success      bool                          `json:"Success"`
	ExitCode     *int                          `json:"ExitCode,omitempty"`
	ErrorMessage string                        `json:"ErrorMessage,omitempty"`
	MetadataJSON string                        `json:"MetadataJson,omitempty"`
}

type ExecutionResult struct {
	Success      bool
	ExitCode     int
	ExitCodeSet  bool
	Output       string
	ErrorMessage string
	MetadataJSON string
}

// PSADTPolicy representa a policy efetiva usada em runtime no executor PSADT.
// Os campos podem vir vazios; o executor aplica defaults seguros.
type PSADTPolicy struct {
	RequiredVersion         string
	ExecutionTimeoutSeconds int
	SuccessExitCodes        []int
	RebootExitCodes         []int
	IgnoreExitCodes         []int
	FallbackPolicy          string
	TimeoutAction           string
	UnknownExitCodePolicy   string
}

type AutomationNotificationRequest struct {
	NotificationID string
	IdempotencyKey string
	Title          string
	Message        string
	Mode           string
	Severity       string
	EventType      string
	Layout         string
	TimeoutSeconds int
	Metadata       map[string]any
}

type AutomationNotificationResponse struct {
	Accepted    bool
	Result      string
	AgentAction string
	Message     string
}

type PackageManager interface {
	Install(ctx context.Context, id string) (string, error)
	Uninstall(ctx context.Context, id string) (string, error)
	Upgrade(ctx context.Context, id string) (string, error)
	UpgradeAll(ctx context.Context) (string, error)
}

// --- Custom Fields ---

// FieldScopeType classifica o escopo de um custom field (Server, Client, Site, Agent).
type FieldScopeType int

const (
	FieldScopeServer FieldScopeType = 0
	FieldScopeClient FieldScopeType = 1
	FieldScopeSite   FieldScopeType = 2
	FieldScopeAgent  FieldScopeType = 3
)

// RuntimeCustomField representa um custom field retornado pelo endpoint de runtime.
// ValueJson é armazenado como json.RawMessage para suportar qualquer tipo JSON.
type RuntimeCustomField struct {
	DefinitionID string          `json:"definitionId"`
	Name         string          `json:"name"`
	Label        string          `json:"label"`
	ScopeType    FieldScopeType  `json:"scopeType"`
	ValueJson    json.RawMessage `json:"valueJson"`
	IsSecret     bool            `json:"isSecret"`
}

// CollectedValueRequest é o payload para POST /api/agent-auth/me/custom-fields/collected.
type CollectedValueRequest struct {
	DefinitionID *string         `json:"definitionId,omitempty"`
	Name         *string         `json:"name,omitempty"`
	Value        json.RawMessage `json:"value"`
	TaskID       *string         `json:"taskId,omitempty"`
	ScriptID     *string         `json:"scriptId,omitempty"`
}

// CollectedValueResponse é a resposta do endpoint collected.
type CollectedValueResponse struct {
	DefinitionID string          `json:"definitionId"`
	Name         string          `json:"name"`
	Label        string          `json:"label"`
	ScopeType    FieldScopeType  `json:"scopeType"`
	EntityID     string          `json:"entityId"`
	ValueJson    json.RawMessage `json:"valueJson"`
	UpdatedAt    string          `json:"updatedAt"`
	IsSecret     bool            `json:"isSecret"`
}

// CustomFieldWriteErrorCode classifica erros de negócio do endpoint collected (HTTP 400).
type CustomFieldWriteErrorCode int

const (
	WriteErrorUnknown          CustomFieldWriteErrorCode = 0
	WriteErrorNotAllowed       CustomFieldWriteErrorCode = 1 // AllowAgentWrite=false
	WriteErrorContextDenied    CustomFieldWriteErrorCode = 2 // contexto de execução não autorizado
	WriteErrorNotFound         CustomFieldWriteErrorCode = 3 // definição não encontrada
	WriteErrorInactive         CustomFieldWriteErrorCode = 4 // definição inativa
	WriteErrorScopeRestriction CustomFieldWriteErrorCode = 5 // escopo != Agent
)

// ErrCustomFieldWrite é um erro de negócio não-retentável retornado pelo servidor (HTTP 400).
type ErrCustomFieldWrite struct {
	Code    CustomFieldWriteErrorCode
	Message string
}

func (e *ErrCustomFieldWrite) Error() string { return e.Message }

// ExecutionCustomFieldCtx contém o contexto de custom fields de uma execução.
// Criado ao início de cada execução e destruído ao fim. Nunca persiste cross-run.
type ExecutionCustomFieldCtx struct {
	// Fields é o mapa name → valor decodificado, sem campos isSecret=true ou valores null.
	Fields    map[string]any
	rawByName map[string]RuntimeCustomField
	rawByID   map[string]RuntimeCustomField
}
