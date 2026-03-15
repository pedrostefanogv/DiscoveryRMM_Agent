package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"

	"winget-store/internal/database"
)

const (
	policySyncInterval   = 5 * time.Minute
	policyRetryInterval  = 30 * time.Second
	callbackPollInterval = 15 * time.Second
	callbackRetryBase    = 15 * time.Second
	callbackRetryMax     = 5 * time.Minute
	recentExecutionLimit = 15
)

type Service struct {
	mu               sync.RWMutex
	db               *database.DB
	client           *Client
	getConfig        func() RuntimeConfig
	logger           func(string)
	packageManager   PackageManager
	state            State
	currentAgent     string
	cron             *cron.Cron
	cronEntries      map[string]cron.EntryID
	activeTasks      map[string]bool
	userLoginHandled bool
}

func NewService(getConfig func() RuntimeConfig, logger func(string)) *Service {
	return &Service{
		client:      NewClient(30 * time.Second),
		getConfig:   getConfig,
		logger:      logger,
		state:       State{},
		cronEntries: make(map[string]cron.EntryID),
		activeTasks: make(map[string]bool),
	}
}

func (s *Service) SetDB(db *database.DB) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.db = db
}

func (s *Service) SetPackageManager(manager PackageManager) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.packageManager = manager
}

func (s *Service) Run(ctx context.Context, onBeat func()) {
	s.startCron()
	defer s.stopCron()

	go s.runCallbackLoop(ctx, onBeat)

	s.loadPersistedForCurrentAgent()
	state, _ := s.refreshPolicy(ctx, false)
	timer := time.NewTimer(nextRunInterval(state))
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if onBeat != nil {
				onBeat()
			}
			state, _ = s.refreshPolicy(ctx, false)
			timer.Reset(nextRunInterval(state))
		}
	}
}

func (s *Service) RefreshPolicy(ctx context.Context, includeScriptContent bool) (State, error) {
	return s.refreshPolicy(ctx, includeScriptContent)
}

func (s *Service) GetState() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneState(s.state)
}

func (s *Service) refreshPolicy(ctx context.Context, includeScriptContent bool) (State, error) {
	cfg := s.getConfig()
	agentID := strings.TrimSpace(cfg.AgentID)
	available := strings.TrimSpace(cfg.BaseURL) != "" && strings.TrimSpace(cfg.Token) != "" && agentID != ""
	if !available {
		s.mu.Lock()
		s.state.Available = false
		s.state.Connected = false
		s.state.IncludeScriptContent = includeScriptContent
		s.mu.Unlock()
		return s.GetState(), nil
	}

	s.loadPersistedForAgent(agentID)

	now := time.Now().UTC().Format(time.RFC3339)
	correlationID := uuid.NewString()
	current := s.GetState()
	knownFingerprint := strings.TrimSpace(current.PolicyFingerprint)
	req := PolicySyncRequest{}
	if knownFingerprint != "" {
		req.KnownPolicyFingerprint = &knownFingerprint
	}
	req.IncludeScriptContent = &includeScriptContent

	s.mu.Lock()
	s.state.Available = true
	s.state.LastAttemptAt = now
	s.state.CorrelationID = correlationID
	s.state.IncludeScriptContent = includeScriptContent
	s.mu.Unlock()

	resp, err := s.client.SyncPolicy(ctx, cfg, req, correlationID)
	if err != nil {
		s.mu.Lock()
		s.state.Available = true
		s.state.Connected = false
		s.state.LastError = err.Error()
		s.state.LastAttemptAt = now
		s.state.CorrelationID = correlationID
		failed := cloneState(s.state)
		s.mu.Unlock()
		return failed, err
	}

	effectiveTasks := cloneTasks(resp.Tasks)
	if resp.UpToDate && len(effectiveTasks) == 0 {
		effectiveTasks = cloneTasks(current.Tasks)
	}

	next := current
	next.Available = true
	next.Connected = true
	next.LoadedFromCache = false
	next.UpToDate = resp.UpToDate
	next.IncludeScriptContent = includeScriptContent
	next.LastError = ""
	next.LastAttemptAt = now
	next.LastSyncAt = now
	next.CorrelationID = correlationID
	if fp := strings.TrimSpace(resp.PolicyFingerprint); fp != "" {
		next.PolicyFingerprint = fp
	}
	if generatedAt := strings.TrimSpace(resp.GeneratedAt); generatedAt != "" {
		next.GeneratedAt = generatedAt
	}
	next.Tasks = effectiveTasks
	next.TaskCount = len(effectiveTasks)

	persisted := PersistedPolicy{
		Policy: PolicySyncResponse{
			UpToDate:          false,
			PolicyFingerprint: next.PolicyFingerprint,
			GeneratedAt:       next.GeneratedAt,
			TaskCount:         len(effectiveTasks),
			Tasks:             effectiveTasks,
		},
		SavedAt:              now,
		IncludeScriptContent: includeScriptContent,
	}
	if s.db != nil {
		payload, marshalErr := json.Marshal(persisted)
		if marshalErr == nil {
			if saveErr := s.db.SaveAutomationPolicy(agentID, next.PolicyFingerprint, payload); saveErr != nil {
				s.logf("automacao: falha ao persistir policy local: %v", saveErr)
			}
		}
	}

	s.populateStateFromDB(agentID, &next)

	s.mu.Lock()
	s.state = next
	s.currentAgent = agentID
	s.mu.Unlock()

	if err := s.reconcilePolicy(ctx, current, next, agentID); err != nil {
		s.logf("automacao: falha ao reconciliar policy: %v", err)
	}

	s.refreshDerivedState(agentID)
	s.logf("automacao: policy sync concluido (tasks=%d upToDate=%t)", len(effectiveTasks), resp.UpToDate)
	return s.GetState(), nil
}

