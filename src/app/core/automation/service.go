package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"

	"discovery/app/core/database"
	"discovery/app/core/errutil"
)

const (
	policySyncInterval   = 5 * time.Minute
	policyRetryInterval  = 30 * time.Second
	callbackPollInterval = 15 * time.Second
	callbackRetryBase    = 15 * time.Second
	callbackRetryMax     = 5 * time.Minute
	recentExecutionLimit = 15
	defaultDeferTimes    = 3
	defaultDeferInterval = 30 * time.Minute
)

type deferState struct {
	ExecutionID  string
	Count        int
	FirstDeferAt time.Time
	LastDeferAt  time.Time
	NextAttempt  time.Time
	DeadlineAt   time.Time
	Exhausted    bool
}

type psadtWelcomeOptions struct {
	AllowDefer                     bool
	AllowDeferCloseProcesses       bool
	DeferTimes                     int
	DeferDays                      float64
	DeferDeadline                  time.Time
	DeferRunInterval               time.Duration
	ForceCountdownSeconds          int
	CloseProcessesCountdownSeconds int
	ForceCloseProcessesCountdown   int
	CloseProcesses                 []string
	BlockExecution                 bool
	CheckDiskSpace                 bool
	RequiredDiskSpaceMB            int
}

type Service struct {
	mu               sync.RWMutex
	db               *database.DB
	client           *Client
	getConfig        func() RuntimeConfig
	logger           func(string)
	packageManager   PackageManager
	packageAuthorize PackageAuthorizationFunc
	psadtResolver    func() PSADTPolicy
	notifyDispatcher func(AutomationNotificationRequest) AutomationNotificationResponse
	deferByTask      map[string]deferState
	state            State
	currentAgent     string
	cron             *cron.Cron
	cronEntries      map[string]cron.EntryID
	activeTasks      map[string]bool
	userLoginHandled bool
	// processStartAt identifica a sessão atual do processo. Usado como parte da chave
	// do marcador userLogin para que TriggerOnUserLogin dispare uma vez por sessão
	// (não uma vez para sempre). Sobrevive a crashes curtos, mas expira em reboot
	// do processo do agente.
	processStartAt time.Time
	cfMu           sync.RWMutex
	cfCache        map[string]*ExecutionCustomFieldCtx // key = executionID
}

