package automation

import (
	"fmt"
	"strings"
	"time"

	"discovery/app/core/errutil"
)

// Anti-loop para tasks winget recorrentes. Evita que skips benignos
// ("pacote ja atualizado") ou falhas persistentes causem reexecução
// cega a cada slot do cron.

const (
	// consecutiveSkipThreshold: número de skips benignos consecutivos
	// que ativa o backoff.
	consecutiveSkipThreshold = 3
	// backoffSlots: número de slots do cron a pular quando o backoff ativa.
	backoffSlots = 2
	// skipSeriesResetWindow: janela para considerar a série de skips "quebrada".
	skipSeriesResetWindow = 6 * time.Hour
	// minBackoffInterval: intervalo mínimo estimado entre slots no cálculo do backoff.
	minBackoffInterval = time.Hour
	// defaultBackoffInterval: fallback quando não é possível estimar o intervalo.
	defaultBackoffInterval = 6 * time.Hour
	// failureThreshold: falhas consecutivas com o mesmo erro que ativam o circuit breaker.
	failureThreshold = 3
	// failureCooldown: tempo de pausa do circuit breaker.
	failureCooldown = 24 * time.Hour
	// maxMarkerErrorLen: limite do erro persistido no marker.
	maxMarkerErrorLen = 500
)

// skipOutcome classifica o resultado de uma execução de pacote winget.
type skipOutcome int

const (
	skipNone           skipOutcome = iota // execução real (instalou/atualizou/falhou)
	skipBenign                            // "ja atualizado" / "ja instalado" — nada a fazer
	skipPackageMissing                    // "nao encontrado" — ausência (UpdateOrInstall trata antes)
)

// classifyPackageResult classifica o resultado de uma execução winget.
// As mensagens vêm de shouldSkipWingetAction (executor.go).
func classifyPackageResult(result ExecutionResult) skipOutcome {
	if !result.Success {
		return skipNone
	}
	out := strings.TrimSpace(result.Output)
	switch {
	case strings.Contains(out, "ja atualizado"), strings.Contains(out, "ja instalado"):
		return skipBenign
	case strings.Contains(out, "nao encontrado"):
		return skipPackageMissing
	}
	return skipNone
}

// isPackageAction já está definida em service_helpers.go.

// ── Backoff de skips benignos ────────────────────────────────────────────────

// skipBackoffState é o estado persistido do backoff de skips (marker SQLite).
// Formato: count|lastRun|skipUntil (timestamps RFC3339).
type skipBackoffState struct {
	Count     int
	LastRun   time.Time
	SkipUntil time.Time
}

func parseSkipBackoffState(raw string) (skipBackoffState, bool) {
	parts := strings.Split(raw, "|")
	if len(parts) != 3 {
		return skipBackoffState{}, false
	}
	var st skipBackoffState
	if _, err := fmt.Sscanf(parts[0], "%d", &st.Count); err != nil {
		return skipBackoffState{}, false
	}
	if t, err := time.Parse(time.RFC3339, parts[1]); err == nil {
		st.LastRun = t
	}
	if t, err := time.Parse(time.RFC3339, parts[2]); err == nil {
		st.SkipUntil = t
	}
	return st, true
}

func encodeSkipBackoffState(st skipBackoffState) string {
	return fmt.Sprintf("%d|%s|%s", st.Count, st.LastRun.UTC().Format(time.RFC3339), st.SkipUntil.UTC().Format(time.RFC3339))
}

// ── Circuit breaker (falhas) ────────────────────────────────────────────────

// circuitBreakerState é o estado persistido do circuit breaker (marker SQLite).
// Formato: failures|lastFailAt|openUntil|lastError.
type circuitBreakerState struct {
	Failures   int
	LastError  string
	LastFailAt time.Time
	OpenUntil  time.Time
}

func parseCircuitBreakerState(raw string) (circuitBreakerState, bool) {
	parts := strings.SplitN(raw, "|", 4)
	if len(parts) < 3 {
		return circuitBreakerState{}, false
	}
	var st circuitBreakerState
	if _, err := fmt.Sscanf(parts[0], "%d", &st.Failures); err != nil {
		return circuitBreakerState{}, false
	}
	if t, err := time.Parse(time.RFC3339, parts[1]); err == nil {
		st.LastFailAt = t
	}
	if t, err := time.Parse(time.RFC3339, parts[2]); err == nil {
		st.OpenUntil = t
	}
	if len(parts) == 4 {
		st.LastError = parts[3]
	}
	return st, true
}

func encodeCircuitBreakerState(st circuitBreakerState) string {
	return fmt.Sprintf("%d|%s|%s|%s",
		st.Failures,
		st.LastFailAt.UTC().Format(time.RFC3339),
		st.OpenUntil.UTC().Format(time.RFC3339),
		sanitizeMarkerText(st.LastError, maxMarkerErrorLen))
}