func (s *Service) reconcilePolicy(ctx context.Context, previous State, current State, agentID string) error {
	s.rebuildRecurringSchedules(ctx, current.Tasks)

	for _, task := range current.Tasks {
		task := task
		if task.TriggerImmediate {
			s.triggerImmediate(ctx, agentID, current.PolicyFingerprint, task)
		}
		if task.TriggerOnAgentCheckIn {
			s.executeTaskAsync(ctx, agentID, task, sourceForTrigger(TriggerTypeAgentCheckIn), TriggerTypeAgentCheckIn)
		}
	}

	if !s.userLoginHandled {
		triggered := false
		for _, task := range current.Tasks {
			if task.TriggerOnUserLogin {
				triggered = true
				s.executeTaskAsync(ctx, agentID, task, sourceForTrigger(TriggerTypeUserLogin), TriggerTypeUserLogin)
			}
		}
		if triggered {
			s.userLoginHandled = true
		}
	}

	_ = previous
	return nil
}

func (s *Service) triggerImmediate(ctx context.Context, agentID, fingerprint string, task AutomationTask) {
	if s.db == nil || agentID == "" || fingerprint == "" {
		s.executeTaskAsync(ctx, agentID, task, sourceForTrigger(TriggerTypeImmediate), TriggerTypeImmediate)
		return
	}
	markerKey := "immediate:" + fingerprint + ":" + strings.TrimSpace(task.TaskID)
	if _, found, err := s.db.GetAutomationMarker(agentID, markerKey); err == nil && found {
		return
	}
	_ = s.db.SetAutomationMarker(agentID, markerKey, time.Now().UTC().Format(time.RFC3339))
	s.executeTaskAsync(ctx, agentID, task, sourceForTrigger(TriggerTypeImmediate), TriggerTypeImmediate)
}

