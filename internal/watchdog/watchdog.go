package watchdog

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"sync"
	"time"
)

// Component represents a monitored component in the system.
type Component string

const (
	ComponentTray      Component = "tray"
	ComponentAI        Component = "ai_service"
	ComponentAgent     Component = "agent_connection"
	ComponentInventory Component = "inventory"
	ComponentUI        Component = "ui_runtime"
)

// Status represents the health status of a component.
type Status string

const (
	StatusHealthy   Status = "healthy"
	StatusDegraded  Status = "degraded"
	StatusUnhealthy Status = "unhealthy"
	StatusUnknown   Status = "unknown"
)

// HealthCheck represents a single health check result.
type HealthCheck struct {
	Component   Component
	Status      Status
	LastBeat    time.Time
	Message     string
	CheckedAt   time.Time
	Recoverable bool
}

// RecoveryAction is called when a component becomes unhealthy.
type RecoveryAction func(component Component) error

// Config defines watchdog behavior.
type Config struct {
	// HeartbeatInterval is how often components should send heartbeats
	HeartbeatInterval time.Duration
	// UnhealthyThreshold is how long without heartbeat before unhealthy
	UnhealthyThreshold time.Duration
	// DegradedThreshold is how long without heartbeat before degraded
	DegradedThreshold time.Duration
	// CheckInterval is how often to run health checks
	CheckInterval time.Duration
	// EnableAutoRecovery enables automatic recovery attempts
	EnableAutoRecovery bool
	// MaxRecoveryAttempts limits recovery retries per component
	MaxRecoveryAttempts int
	// UnhealthyNotifyCooldown limits how often unhealthy callbacks are emitted per component
	UnhealthyNotifyCooldown time.Duration
	// ComponentThresholds allows per-component degraded/unhealthy overrides
	ComponentThresholds map[Component]Thresholds
}

// Thresholds defines degraded/unhealthy timing for a component.
type Thresholds struct {
	DegradedThreshold  time.Duration
	UnhealthyThreshold time.Duration
}

// DefaultConfig returns sensible defaults for watchdog.
func DefaultConfig() Config {
	return Config{
		HeartbeatInterval:       30 * time.Second,
		UnhealthyThreshold:      90 * time.Second,
		DegradedThreshold:       60 * time.Second,
		CheckInterval:           15 * time.Second,
		EnableAutoRecovery:      true,
		MaxRecoveryAttempts:     3,
		UnhealthyNotifyCooldown: 60 * time.Second,
		ComponentThresholds: map[Component]Thresholds{
			ComponentInventory: {
				DegradedThreshold:  90 * time.Second,
				UnhealthyThreshold: 150 * time.Second,
			},
		},
	}
}

// Watchdog monitors system health and attempts automatic recovery.
type Watchdog struct {
	cfg Config

	mu            sync.RWMutex
	heartbeats    map[Component]time.Time
	recoveryFuncs map[Component]RecoveryAction
	recoveryCount map[Component]int
	lastNotified  map[Component]time.Time
	lastCheck     time.Time

	onUnhealthy func(HealthCheck)

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New creates a new watchdog with the given configuration.
func New(cfg Config) *Watchdog {
	copiedThresholds := make(map[Component]Thresholds, len(cfg.ComponentThresholds))
	for component, thresholds := range cfg.ComponentThresholds {
		copiedThresholds[component] = thresholds
	}
	cfg.ComponentThresholds = copiedThresholds

	return &Watchdog{
		cfg:           cfg,
		heartbeats:    make(map[Component]time.Time),
		recoveryFuncs: make(map[Component]RecoveryAction),
		recoveryCount: make(map[Component]int),
		lastNotified:  make(map[Component]time.Time),
	}
}

// Start begins monitoring.
func (w *Watchdog) Start(ctx context.Context) {
	w.ctx, w.cancel = context.WithCancel(ctx)
	w.wg.Add(1)

	go w.safeRun(func() {
		defer w.wg.Done()
		ticker := time.NewTicker(w.cfg.CheckInterval)
		defer ticker.Stop()

		for {
			select {
			case <-w.ctx.Done():
				return
			case <-ticker.C:
				w.performHealthChecks()
			}
		}
	})

	log.Printf("[watchdog] iniciado (intervalo: %v, degraded: %v, unhealthy: %v)",
		w.cfg.CheckInterval, w.cfg.DegradedThreshold, w.cfg.UnhealthyThreshold)
}

// Stop halts the watchdog.
func (w *Watchdog) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
	w.wg.Wait()
	log.Println("[watchdog] parado")
}