// sanitizeMarkerText remove delimitadores e limita tamanho para o marker.
func sanitizeMarkerText(s string, max int) string {
	s = strings.NewReplacer("|", "/", "\n", " ", "\r", " ").Replace(s)
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// circuitBreakerOpen retorna true se o circuit breaker está aberto (pausado).
func circuitBreakerOpen(st circuitBreakerState, now time.Time) bool {
	return !st.OpenUntil.IsZero() && now.Before(st.OpenUntil)
}

// ── Atualização de estado pós-execução ──────────────────────────────────────

// updateAntiLoopState atualiza os markers de anti-loop (backoff de skips e
// circuit breaker) após uma execução de task. Chamado no executeTaskAsync
// após obter o resultado.
func (s *Service) updateAntiLoopState(agentID string, task AutomationTask, result ExecutionResult) {
	if s == nil || s.db == nil || agentID == "" {
		return
	}
	taskID := strings.TrimSpace(task.TaskID)
	if taskID == "" {
		return
	}
	now := time.Now().UTC()

	// ── Circuit breaker (vale para qualquer task) ───────────────────────────
	cbKey := "circuit:fail:" + taskID
	if !result.Success {
		cbRaw, found, _ := s.db.GetAutomationMarker(agentID, cbKey)
		cb, _ := parseCircuitBreakerState(cbRaw)
		if !found {
			cb = circuitBreakerState{}
		}
		// Série quebrada: última falha foi há mais de failureCooldown.
		if !cb.LastFailAt.IsZero() && now.Sub(cb.LastFailAt) > failureCooldown {
			cb = circuitBreakerState{}
		}
		cb.Failures++
		cb.LastError = result.ErrorMessage
		cb.LastFailAt = now
		if cb.Failures >= failureThreshold {
			cb.OpenUntil = now.Add(failureCooldown)
			s.logf("automacao: circuit breaker aberto para task=%s (%d falhas consecutivas) - pausada ate %s", taskID, cb.Failures, cb.OpenUntil.UTC().Format(time.RFC3339))
		}
		errutil.LogIfErr(s.db.SetAutomationMarker(agentID, cbKey, encodeCircuitBreakerState(cb)), "automacao: atualizar circuit breaker")
	} else if cbRaw, found, _ := s.db.GetAutomationMarker(agentID, cbKey); found {
		if cb, ok := parseCircuitBreakerState(cbRaw); ok && (cb.Failures > 0 || !cb.OpenUntil.IsZero()) {
			// Sucesso reseta o circuit breaker: grava estado zerado (não há API
			// de remoção individual de marker).
			s.db.SetAutomationMarker(agentID, cbKey, encodeCircuitBreakerState(circuitBreakerState{}))
		}
	}

	// ── Backoff de skips benignos (apenas tasks de pacote) ─────────────────
	skipKey := "skipbackoff:" + taskID
	if !isPackageAction(task.ActionType) {
		return
	}
	outcome := classifyPackageResult(result)
	if outcome == skipNone {
		// Execução real (efetivo ou falha) reseta o backoff: grava estado zerado.
		if _, found, _ := s.db.GetAutomationMarker(agentID, skipKey); found {
			s.db.SetAutomationMarker(agentID, skipKey, encodeSkipBackoffState(skipBackoffState{}))
		}
		return
	}
	if outcome == skipPackageMissing {
		// Ausência é tratada pelo UpdateOrInstall (fallback install); não conta
		// como skip benigno — a task seguinte instalará ou falhará de verdade.
		return
	}

	// Skip benigno: incrementa contador; ao atingir o threshold, ativa backoff.
	stRaw, found, _ := s.db.GetAutomationMarker(agentID, skipKey)
	st, _ := parseSkipBackoffState(stRaw)
	if !found {
		st = skipBackoffState{}
	}
	// Série quebrada: último skip foi há mais de skipSeriesResetWindow.
	if !st.LastRun.IsZero() && now.Sub(st.LastRun) > skipSeriesResetWindow {
		st = skipBackoffState{}
	}

	// Estima o intervalo entre slots: tempo entre os dois últimos skips
	// (fallback defaultBackoffInterval).
	interval := defaultBackoffInterval
	if !st.LastRun.IsZero() {
		if d := now.Sub(st.LastRun); d > minBackoffInterval {
			interval = d
		} else {
			interval = minBackoffInterval
		}
	}

	st.Count++
	st.LastRun = now
	if st.Count >= consecutiveSkipThreshold {
		st.SkipUntil = now.Add(time.Duration(st.Count-consecutiveSkipThreshold+1) * backoffSlots * interval)
		s.logf("automacao: backoff de skips ativado para task=%s (%d skips benignos) - pula ate %s", taskID, st.Count, st.SkipUntil.UTC().Format(time.RFC3339))
	}
	errutil.LogIfErr(s.db.SetAutomationMarker(agentID, skipKey, encodeSkipBackoffState(st)), "automacao: atualizar skip backoff")
}
