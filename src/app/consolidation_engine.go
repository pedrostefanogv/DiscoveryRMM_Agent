package app

import (
	"strings"
	"sync"
	"time"

	"discovery/internal/database"
)

// Consolidation window mode constants.
const (
	ConsolidationModeRealtime = "realtime"
	ConsolidationMode1Min     = "1min"
	ConsolidationMode5Min     = "5min"
)

// consolidationWindowDurations maps each window mode to its duration.
var consolidationWindowDurations = map[string]time.Duration{
	ConsolidationModeRealtime: 0,
	ConsolidationMode1Min:     1 * time.Minute,
	ConsolidationMode5Min:     5 * time.Minute,
}

// ConsolidationEngine controls send-batching windows per data type.
// It is feature-flagged: disabled by default, falling back to realtime behavior.
// When enabled, it uses the persisted window state in SQLite to decide whether
// enough time has elapsed since the last flush for a given data type.
type ConsolidationEngine struct {
	mu       sync.RWMutex
	enabled  bool
	policies map[string]string // dataType → windowMode
	db       *database.DB
	agentID  string
}

// newConsolidationEngine creates a disabled engine. Call Enable() to activate.
func newConsolidationEngine(db *database.DB, agentID string) *ConsolidationEngine {
	return &ConsolidationEngine{
		enabled:  false,
		policies: make(map[string]string),
		db:       db,
		agentID:  strings.TrimSpace(agentID),
	}
}

// SetAgentID refreshes the agent identifier used for persisted window state.
func (e *ConsolidationEngine) SetAgentID(agentID string) {
	e.mu.Lock()
	e.agentID = strings.TrimSpace(agentID)
	e.mu.Unlock()
}

// Enable activates the consolidation engine.
func (e *ConsolidationEngine) Enable() {
	e.mu.Lock()
	e.enabled = true
	e.mu.Unlock()
}

// Disable deactivates the engine, restoring realtime behavior for all data types.
func (e *ConsolidationEngine) Disable() {
	e.mu.Lock()
	e.enabled = false
	e.mu.Unlock()
}

// IsEnabled reports whether the engine is currently active.
func (e *ConsolidationEngine) IsEnabled() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.enabled
}

// SetPolicy configures the window mode for a data type.
func (e *ConsolidationEngine) SetPolicy(dataType, windowMode string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.policies[strings.TrimSpace(dataType)] = strings.TrimSpace(windowMode)
}

// GetWindowMode returns the effective window mode for a data type.
// Falls back to realtime when not configured.
func (e *ConsolidationEngine) GetWindowMode(dataType string) string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if mode, ok := e.policies[strings.TrimSpace(dataType)]; ok && mode != "" {
		return mode
	}
	return ConsolidationModeRealtime
}

// ShouldFlush reports whether the given data type should be sent right now.
// Returns true (flush immediately) when:
//   - engine is disabled
//   - window mode is realtime or unknown
//   - no window is currently open
//   - the window duration has elapsed since the last flush
//
// Returns false (skip / batch) otherwise.
func (e *ConsolidationEngine) ShouldFlush(dataType string, now time.Time) (bool, error) {
	if !e.IsEnabled() {
		return true, nil
	}
	mode := e.GetWindowMode(dataType)
	dur, known := consolidationWindowDurations[mode]
	if !known || dur == 0 {
		return true, nil
	}
	if e.db == nil || e.agentID == "" {
		return true, nil
	}
	state, found, err := e.db.GetConsolidationWindowState(e.agentID, dataType)
	if err != nil {
		// Fallback to flush on DB error so we don't silently drop data.
		return true, err
	}
	if !found || state.WindowStartAt.IsZero() {
		return true, nil
	}
	baseline := state.LastFlushAt
	if baseline.IsZero() {
		baseline = state.WindowStartAt
	}
	return now.Sub(baseline) >= dur, nil
}

// RecordFlush persists the post-flush window state for a given data type.
func (e *ConsolidationEngine) RecordFlush(dataType string, now time.Time) error {
	if e.db == nil || e.agentID == "" {
		return nil
	}
	agentID := strings.TrimSpace(e.agentID)
	if agentID == "" {
		return nil
	}
	mode := e.GetWindowMode(dataType)
	// Preserve existing WindowStartAt to track overall window age.
	state, found, _ := e.db.GetConsolidationWindowState(agentID, dataType)
	windowStart := now
	if found && !state.WindowStartAt.IsZero() {
		windowStart = state.WindowStartAt
	}
	return e.db.UpsertConsolidationWindowState(database.ConsolidationWindowStateEntry{
		AgentID:       agentID,
		DataType:      dataType,
		WindowMode:    mode,
		WindowStartAt: windowStart,
		LastFlushAt:   now,
		UpdatedAt:     now,
	})
}

// ApplyAgentConfig updates consolidation policies from the remote agent configuration.
func (e *ConsolidationEngine) ApplyAgentConfig(cfg AgentConfiguration) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.enabled = false
	e.policies = make(map[string]string)

	if cfg.Rollout.EnableConsolidationEngine != nil && !*cfg.Rollout.EnableConsolidationEngine {
		return
	}
	if cfg.Consolidation.Enabled == nil || !*cfg.Consolidation.Enabled {
		return
	}

	e.enabled = true
	for _, policy := range cfg.Consolidation.Policies {
		dataType := strings.TrimSpace(strings.ToLower(policy.DataType))
		if dataType == "" {
			continue
		}
		e.policies[dataType] = normalizeConsolidationWindowMode(policy.WindowMode)
	}
}