// Heartbeat records that a component is alive.
func (w *Watchdog) Heartbeat(component Component) {
	w.mu.Lock()
	w.heartbeats[component] = time.Now()
	w.mu.Unlock()
}

// RegisterRecovery sets the recovery action for a component.
func (w *Watchdog) RegisterRecovery(component Component, action RecoveryAction) {
	w.mu.Lock()
	w.recoveryFuncs[component] = action
	w.mu.Unlock()
}

// OnUnhealthy sets a callback for when components become unhealthy.
func (w *Watchdog) OnUnhealthy(callback func(HealthCheck)) {
	w.mu.Lock()
	w.onUnhealthy = callback
	w.mu.Unlock()
}

// GetHealth returns the current health status of all monitored components.
func (w *Watchdog) GetHealth() []HealthCheck {
	w.mu.RLock()
	defer w.mu.RUnlock()

	now := time.Now()
	checks := make([]HealthCheck, 0, len(w.heartbeats))

	for component, lastBeat := range w.heartbeats {
		checks = append(checks, w.buildHealthCheck(component, lastBeat, now))
	}

	return checks
}

// GetComponentHealth returns health status for a specific component.
func (w *Watchdog) GetComponentHealth(component Component) HealthCheck {
	w.mu.RLock()
	defer w.mu.RUnlock()

	lastBeat, ok := w.heartbeats[component]
	if !ok {
		return HealthCheck{
			Component: component,
			Status:    StatusUnknown,
			Message:   "componente não monitorado",
			CheckedAt: time.Now(),
		}
	}

	return w.buildHealthCheck(component, lastBeat, time.Now())
}

// ResetRecoveryCount resets the recovery attempt counter for a component.
func (w *Watchdog) ResetRecoveryCount(component Component) {
	w.mu.Lock()
	w.recoveryCount[component] = 0
	w.mu.Unlock()
}

func (w *Watchdog) buildHealthCheck(component Component, lastBeat, now time.Time) HealthCheck {
	elapsed := now.Sub(lastBeat)
	degradedThreshold, unhealthyThreshold := w.thresholdsFor(component)
	check := HealthCheck{
		Component:   component,
		LastBeat:    lastBeat,
		CheckedAt:   now,
		Recoverable: w.recoveryFuncs[component] != nil,
	}

	if elapsed > unhealthyThreshold {
		check.Status = StatusUnhealthy
		check.Message = fmt.Sprintf("sem heartbeat ha %v (limite: %v)", elapsed.Round(time.Second), unhealthyThreshold)
	} else if elapsed > degradedThreshold {
		check.Status = StatusDegraded
		check.Message = fmt.Sprintf("heartbeat atrasado em %v (limite: %v)", elapsed.Round(time.Second), degradedThreshold)
	} else {
		check.Status = StatusHealthy
		check.Message = fmt.Sprintf("saudavel (ultimo heartbeat: %v atrás)", elapsed.Round(time.Second))
	}

	return check
}