func (s *Service) executeTaskAsync(ctx context.Context, agentID string, task AutomationTask, sourceType AutomationExecutionSourceType, triggerType TriggerType) {
	if agentID == "" {
		return
	}
	activeKey := strings.TrimSpace(task.TaskID) + "|" + string(triggerType)

	s.mu.Lock()
	if s.activeTasks[activeKey] {
		s.mu.Unlock()
		return
	}
	s.activeTasks[activeKey] = true
	packages := s.packageManager
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.activeTasks, activeKey)
			s.mu.Unlock()
		}()

		startedAt := time.Now().UTC()
		executionID := uuid.NewString()
		correlationID := uuid.NewString()
		entry := database.AutomationExecutionEntry{
			ExecutionID:      executionID,
			AgentID:          agentID,
			CommandID:        strings.TrimSpace(task.CommandID),
			TaskID:           strings.TrimSpace(task.TaskID),
			TaskName:         strings.TrimSpace(task.Name),
			ActionType:       string(task.ActionType),
			InstallationType: string(task.InstallationType),
			SourceType:       string(sourceType),
			TriggerType:      string(triggerType),
			Status:           string(ExecutionStatusDispatched),
			CorrelationID:    correlationID,
			StartedAt:        startedAt,
			PackageID:        strings.TrimSpace(task.PackageID),
			ScriptID:         strings.TrimSpace(task.ScriptID),
			MetadataJSON:     buildExecutionMetadata(task, triggerType, "start", nil),
		}
		if s.db != nil {
			_ = s.db.UpsertAutomationExecution(entry)
		}
		s.refreshDerivedState(agentID)

		if entry.CommandID != "" {
			ack := AckRequest{
				TaskID:       entry.TaskID,
				ScriptID:     entry.ScriptID,
				SourceType:   sourceType,
				MetadataJSON: buildExecutionMetadata(task, triggerType, "ack", nil),
			}
			if err := s.sendOrQueueCallback(ctx, agentID, executionID, entry.CommandID, CallbackTypeAck, ack, correlationID); err == nil {
				entry.Status = string(ExecutionStatusAcknowledged)
				if s.db != nil {
					_ = s.db.UpsertAutomationExecution(entry)
				}
			}
		}

		result := executeTask(ctx, packages, task)
		entry.FinishedAt = time.Now().UTC()
		entry.Success = result.Success
		entry.SuccessSet = true
		entry.ExitCode = result.ExitCode
		entry.ExitCodeSet = result.ExitCodeSet
		entry.Output = result.Output
		entry.ErrorMessage = result.ErrorMessage
		entry.MetadataJSON = buildExecutionMetadata(task, triggerType, "result", &result)
		if result.Success {
			entry.Status = string(ExecutionStatusCompleted)
		} else {
			entry.Status = string(ExecutionStatusFailed)
		}
		if s.db != nil {
			_ = s.db.UpsertAutomationExecution(entry)
		}

		if entry.CommandID != "" {
			payload := ResultRequest{
				TaskID:       entry.TaskID,
				ScriptID:     entry.ScriptID,
				SourceType:   sourceType,
				Success:      result.Success,
				ErrorMessage: result.ErrorMessage,
				MetadataJSON: entry.MetadataJSON,
			}
			if result.ExitCodeSet {
				exitCode := result.ExitCode
				payload.ExitCode = &exitCode
			}
			_ = s.sendOrQueueCallback(ctx, agentID, executionID, entry.CommandID, CallbackTypeResult, payload, correlationID)
		}

		s.refreshDerivedState(agentID)
	}()
}

func (s *Service) sendOrQueueCallback(ctx context.Context, agentID, executionID, commandID string, callbackType CallbackType, payload any, correlationID string) error {
	if strings.TrimSpace(commandID) == "" {
		return nil
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	cfg := s.getConfig()

	var callbackErr error
	switch callbackType {
	case CallbackTypeAck:
		var req AckRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return err
		}
		callbackErr = s.client.AckExecution(ctx, cfg, commandID, req, correlationID)
	case CallbackTypeResult:
		var req ResultRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return err
		}
		callbackErr = s.client.ReportExecutionResult(ctx, cfg, commandID, req, correlationID)
	default:
		callbackErr = fmt.Errorf("callbackType invalido")
	}
	if callbackErr == nil {
		return nil
	}
	if s.db == nil {
		return callbackErr
	}
	queueEntry := database.AutomationCallbackEntry{
		AgentID:       agentID,
		ExecutionID:   executionID,
		CommandID:     commandID,
		CallbackType:  string(callbackType),
		CorrelationID: correlationID,
		PayloadJSON:   string(body),
		Attempts:      1,
		NextAttemptAt: time.Now().Add(callbackBackoff(1)),
		LastError:     callbackErr.Error(),
	}
	if err := s.db.EnqueueAutomationCallback(queueEntry); err != nil {
		return err
	}
	return callbackErr
}

func (s *Service) rebuildRecurringSchedules(ctx context.Context, tasks []AutomationTask) {
	s.startCron()

	s.mu.Lock()
	for taskID, entryID := range s.cronEntries {
		s.cron.Remove(entryID)
		delete(s.cronEntries, taskID)
	}
	cronInstance := s.cron
	s.mu.Unlock()

	for _, task := range tasks {
		if !task.TriggerRecurring || strings.TrimSpace(task.ScheduleCron) == "" {
			continue
		}
		if task.RequiresApproval {
			s.logf("automacao: tarefa %s requer aprovacao e nao sera agendada no cron", task.TaskID)
			continue
		}
		taskCopy := task
		entryID, err := cronInstance.AddFunc(strings.TrimSpace(task.ScheduleCron), func() {
			s.executeTaskAsync(ctx, strings.TrimSpace(s.getConfig().AgentID), taskCopy, sourceForTrigger(TriggerTypeRecurring), TriggerTypeRecurring)
		})
		if err != nil {
			s.logf("automacao: cron invalido para tarefa %s: %v", task.TaskID, err)
			continue
		}
		s.mu.Lock()
		s.cronEntries[task.TaskID] = entryID
		s.mu.Unlock()
	}
}

