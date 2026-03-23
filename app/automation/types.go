package automation

// TaskView is the frontend representation of a resolved automation task.
type TaskView struct {
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

type ExecutionView struct {
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

// StateView represents the current automation policy state in the UI.
type StateView struct {
	Available            bool            `json:"available"`
	Connected            bool            `json:"connected"`
	LoadedFromCache      bool            `json:"loadedFromCache"`
	UpToDate             bool            `json:"upToDate"`
	IncludeScriptContent bool            `json:"includeScriptContent"`
	PolicyFingerprint    string          `json:"policyFingerprint,omitempty"`
	GeneratedAt          string          `json:"generatedAt,omitempty"`
	LastSyncAt           string          `json:"lastSyncAt,omitempty"`
	LastAttemptAt        string          `json:"lastAttemptAt,omitempty"`
	LastError            string          `json:"lastError,omitempty"`
	CorrelationID        string          `json:"correlationId,omitempty"`
	TaskCount            int             `json:"taskCount"`
	PendingCallbacks     int             `json:"pendingCallbacks"`
	Tasks                []TaskView      `json:"tasks,omitempty"`
	RecentExecutions     []ExecutionView `json:"recentExecutions,omitempty"`
}