func (w *Watchdog) thresholdsFor(component Component) (time.Duration, time.Duration) {
	degraded := w.cfg.DegradedThreshold
	unhealthy := w.cfg.UnhealthyThreshold

	if override, ok := w.cfg.ComponentThresholds[component]; ok {
		if override.DegradedThreshold > 0 {
			degraded = override.DegradedThreshold
		}
		if override.UnhealthyThreshold > 0 {
			unhealthy = override.UnhealthyThreshold
		}
	}

	if unhealthy <= 0 {
		unhealthy = 1 * time.Second
	}
	if degraded <= 0 || degraded >= unhealthy {
		degraded = unhealthy / 2
	}

	return degraded, unhealthy
}

func (w *Watchdog) performHealthChecks() {
	checks := w.GetHealth()
	w.mu.Lock()
	w.lastCheck = time.Now()
	w.mu.Unlock()

	for _, check := range checks {
		switch check.Status {
		case StatusHealthy:
			// Reset recovery count if component is now healthy (recovered)
			w.mu.Lock()
			if w.recoveryCount[check.Component] > 0 {
				log.Printf("[watchdog] %s totalmente recuperado - resetando contador", check.Component)
				w.recoveryCount[check.Component] = 0
			}
			w.mu.Unlock()
		case StatusUnhealthy:
			log.Printf("[watchdog] ALERTA: %s %s - %s", check.Component, check.Status, check.Message)
			w.handleUnhealthy(check)
		case StatusDegraded:
			log.Printf("[watchdog] AVISO: %s %s - %s", check.Component, check.Status, check.Message)
		}
	}
}

func (w *Watchdog) handleUnhealthy(check HealthCheck) {
	// Notify callback if set (with per-component cooldown to avoid alert storms)
	w.mu.Lock()
	callback := w.onUnhealthy
	notify := true
	if w.cfg.UnhealthyNotifyCooldown > 0 {
		if last := w.lastNotified[check.Component]; !last.IsZero() && time.Since(last) < w.cfg.UnhealthyNotifyCooldown {
			notify = false
		}
	}
	if notify {
		w.lastNotified[check.Component] = time.Now()
	}
	w.mu.Unlock()

	if callback != nil && notify {
		go w.safeRun(func() {
			callback(check)
		})
	}

	// Attempt recovery if enabled
	if !w.cfg.EnableAutoRecovery || !check.Recoverable {
		return
	}

	w.mu.Lock()
	attempts := w.recoveryCount[check.Component]
	if attempts >= w.cfg.MaxRecoveryAttempts {
		w.mu.Unlock()
		log.Printf("[watchdog] %s: limite de tentativas de recuperação atingido (%d)", check.Component, attempts)
		return
	}
	w.recoveryCount[check.Component]++
	recoveryFunc := w.recoveryFuncs[check.Component]
	w.mu.Unlock()

	log.Printf("[watchdog] tentando recuperar %s (tentativa %d/%d)", check.Component, attempts+1, w.cfg.MaxRecoveryAttempts)

	go w.safeRun(func() {
		if err := recoveryFunc(check.Component); err != nil {
			log.Printf("[watchdog] falha ao recuperar %s: %v", check.Component, err)
		} else {
			log.Printf("[watchdog] ação de recuperação executada para %s", check.Component)
		}
	})
}

// safeRun executes a function with panic recovery.
func (w *Watchdog) safeRun(fn func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[watchdog] PANIC recuperado: %v\n%s", r, debug.Stack())
		}
	}()
	fn()
}

// SafeGo is a helper to run a goroutine with automatic panic recovery and logging.
func SafeGo(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[watchdog] PANIC em goroutine '%s': %v\n%s", name, r, debug.Stack())
			}
		}()
		fn()
	}()
}

// SafeGoWithContext runs a goroutine with context, panic recovery, and optional heartbeat.
func SafeGoWithContext(ctx context.Context, name string, w *Watchdog, component Component, fn func(context.Context)) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[watchdog] PANIC em goroutine '%s' (%s): %v\n%s", name, component, r, debug.Stack())
			}
		}()

		// Send initial heartbeat
		if w != nil && component != "" {
			w.Heartbeat(component)
		}

		fn(ctx)
	}()
}