func (s *Service) runCallbackLoop(ctx context.Context, onBeat func()) {
	ticker := time.NewTicker(callbackPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if onBeat != nil {
				onBeat()
			}
			s.processCallbackQueue(ctx)
		}
	}
}

func (s *Service) processCallbackQueue(ctx context.Context) {
	cfg := s.getConfig()
	agentID := strings.TrimSpace(cfg.AgentID)
	if s.db == nil || agentID == "" || strings.TrimSpace(cfg.BaseURL) == "" || strings.TrimSpace(cfg.Token) == "" {
		return
	}

	entries, err := s.db.ListDueAutomationCallbacks(agentID, time.Now(), 20)
	if err != nil {
		s.logf("automacao: falha ao listar callbacks pendentes: %v", err)
		return
	}

	for _, entry := range entries {
		var callbackErr error
		switch CallbackType(entry.CallbackType) {
		case CallbackTypeAck:
			var req AckRequest
			if err := json.Unmarshal([]byte(entry.PayloadJSON), &req); err != nil {
				callbackErr = err
			} else {
				callbackErr = s.client.AckExecution(ctx, cfg, entry.CommandID, req, entry.CorrelationID)
			}
		case CallbackTypeResult:
			var req ResultRequest
			if err := json.Unmarshal([]byte(entry.PayloadJSON), &req); err != nil {
				callbackErr = err
			} else {
				callbackErr = s.client.ReportExecutionResult(ctx, cfg, entry.CommandID, req, entry.CorrelationID)
			}
		default:
			callbackErr = fmt.Errorf("callbackType desconhecido")
		}

		if callbackErr == nil {
			_ = s.db.DeleteAutomationCallback(entry.ID)
			continue
		}

		attempts := entry.Attempts + 1
		_ = s.db.RescheduleAutomationCallback(entry.ID, attempts, time.Now().Add(callbackBackoff(attempts)), callbackErr.Error())
		if strings.Contains(strings.ToLower(callbackErr.Error()), "404") {
			s.logf("automacao: callback %s rejeitado por 404 para commandId=%s", entry.CallbackType, entry.CommandID)
		}
	}

	s.refreshDerivedState(agentID)
}

func (s *Service) loadPersistedForCurrentAgent() {
	if s.getConfig == nil {
		return
	}
	cfg := s.getConfig()
	s.loadPersistedForAgent(strings.TrimSpace(cfg.AgentID))
}

func (s *Service) loadPersistedForAgent(agentID string) {
	if strings.TrimSpace(agentID) == "" {
		return
	}

	s.mu.RLock()
	if s.currentAgent == agentID && (s.state.PolicyFingerprint != "" || len(s.state.Tasks) > 0 || s.state.LoadedFromCache) {
		s.mu.RUnlock()
		return
	}
	db := s.db
	s.mu.RUnlock()
	if db == nil {
		return
	}

	payload, _, _, found, err := db.GetAutomationPolicy(agentID)
	if err != nil || !found || len(payload) == 0 {
		return
	}

	var persisted PersistedPolicy
	if err := json.Unmarshal(payload, &persisted); err != nil {
		s.logf("automacao: falha ao ler policy persistida: %v", err)
		return
	}

	loaded := State{
		Available:            true,
		Connected:            false,
		LoadedFromCache:      true,
		UpToDate:             false,
		IncludeScriptContent: persisted.IncludeScriptContent,
		PolicyFingerprint:    strings.TrimSpace(persisted.Policy.PolicyFingerprint),
		GeneratedAt:          strings.TrimSpace(persisted.Policy.GeneratedAt),
		TaskCount:            len(persisted.Policy.Tasks),
		Tasks:                cloneTasks(persisted.Policy.Tasks),
	}
	s.populateStateFromDB(agentID, &loaded)

	s.mu.Lock()
	if s.currentAgent != agentID || (s.state.PolicyFingerprint == "" && len(s.state.Tasks) == 0) {
		s.state = loaded
		s.currentAgent = agentID
	}
	s.mu.Unlock()
}

func (s *Service) populateStateFromDB(agentID string, state *State) {
	if s.db == nil || state == nil || strings.TrimSpace(agentID) == "" {
		return
	}
	recent, err := s.db.ListRecentAutomationExecutions(agentID, recentExecutionLimit)
	if err == nil {
		state.RecentExecutions = mapExecutionEntries(recent)
	}
	count, err := s.db.CountPendingAutomationCallbacks(agentID)
	if err == nil {
		state.PendingCallbacks = count
	}
}

