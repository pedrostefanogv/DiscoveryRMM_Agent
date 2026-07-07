package automation

import (
	"strings"
	"time"
)

func (s *Service) shouldDeferExecution(task AutomationTask, response AutomationNotificationResponse) bool {
	if !isPackageAction(task.ActionType) {
		return false
	}
	if !response.Accepted {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(response.Result), "deferred")
}

func (s *Service) recordAndGetNextDefer(agentID, executionID string, task AutomationTask, current deferState, welcome psadtWelcomeOptions) time.Time {
	taskID := strings.TrimSpace(task.TaskID)
	if taskID == "" {
		return time.Time{}
	}
	agentID = strings.TrimSpace(agentID)
	executionID = strings.TrimSpace(executionID)

	if !welcome.AllowDefer {
		current.Exhausted = true
		s.mu.Lock()
		s.deferByTask[taskID] = current
		s.mu.Unlock()
		s.persistDeferState(agentID, taskID, current, "deferred")
		return time.Time{}
	}

	maxTimes := welcome.DeferTimes
	if maxTimes <= 0 {
		maxTimes = defaultDeferTimes
	}
	if current.Count >= maxTimes {
		current.Exhausted = true
		s.mu.Lock()
		s.deferByTask[taskID] = current
		s.mu.Unlock()
		s.persistDeferState(agentID, taskID, current, "deferred")
		return time.Time{}
	}

	now := time.Now().UTC()
	current.ExecutionID = executionID
	if current.FirstDeferAt.IsZero() {
		current.FirstDeferAt = now
	}

	deadline := current.DeadlineAt
	if !welcome.DeferDeadline.IsZero() {
		if deadline.IsZero() || welcome.DeferDeadline.Before(deadline) {
			deadline = welcome.DeferDeadline
		}
	}
	if welcome.DeferDays > 0 {
		windowDeadline := current.FirstDeferAt.Add(time.Duration(welcome.DeferDays * float64(24*time.Hour)))
		if deadline.IsZero() || windowDeadline.Before(deadline) {
			deadline = windowDeadline
		}
	}
	if !deadline.IsZero() && (now.Equal(deadline) || now.After(deadline)) {
		current.DeadlineAt = deadline
		current.Exhausted = true
		s.mu.Lock()
		s.deferByTask[taskID] = current
		s.mu.Unlock()
		s.persistDeferState(agentID, taskID, current, "deferred")
		return time.Time{}
	}
	current.DeadlineAt = deadline

	current.Count++
	current.LastDeferAt = now
	interval := welcome.DeferRunInterval
	if interval <= 0 {
		interval = defaultDeferInterval
	}
	current.NextAttempt = now.Add(interval)
	current.Exhausted = current.Count >= maxTimes

	s.mu.Lock()
	s.deferByTask[taskID] = current
	s.mu.Unlock()
	s.persistDeferState(agentID, taskID, current, "deferred")

	if current.Count > maxTimes {
		return time.Time{}
	}
	return current.NextAttempt
}

func (s *Service) resolvePSADTPolicyLocked() PSADTPolicy {
	if s.psadtResolver == nil {
		return normalizePSADTPolicy(PSADTPolicy{})
	}
	return normalizePSADTPolicy(s.psadtResolver())
}
