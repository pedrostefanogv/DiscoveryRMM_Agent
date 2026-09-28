package automation

import (
	"strings"
	"time"
)

// shouldDeferExecution decide se o usuario adiou a execucao. Vale para
// qualquer tipo de acao quando o prompt esta habilitado — antes so acoes de
// pacote podiam adiar, o que impedia RunScript/CustomCommand de usar o Welcome.
func (s *Service) shouldDeferExecution(task AutomationTask, response AutomationNotificationResponse) bool {
	_ = task
	// Timeout do prompt: acao padrao e CONTINUAR (nao adiar). Um resultado
	// "timeout_policy_applied" NAO adia — a execucao segue normalmente.
	return response.Accepted && isDeferredResult(response.Result)
}

// isDeferredResult identifica o resultado "deferred" devolvido pelo prompt do
// usuario (toast require_confirmation ou Welcome do PSADT).
func isDeferredResult(result string) bool {
	return strings.EqualFold(strings.TrimSpace(result), "deferred") ||
		strings.EqualFold(strings.TrimSpace(result), "defer")
}

// resolveUserPromptTimeoutSeconds normaliza o tempo para a acao padrao de
// continuar quando o usuario nao responde ao prompt. Default 60s; faixa 5..3600.
func resolveUserPromptTimeoutSeconds(task AutomationTask) int {
	const (
		defaultTimeout = 60
		minTimeout     = 5
		maxTimeout     = 3600
	)
	seconds := task.PromptTimeoutSeconds
	if seconds <= 0 {
		return defaultTimeout
	}
	if seconds < minTimeout {
		return minTimeout
	}
	if seconds > maxTimeout {
		return maxTimeout
	}
	return seconds
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