func NewService(getConfig func() RuntimeConfig, logger func(string)) *Service {
	return &Service{
		client:         NewClient(30 * time.Second),
		getConfig:      getConfig,
		logger:         logger,
		state:          State{},
		cronEntries:    make(map[string]cron.EntryID),
		activeTasks:    make(map[string]bool),
		deferByTask:    make(map[string]deferState),
		processStartAt: time.Now().UTC(),
		cfCache:        make(map[string]*ExecutionCustomFieldCtx),
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

func (s *Service) SetPackageAuthorization(authorize PackageAuthorizationFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.packageAuthorize = authorize
}

func (s *Service) SetPSADTPolicyResolver(resolver func() PSADTPolicy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.psadtResolver = resolver
}

func (s *Service) SetNotificationDispatcher(dispatcher func(AutomationNotificationRequest) AutomationNotificationResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notifyDispatcher = dispatcher
}

func (s *Service) Run(ctx context.Context, onBeat func()) {
	// Watcher de logon real (Windows/WTS). Se indisponivel, mantem fallback
	// 'uma vez por processo' no reconcilePolicy.
	loginEvents, stopLoginWatcher := startUserLoginWatcher(ctx)
	defer stopLoginWatcher()
	go s.consumeUserLoginEvents(ctx, loginEvents)

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

// consumeUserLoginEvents consome eventos de logon do watcher WTS (Windows) e dispara
// tasks TriggerOnUserLogin por evento real, com dedup por sessão+janela de 60s.
// Canal nil (não-Windows ou falha no registro) → fallback "uma vez por processo" no reconcile.
func (s *Service) consumeUserLoginEvents(ctx context.Context, events <-chan userLoginEvent) {
	if events == nil {
		return
	}
	lastBySession := make(map[uint32]time.Time)
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-events:
			if last, ok := lastBySession[ev.SessionID]; ok && time.Since(last) < 60*time.Second {
				continue
			}
			lastBySession[ev.SessionID] = time.Now()
			s.logf("automacao: logon detectado (sessionId=%d) - disparando tasks TriggerOnUserLogin", ev.SessionID)
			s.triggerUserLoginTasks(ctx, ev.SessionID)
		}
	}
}

// triggerUserLoginTasks executa todas as tasks TriggerOnUserLogin ativas para um evento de logon.
func (s *Service) triggerUserLoginTasks(ctx context.Context, sessionID uint32) {
	cfg := s.getConfig()
	agentID := strings.TrimSpace(cfg.AgentID)
	if agentID == "" {
		return
	}
	s.mu.RLock()
	state := cloneState(s.state)
	s.mu.RUnlock()
	for _, task := range state.Tasks {
		if !task.TriggerOnUserLogin {
			continue
		}
		if task.RequiresApproval {
			s.logf("automacao: tarefa %s requer aprovacao - trigger userlogin ignorado", strings.TrimSpace(task.TaskID))
			continue
		}
		s.executeTaskAsync(ctx, agentID, task, sourceForTrigger(TriggerTypeUserLogin), TriggerTypeUserLogin, nil)
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
	s.loadDeferStateForAgent(agentID)

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
	s.rebuildRecurringSchedules(ctx, previous.Tasks, current.Tasks)

	for _, task := range current.Tasks {
		task := task
		if task.TriggerImmediate {
			s.triggerImmediate(ctx, agentID, current.PolicyFingerprint, task)
		}
		if task.TriggerOnAgentCheckIn {
			s.triggerOnAgentCheckIn(ctx, agentID, current.PolicyFingerprint, task)
		}
	}

	// Persiste userLoginHandled no SQLite para sobreviver a crashes curtos do agente.
	// A chave inclui o timestamp de início do processo, então expira a cada restart
	// do processo (não a cada reboot do Windows). Isso preserva a semântica original
	// de "uma vez por ciclo de vida do processo" e evita que TriggerOnUserLogin
	// nunca mais dispare após o primeiro login.
	userLoginMarkerKey := "_sys:userlogin:handled:" + s.processStartAt.Format(time.RFC3339)
	if !s.userLoginHandled {
		if s.db != nil && agentID != "" {
			// Se o marcador existe no banco, user login já foi tratado nesta sessão.
			if _, found, _ := s.db.GetAutomationMarker(agentID, userLoginMarkerKey); found {
				s.userLoginHandled = true
			}
		}
	}

	if !s.userLoginHandled {
		triggered := false
		for _, task := range current.Tasks {
			if task.TriggerOnUserLogin {
				if task.RequiresApproval {
					s.logf("automacao: tarefa %s requer aprovacao - trigger userlogin ignorado", strings.TrimSpace(task.TaskID))
					continue
				}
				triggered = true
				s.executeTaskAsync(ctx, agentID, task, sourceForTrigger(TriggerTypeUserLogin), TriggerTypeUserLogin, nil)
			}
		}
		if triggered {
			s.userLoginHandled = true
			if s.db != nil && agentID != "" {
				errutil.LogIfErr(s.db.SetAutomationMarker(agentID, userLoginMarkerKey, time.Now().UTC().Format(time.RFC3339)), "automation: persistir marcador userLogin")
			}
		}
	}

	_ = previous

	// Remove marcadores órfãos do SQLite (immediate/checkin de fingerprints antigos,
	// recurring:last de tasks deletadas, userlogin de sessões passadas).
	s.cleanupOrphanedMarkers(agentID, current)

	return nil
}

func (s *Service) triggerImmediate(ctx context.Context, agentID, fingerprint string, task AutomationTask) {
	if s.db == nil || agentID == "" || fingerprint == "" {
		s.executeTaskAsync(ctx, agentID, task, sourceForTrigger(TriggerTypeImmediate), TriggerTypeImmediate, nil)
		return
	}
	if task.RequiresApproval {
		s.logf("automacao: tarefa %s requer aprovacao - trigger immediate ignorado", strings.TrimSpace(task.TaskID))
		return
	}
	// C2: a chave inclui LastUpdatedAt — se a task for atualizada no servidor,
	// o marcador antigo (com updatedAt anterior) não existe e o trigger dispara
	// novamente. Antes a chave era só immediate:<fp>:<taskId> e persistia para
	// sempre, bloqueando silenciosamente tasks recriadas/enviadas offline.
	markerKey := "immediate:" + fingerprint + ":" + strings.TrimSpace(task.TaskID) + ":" + task.LastUpdatedAt
	if _, found, err := s.db.GetAutomationMarker(agentID, markerKey); err == nil && found {
		return
	}
	errutil.LogIfErr(s.db.SetAutomationMarker(agentID, markerKey, time.Now().UTC().Format(time.RFC3339)), "automation: definir marker imediato")
	s.executeTaskAsync(ctx, agentID, task, sourceForTrigger(TriggerTypeImmediate), TriggerTypeImmediate, nil)
}

// triggerOnAgentCheckIn dispara tarefas TriggerOnAgentCheckIn com deduplicação por marcador.
// O marcador é atrelado ao fingerprint da política, assim como no triggerImmediate.
// Diferente do immediate, o marcador é resetado quando a política muda (novo fingerprint),
// permitindo que a tarefa seja reexecutada após alteração no servidor.
// TriggerAgentCheckInTasks dispara tasks TriggerOnAgentCheckIn a cada ciclo de inventário
// completo do agent (periodicInventorySync, ~6h). Dedup por janela mínima de 1h via
// marcador SQLite — protege contra chamadas repetidas em pouco tempo (ex.: restarts).
// Chamado pelo App quando o inventário completo é coletado e sincronizado.
func (s *Service) TriggerAgentCheckInTasks(ctx context.Context) {
	cfg := s.getConfig()
	agentID := strings.TrimSpace(cfg.AgentID)
	if agentID == "" {
		return
	}
	s.mu.RLock()
	state := cloneState(s.state)
	s.mu.RUnlock()
	if len(state.Tasks) == 0 {
		return
	}

	const checkInWindow = time.Hour
	now := time.Now().UTC()
	for _, task := range state.Tasks {
		if !task.TriggerOnAgentCheckIn {
			continue
		}
		if task.RequiresApproval {
			s.logf("automacao: tarefa %s requer aprovacao - trigger checkin ignorado", strings.TrimSpace(task.TaskID))
			continue
		}
		taskID := strings.TrimSpace(task.TaskID)
		if s.db != nil {
			markerKey := "checkin:cycle:" + taskID
			if raw, found, err := s.db.GetAutomationMarker(agentID, markerKey); err == nil && found {
				if last, perr := time.Parse(time.RFC3339, raw); perr == nil && now.Sub(last) < checkInWindow {
					continue
				}
			}
			errutil.LogIfErr(s.db.SetAutomationMarker(agentID, markerKey, now.Format(time.RFC3339)), "automation: definir marker checkin-cycle")
		}
		s.logf("automacao: check-in cycle — disparando tarefa %s", taskID)
		s.executeTaskAsync(ctx, agentID, task, sourceForTrigger(TriggerTypeAgentCheckIn), TriggerTypeAgentCheckIn, nil)
	}
}
func (s *Service) triggerOnAgentCheckIn(ctx context.Context, agentID, fingerprint string, task AutomationTask) {
	if s.db == nil || agentID == "" || fingerprint == "" {
		s.executeTaskAsync(ctx, agentID, task, sourceForTrigger(TriggerTypeAgentCheckIn), TriggerTypeAgentCheckIn, nil)
		return
	}
	if task.RequiresApproval {
		s.logf("automacao: tarefa %s requer aprovacao - trigger checkin ignorado", strings.TrimSpace(task.TaskID))
		return
	}
	// C2: mesma correção do triggerImmediate — LastUpdatedAt na chave.
	markerKey := "checkin:" + fingerprint + ":" + strings.TrimSpace(task.TaskID) + ":" + task.LastUpdatedAt
	if _, found, err := s.db.GetAutomationMarker(agentID, markerKey); err == nil && found {
		return
	}
	errutil.LogIfErr(s.db.SetAutomationMarker(agentID, markerKey, time.Now().UTC().Format(time.RFC3339)), "automation: definir marker checkin")
	s.executeTaskAsync(ctx, agentID, task, sourceForTrigger(TriggerTypeAgentCheckIn), TriggerTypeAgentCheckIn, nil)
}

func (s *Service) executeTaskAsync(ctx context.Context, agentID string, task AutomationTask, sourceType AutomationExecutionSourceType, triggerType TriggerType, onComplete func(success bool)) {
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
	authorize := s.packageAuthorize
	psadtPolicy := s.resolvePSADTPolicyLocked()
	notifyDispatcher := s.notifyDispatcher
	deferStateSnapshot := s.deferByTask[strings.TrimSpace(task.TaskID)]
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.activeTasks, activeKey)
			s.mu.Unlock()
		}()

		welcome := resolvePSADTWelcomeOptions(task)
		nowUTC := time.Now().UTC()
		if !deferStateSnapshot.NextAttempt.IsZero() && nowUTC.Before(deferStateSnapshot.NextAttempt) {
			s.logf("automacao: task=%s aguardando proxima tentativa de deferimento em %s", strings.TrimSpace(task.TaskID), deferStateSnapshot.NextAttempt.UTC().Format(time.RFC3339))
			return
		}

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
			MetadataJSON:     buildExecutionMetadata(task, triggerType, "start", nil, &psadtPolicy),
		}
		if s.db != nil {
			errutil.LogIfErr(s.db.UpsertAutomationExecution(entry), "automation: persistir execucao iniciada")
		}
		s.refreshDerivedState(agentID)

		startResp := s.dispatchExecutionNotification(notifyDispatcher, task, entry, nil, deferStateSnapshot, welcome)
		if s.shouldDeferExecution(task, startResp) {
			next := s.recordAndGetNextDefer(agentID, executionID, task, deferStateSnapshot, welcome)
			if next.IsZero() {
				s.logf("automacao: defer ignorado para task=%s por limite esgotado", strings.TrimSpace(task.TaskID))
			} else {
				s.logf("automacao: task=%s adiada; proxima tentativa em %s", strings.TrimSpace(task.TaskID), next.UTC().Format(time.RFC3339))
				taskCopy := task
				delay := time.Until(next)
				if delay < 0 {
					delay = 0
				}
				time.AfterFunc(delay, func() {
					s.executeTaskAsync(context.Background(), agentID, taskCopy, sourceType, triggerType, nil)
				})
				return
			}
		}

		if entry.CommandID != "" {
			ack := AckRequest{
				TaskID:       entry.TaskID,
				ScriptID:     entry.ScriptID,
				SourceType:   sourceType,
				MetadataJSON: buildExecutionMetadata(task, triggerType, "ack", nil, &psadtPolicy),
			}
			if err := s.sendOrQueueCallback(ctx, agentID, executionID, entry.CommandID, CallbackTypeAck, ack, correlationID); err == nil {
				entry.Status = string(ExecutionStatusAcknowledged)
				if s.db != nil {
					errutil.LogIfErr(s.db.UpsertAutomationExecution(entry), "automation: persistir ack")
				}
			}
		}

		// Carrega custom fields para esta execução (falha silenciosa - não bloqueia a execução).
		cfg := s.getConfig()
		cfCtx := s.loadCustomFieldsForExecution(ctx, cfg, executionID, entry.TaskID, entry.ScriptID, correlationID)
		defer s.clearCustomFieldCtx(executionID)

		result := executeTask(ctx, packages, authorize, task, psadtPolicy, cfCtx.Fields)

		// Parseia valores coletados pelo script via protocolo MDZ_COLLECT.
		collectedItems, cleanedOutput := parseCollectedValues(result.Output)
		result.Output = cleanedOutput
		s.postCollectedValues(ctx, cfg, executionID, task, collectedItems, cfCtx, correlationID)

		entry.FinishedAt = time.Now().UTC()
		entry.Success = result.Success
		entry.SuccessSet = true
		entry.ExitCode = result.ExitCode
		entry.ExitCodeSet = result.ExitCodeSet
		entry.Output = result.Output
		entry.ErrorMessage = result.ErrorMessage
		entry.MetadataJSON = buildExecutionMetadata(task, triggerType, "result", &result, &psadtPolicy)
		if result.Success {
			entry.Status = string(ExecutionStatusCompleted)
		} else {
			entry.Status = string(ExecutionStatusFailed)
		}
		if s.db != nil {
			errutil.LogIfErr(s.db.UpsertAutomationExecution(entry), "automation: persistir resultado da execucao")
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
		// Notifica o chamador do resultado (ex: marcador recurring:last).
		if onComplete != nil {
			onComplete(result.Success)
		}

		// Anti-loop (Fase 1): atualiza backoff de skips e circuit breaker
		// para tasks winget recorrentes. Skips benignos consecutivos ativam
		// backoff; falhas consecutivas idênticas abrem o circuit breaker.
		s.updateAntiLoopState(agentID, task, result)

		s.dispatchExecutionNotification(notifyDispatcher, task, entry, &result, s.deferByTask[strings.TrimSpace(task.TaskID)], welcome)
		s.clearDeferState(agentID, task.TaskID, entry.Status)

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

// loadCustomFieldsForExecution consulta o endpoint de runtime e armazena o contexto em memória
// escopado ao executionID. Em caso de erro, retorna um contexto vazio (não bloqueia a execução).
func (s *Service) loadCustomFieldsForExecution(ctx context.Context, cfg RuntimeConfig, executionID, taskID, scriptID, correlationID string) *ExecutionCustomFieldCtx {
	fields, err := s.client.GetRuntimeCustomFields(ctx, cfg, taskID, scriptID, correlationID)
	if err != nil {
		s.logf("automacao: falha ao carregar custom fields para execucao %s: %v", executionID, err)
		empty := &ExecutionCustomFieldCtx{
			Fields:    make(map[string]any),
			rawByName: make(map[string]RuntimeCustomField),
			rawByID:   make(map[string]RuntimeCustomField),
		}
		return empty
	}
	cfCtx := buildExecutionCustomFieldCtx(fields)
	s.cfMu.Lock()
	s.cfCache[executionID] = cfCtx
	s.cfMu.Unlock()
	s.logf("automacao: custom fields carregados para execucao %s: total=%d publicos=%d", executionID, len(fields), len(cfCtx.Fields))
	return cfCtx
}

// clearCustomFieldCtx remove o contexto de custom fields da execução da memória.
func (s *Service) clearCustomFieldCtx(executionID string) {
	s.cfMu.Lock()
	delete(s.cfCache, executionID)
	s.cfMu.Unlock()
}

// postCollectedValues valida localmente e posta cada valor coletado pelo script.
func (s *Service) postCollectedValues(ctx context.Context, cfg RuntimeConfig, executionID string, task AutomationTask, items []CollectedValueRequest, cfCtx *ExecutionCustomFieldCtx, correlationID string) {
	if len(items) == 0 {
		return
	}
	taskID := strings.TrimSpace(task.TaskID)
	scriptID := strings.TrimSpace(task.ScriptID)
	for i := range items {
		// Propaga contexto de taskId/scriptId se não informado pelo script.
		if items[i].TaskID == nil && taskID != "" {
			id := taskID
			items[i].TaskID = &id
		}
		if items[i].ScriptID == nil && scriptID != "" {
			id := scriptID
			items[i].ScriptID = &id
		}
		// Validação local fail-fast.
		if err := validateCollectedWrite(cfCtx, items[i]); err != nil {
			s.logf("automacao: campo coletado bloqueado (execucao=%s): %s", executionID, sanitizeCustomFieldErrForLog(err))
			continue
		}
		_, err := s.client.CollectCustomFieldValue(ctx, cfg, items[i], correlationID)
		if err != nil {
			if _, isBiz := err.(*ErrCustomFieldWrite); isBiz {
				s.logf("automacao: coleta rejeitada pelo servidor (execucao=%s): %s", executionID, sanitizeCustomFieldErrForLog(err))
			} else {
				s.logf("automacao: falha de transporte ao postar campo coletado (execucao=%s): %v", executionID, err)
			}
		}
	}
}

func (s *Service) rebuildRecurringSchedules(ctx context.Context, previous, current []AutomationTask) {
	s.startCron()

	// Constrói mapas de diff para evitar remover/recriar jobs que não mudaram.
	prevByID := make(map[string]AutomationTask, len(previous))
	for _, t := range previous {
		prevByID[t.TaskID] = t
	}
	currByID := make(map[string]AutomationTask, len(current))
	for _, t := range current {
		currByID[t.TaskID] = t
	}

	s.mu.Lock()
	cronInstance := s.cron
	// Remove apenas jobs cuja tarefa foi deletada ou alterada.
	for taskID, entryID := range s.cronEntries {
		curr, currExists := currByID[taskID]
		prev, prevExists := prevByID[taskID]
		if !currExists || !prevExists || cronKey(prev) != cronKey(curr) {
			cronInstance.Remove(entryID)
			delete(s.cronEntries, taskID)
		}
	}
	s.mu.Unlock()

	for _, task := range current {
		if !task.TriggerRecurring || strings.TrimSpace(task.ScheduleCron) == "" {
			continue
		}
		if task.RequiresApproval {
			s.logf("automacao: tarefa %s requer aprovacao e nao sera agendada no cron", task.TaskID)
			continue
		}

		// Pula se já existe um job igual para esta tarefa.
		existingPrev, hadPrev := prevByID[task.TaskID]
		existingCurr, hasCurr := currByID[task.TaskID]
		s.mu.RLock()
		_, hasEntry := s.cronEntries[task.TaskID]
		s.mu.RUnlock()
		if hasEntry && hadPrev && hasCurr && cronKey(existingPrev) == cronKey(existingCurr) {
			continue // job não mudou, mantém
		}

		taskCopy := task
		entryID, err := cronInstance.AddFunc(strings.TrimSpace(task.ScheduleCron), func() {
			agentID := strings.TrimSpace(s.getConfig().AgentID)
			taskID := strings.TrimSpace(taskCopy.TaskID)
			// Dedup persistente: evita reexecucao apos crash do agente no mesmo intervalo do cron.
			// Verifica cooldown de 60s - se a ultima execucao foi ha menos de 60s, pula.
			s.mu.RLock()
			db := s.db
			s.mu.RUnlock()
			if db != nil && agentID != "" && taskID != "" {
				markerKey := "recurring:last:" + taskID
				if lastRun, found, err := db.GetAutomationMarker(agentID, markerKey); err == nil && found {
					if lastTS, parseErr := time.Parse(time.RFC3339, lastRun); parseErr == nil {
						if time.Since(lastTS) < 60*time.Second {
							s.logf("automacao: tarefa recorrente %s pulada - ultima execucao em %s (cooldown)", taskID, lastTS.UTC().Format(time.RFC3339))
							return
						}
					}
				}
			}
			// Circuit breaker: falhas consecutivas pausam a task por failureCooldown.
			if cbRaw, found, err := db.GetAutomationMarker(agentID, "circuit:fail:"+taskID); err == nil && found {
				if cb, ok := parseCircuitBreakerState(cbRaw); ok && circuitBreakerOpen(cb, time.Now().UTC()) {
					s.logf("automacao: tarefa recorrente %s pausada por falhas repetidas (%d) - retoma em %s", taskID, cb.Failures, cb.OpenUntil.UTC().Format(time.RFC3339))
					return
				}
			}
			// Backoff de skips benignos: após consecutiveSkipThreshold skips idênticos,
			// pula slots até SkipUntil (marker skipbackoff).
			if skipRaw, found, err := db.GetAutomationMarker(agentID, "skipbackoff:"+taskID); err == nil && found {
				if st, ok := parseSkipBackoffState(skipRaw); ok && time.Now().UTC().Before(st.SkipUntil) {
					s.logf("automacao: tarefa recorrente %s em backoff de skips (%d consecutivos) - retoma em %s", taskID, st.Count, st.SkipUntil.UTC().Format(time.RFC3339))
					return
				}
			}
			// O marcador é atualizado APÓS a execução via callback onComplete,
			// garantindo que falhas não bloqueiem o próximo slot do cron.
			s.executeTaskAsync(ctx, agentID, taskCopy, sourceForTrigger(TriggerTypeRecurring), TriggerTypeRecurring,
				func(success bool) {
					s.mu.RLock()
					db := s.db
					s.mu.RUnlock()
					if db != nil && agentID != "" && taskID != "" {
						markerKey := "recurring:last:" + taskID
						errutil.LogIfErr(db.SetAutomationMarker(agentID, markerKey, time.Now().UTC().Format(time.RFC3339)), "automacao: atualizar marcador recorrente")
					}
				})
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

// cronKey produz uma chave estável para comparar se a configuração de cron de uma tarefa mudou.
func cronKey(t AutomationTask) string {
	return t.TaskID + "|" + t.ScheduleCron + "|" + strconv.FormatBool(t.TriggerRecurring)
}

// collectValidMarkerKeys coleta todas as chaves de marcadores legítimas para as tarefas atuais.
// Chaves órfãs (de fingerprints/tasks antigas) serão removidas por cleanupOrphanedMarkers.
func (s *Service) collectValidMarkerKeys(current State) []string {
	keys := make([]string, 0, len(current.Tasks)*2+2)

	fingerprint := current.PolicyFingerprint
	for _, task := range current.Tasks {
		taskID := strings.TrimSpace(task.TaskID)
		if taskID == "" {
			continue
		}
		if task.TriggerImmediate {
			// C2: chave inclui LastUpdatedAt (mesmo formato do triggerImmediate).
			keys = append(keys, "immediate:"+fingerprint+":"+taskID+":"+task.LastUpdatedAt)
		}
		if task.TriggerOnAgentCheckIn {
			keys = append(keys, "checkin:"+fingerprint+":"+taskID+":"+task.LastUpdatedAt)
		}
		if task.TriggerRecurring {
			keys = append(keys, "recurring:last:"+taskID)
		}
		// Estado do anti-loop (Fase 1) e do check-in cycle — sem estas chaves,
		// cleanupOrphanedMarkers apagava o backoff/circuit breaker a cada
		// policy-sync, desativando a proteção anti-loop.
		keys = append(keys,
			"skipbackoff:"+taskID,
			"circuit:fail:"+taskID,
			"checkin:cycle:"+taskID,
		)
	}
	// Marcador de sistema: userLoginHandled da sessão atual.
	keys = append(keys, "_sys:userlogin:handled:"+s.processStartAt.Format(time.RFC3339))

	return keys
}

// cleanupOrphanedMarkers remove marcadores órfãos do SQLite para evitar acúmulo
// de lixo (immediate/checkin de fingerprints antigos, recurring:last de tasks deletadas).
func (s *Service) cleanupOrphanedMarkers(agentID string, current State) {
	if s.db == nil || agentID == "" {
		return
	}
	validKeys := s.collectValidMarkerKeys(current)
	deleted, err := s.db.DeleteAutomationMarkersExcept(agentID, validKeys)
	if err != nil {
		s.logf("automacao: falha ao limpar marcadores orfaos: %v", err)
		return
	}
	if deleted > 0 {
		s.logf("automacao: %d marcadores orfaos removidos para agente %s", deleted, agentID)
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
			errutil.LogIfErr(s.db.DeleteAutomationCallback(entry.ID), "automation: remover callback processado")
			continue
		}

		attempts := entry.Attempts + 1
		errutil.LogIfErr(s.db.RescheduleAutomationCallback(entry.ID, attempts, time.Now().Add(callbackBackoff(attempts)), callbackErr.Error()), "automation: reagendar callback")
		if strings.Contains(strings.ToLower(callbackErr.Error()), "404") {
			s.logf("automacao: callback %s rejeitado por 404 para commandId=%s", entry.CallbackType, entry.CommandID)
		}
	}

	s.refreshDerivedState(agentID)
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

func (s *Service) dispatchExecutionNotification(dispatcher func(AutomationNotificationRequest) AutomationNotificationResponse, task AutomationTask, entry database.AutomationExecutionEntry, result *ExecutionResult, deferSnapshot deferState, welcome psadtWelcomeOptions) AutomationNotificationResponse {
	if dispatcher == nil {
		return AutomationNotificationResponse{}
	}
	if !isPackageAction(task.ActionType) {
		return AutomationNotificationResponse{}
	}

	eventType := "install_start"
	severity := "medium"
	title := "Instalacao iniciada"
	message := fmt.Sprintf("Tarefa %s iniciada.", strings.TrimSpace(task.Name))

	if result != nil {
		if result.Success {
			if result.ExitCodeSet && (result.ExitCode == 3010 || result.ExitCode == 1641) {
				eventType = "reboot_required"
				severity = "medium"
				title = "Reinicio necessario"
				message = fmt.Sprintf("Tarefa %s concluida. Reinicie para aplicar as alteracoes.", strings.TrimSpace(task.Name))
			} else {
				eventType = "install_end"
				severity = "low"
				title = "Instalacao concluida"
				message = fmt.Sprintf("Tarefa %s concluida com sucesso.", strings.TrimSpace(task.Name))
			}
		} else {
			eventType = "install_failed"
			severity = "high"
			title = "Instalacao com falha"
			message = fmt.Sprintf("Tarefa %s falhou.", strings.TrimSpace(task.Name))
		}
	}

	if strings.TrimSpace(task.PackageID) != "" {
		message = message + " Pacote: " + strings.TrimSpace(task.PackageID)
	}

	metadata := map[string]any{
		"executionId":                    entry.ExecutionID,
		"taskId":                         entry.TaskID,
		"taskName":                       entry.TaskName,
		"actionType":                     entry.ActionType,
		"installationType":               entry.InstallationType,
		"packageId":                      entry.PackageID,
		"triggerType":                    entry.TriggerType,
		"sourceType":                     entry.SourceType,
		"status":                         entry.Status,
		"correlationId":                  entry.CorrelationID,
		"deferCount":                     deferSnapshot.Count,
		"deferExhausted":                 deferSnapshot.Exhausted,
		"allowDefer":                     welcome.AllowDefer,
		"deferTimes":                     welcome.DeferTimes,
		"deferRunIntervalSeconds":        int(welcome.DeferRunInterval.Seconds()),
		"deferDays":                      welcome.DeferDays,
		"allowDeferCloseProcesses":       welcome.AllowDeferCloseProcesses,
		"closeProcesses":                 append([]string(nil), welcome.CloseProcesses...),
		"blockExecution":                 welcome.BlockExecution,
		"checkDiskSpace":                 welcome.CheckDiskSpace,
		"requiredDiskSpaceMb":            welcome.RequiredDiskSpaceMB,
		"forceCountdownSeconds":          welcome.ForceCountdownSeconds,
		"closeProcessesCountdownSeconds": welcome.CloseProcessesCountdownSeconds,
		"forceCloseProcessesCountdown":   welcome.ForceCloseProcessesCountdown,
	}
	if !deferSnapshot.NextAttempt.IsZero() {
		metadata["nextAttemptAt"] = deferSnapshot.NextAttempt.UTC().Format(time.RFC3339)
	}
	if !deferSnapshot.DeadlineAt.IsZero() {
		metadata["deferDeadlineAt"] = deferSnapshot.DeadlineAt.UTC().Format(time.RFC3339)
	}
	if welcome.DeferTimes > 0 {
		metadata["deferRemaining"] = max(0, welcome.DeferTimes-deferSnapshot.Count)
	}
	if !welcome.DeferDeadline.IsZero() {
		metadata["welcomeDeferDeadlineAt"] = welcome.DeferDeadline.UTC().Format(time.RFC3339)
	}
	if result != nil {
		metadata["success"] = result.Success
		if result.ExitCodeSet {
			metadata["exitCode"] = result.ExitCode
		}
		if strings.TrimSpace(result.ErrorMessage) != "" {
			metadata["error"] = result.ErrorMessage
		}
	}

	notificationID := fmt.Sprintf("automation-%s-%s", strings.TrimSpace(entry.ExecutionID), eventType)
	// C1: apenas install_start de tasks com RequiresApproval pede confirmação.
	// Resultados (install_end/install_failed/reboot_required) nunca pedem —
	// antes usavam require_confirmation e geravam modal de "concluído" que
	// exigia interação do usuário (e timeout registrava "deferred").
	mode := "notify_only"
	if result == nil && task.RequiresApproval {
		mode = "require_confirmation"
	}
	return dispatcher(AutomationNotificationRequest{
		NotificationID: notificationID,
		IdempotencyKey: notificationID,
		Title:          title,
		Message:        message,
		Mode:           mode,
		Severity:       severity,
		EventType:      eventType,
		Layout:         "toast",
		TimeoutSeconds: 45,
		Metadata:       metadata,
	})
}

func (s *Service) logf(format string, args ...any) {
	if s.logger != nil {
		s.logger(formatMessage(format, args...))
	}
}
