package automation

import (
	"encoding/json"
	"strings"

	"discovery/internal/database"
)

func (s *Service) clearDeferState(agentID, taskID, finalStatus string) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return
	}
	agentID = strings.TrimSpace(agentID)
	finalStatus = strings.TrimSpace(finalStatus)
	if finalStatus == "" {
		finalStatus = "completed"
	}

	var existing deferState
	s.mu.Lock()
	existing = s.deferByTask[taskID]
	delete(s.deferByTask, taskID)
	s.mu.Unlock()

	if existing.Count == 0 {
		if s.db != nil && agentID != "" {
			_ = s.db.UpsertAutomationDeferState(database.AutomationDeferStateEntry{
				AgentID:        agentID,
				TaskID:         taskID,
				FinalStatus:    finalStatus,
				DeferCount:     0,
				DeferExhausted: false,
			})
		}
		return
	}
	s.persistDeferState(agentID, taskID, existing, finalStatus)
}

func (s *Service) loadDeferStateForAgent(agentID string) {
	agentID = strings.TrimSpace(agentID)
	if s.db == nil || agentID == "" {
		return
	}

	states, err := s.db.ListAutomationDeferStates(agentID, 200)
	if err != nil {
		s.logf("automacao: falha ao carregar estado de deferimento: %v", err)
		return
	}

	loaded := make(map[string]deferState)
	for _, item := range states {
		if strings.TrimSpace(item.FinalStatus) != "" && !strings.EqualFold(strings.TrimSpace(item.FinalStatus), "deferred") {
			continue
		}
		taskID := strings.TrimSpace(item.TaskID)
		if taskID == "" {
			continue
		}
		loaded[taskID] = deferState{
			ExecutionID:  strings.TrimSpace(item.ExecutionID),
			Count:        item.DeferCount,
			FirstDeferAt: item.FirstDeferAt,
			LastDeferAt:  item.LastDeferAt,
			NextAttempt:  item.NextAttemptAt,
			DeadlineAt:   item.DeadlineAt,
			Exhausted:    item.DeferExhausted,
		}
	}

	s.mu.Lock()
	for taskID, state := range loaded {
		s.deferByTask[taskID] = state
	}
	s.mu.Unlock()
}

func (s *Service) persistDeferState(agentID, taskID string, current deferState, finalStatus string) {
	if s.db == nil {
		return
	}
	agentID = strings.TrimSpace(agentID)
	taskID = strings.TrimSpace(taskID)
	if agentID == "" || taskID == "" {
		return
	}

	if err := s.db.UpsertAutomationDeferState(database.AutomationDeferStateEntry{
		AgentID:        agentID,
		TaskID:         taskID,
		ExecutionID:    strings.TrimSpace(current.ExecutionID),
		DeferCount:     current.Count,
		FirstDeferAt:   current.FirstDeferAt,
		LastDeferAt:    current.LastDeferAt,
		DeadlineAt:     current.DeadlineAt,
		NextAttemptAt:  current.NextAttempt,
		DeferExhausted: current.Exhausted,
		FinalStatus:    strings.TrimSpace(finalStatus),
	}); err != nil {
		s.logf("automacao: falha ao persistir estado de deferimento task=%s: %v", taskID, err)
	}
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