func (s *Service) refreshDerivedState(agentID string) {
	if s.db == nil || strings.TrimSpace(agentID) == "" {
		return
	}
	recent, err := s.db.ListRecentAutomationExecutions(agentID, recentExecutionLimit)
	if err != nil {
		return
	}
	count, err := s.db.CountPendingAutomationCallbacks(agentID)
	if err != nil {
		return
	}

	s.mu.Lock()
	if s.currentAgent == agentID {
		s.state.RecentExecutions = mapExecutionEntries(recent)
		s.state.PendingCallbacks = count
	}
	s.mu.Unlock()
}

func (s *Service) startCron() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron != nil {
		return
	}
	s.cron = cron.New()
	s.cron.Start()
}

func (s *Service) stopCron() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron == nil {
		return
	}
	stopCtx := s.cron.Stop()
	<-stopCtx.Done()
	s.cron = nil
	s.cronEntries = make(map[string]cron.EntryID)
}

func cloneState(in State) State {
	out := in
	out.Tasks = cloneTasks(in.Tasks)
	out.RecentExecutions = append([]ExecutionRecord(nil), in.RecentExecutions...)
	return out
}

func cloneTasks(in []AutomationTask) []AutomationTask {
	if len(in) == 0 {
		return nil
	}
	out := make([]AutomationTask, len(in))
	copy(out, in)
	for i := range out {
		out[i].IncludeTags = append([]string(nil), in[i].IncludeTags...)
		out[i].ExcludeTags = append([]string(nil), in[i].ExcludeTags...)
		if in[i].Script != nil {
			scriptCopy := *in[i].Script
			out[i].Script = &scriptCopy
		}
	}
	return out
}

func mapExecutionEntries(entries []database.AutomationExecutionEntry) []ExecutionRecord {
	out := make([]ExecutionRecord, 0, len(entries))
	for _, entry := range entries {
		out = append(out, ExecutionRecord{
			ExecutionID:      entry.ExecutionID,
			CommandID:        entry.CommandID,
			TaskID:           entry.TaskID,
			TaskName:         entry.TaskName,
			ActionType:       AutomationTaskActionType(entry.ActionType),
			InstallationType: AppInstallationType(entry.InstallationType),
			SourceType:       AutomationExecutionSourceType(entry.SourceType),
			TriggerType:      TriggerType(entry.TriggerType),
			Status:           AutomationExecutionStatus(entry.Status),
			Success:          entry.Success,
			ExitCode:         entry.ExitCode,
			ExitCodeSet:      entry.ExitCodeSet,
			ErrorMessage:     entry.ErrorMessage,
			Output:           entry.Output,
			PackageID:        entry.PackageID,
			ScriptID:         entry.ScriptID,
			CorrelationID:    entry.CorrelationID,
			StartedAt:        formatTime(entry.StartedAt),
			FinishedAt:       formatTime(entry.FinishedAt),
			MetadataJSON:     entry.MetadataJSON,
		})
	}
	return out
}

func sourceForTrigger(triggerType TriggerType) AutomationExecutionSourceType {
	switch triggerType {
	case TriggerTypeImmediate, TriggerTypeAgentCheckIn:
		return ExecutionSourceForceSync
	case TriggerTypeRecurring, TriggerTypeUserLogin:
		return ExecutionSourceScheduled
	case TriggerTypeManual:
		return ExecutionSourceAgentManual
	default:
		return ExecutionSourceRunNow
	}
}

func buildExecutionMetadata(task AutomationTask, triggerType TriggerType, stage string, result *ExecutionResult) string {
	payload := map[string]any{
		"stage":            stage,
		"triggerType":      string(triggerType),
		"taskName":         task.Name,
		"actionType":       string(task.ActionType),
		"packageId":        task.PackageID,
		"scriptId":         task.ScriptID,
		"requiresApproval": task.RequiresApproval,
	}
	if result != nil {
		payload["success"] = result.Success
		if result.ExitCodeSet {
			payload["exitCode"] = result.ExitCode
		}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(data)
}

func callbackBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		attempt = 1
	}
	backoff := callbackRetryBase
	for i := 1; i < attempt; i++ {
		backoff *= 2
		if backoff >= callbackRetryMax {
			return callbackRetryMax
		}
	}
	if backoff > callbackRetryMax {
		return callbackRetryMax
	}
	return backoff
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func (s *Service) logf(format string, args ...any) {
	if s.logger != nil {
		s.logger(formatMessage(format, args...))
	}
}

func formatMessage(format string, args ...any) string {
	if len(args) == 0 {
		return format
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(fmt.Sprintf(format, args...)), "\n"))
}

func nextRunInterval(state State) time.Duration {
	if !state.Available || !state.Connected {
		return policyRetryInterval
	}
	return policySyncInterval
}
