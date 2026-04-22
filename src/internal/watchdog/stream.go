package watchdog

import (
	"context"
	"log"
	"sync"
	"time"
)

// StreamMonitor tracks long-running streaming operations for stalls.
type StreamMonitor struct {
	mu           sync.Mutex
	lastActivity time.Time
	timeout      time.Duration
	name         string
	onStall      func()
	cancel       context.CancelFunc
	done         chan struct{}
}

// NewStreamMonitor creates a monitor for a streaming operation.
// It will call onStall if no activity is detected within timeout.
func NewStreamMonitor(name string, timeout time.Duration, onStall func()) *StreamMonitor {
	return &StreamMonitor{
		name:         name,
		timeout:      timeout,
		lastActivity: time.Now(),
		onStall:      onStall,
		done:         make(chan struct{}),
	}
}

// Start begins monitoring the stream.
func (sm *StreamMonitor) Start(ctx context.Context) {
	monitorCtx, cancel := context.WithCancel(ctx)
	sm.cancel = cancel

	SafeGo("stream-monitor-"+sm.name, func() {
		ticker := time.NewTicker(sm.timeout / 3) // Check 3 times per timeout period
		defer ticker.Stop()
		defer close(sm.done)

		for {
			select {
			case <-monitorCtx.Done():
				return
			case <-ticker.C:
				sm.mu.Lock()
				elapsed := time.Since(sm.lastActivity)
				sm.mu.Unlock()

				if elapsed > sm.timeout {
					log.Printf("[stream-monitor] ALERTA: stream '%s' travado ha %v (timeout: %v)",
						sm.name, elapsed.Round(time.Second), sm.timeout)
					if sm.onStall != nil {
						sm.onStall()
					}
					return
				}
			}
		}
	})
}

// Activity records that the stream is still active.
func (sm *StreamMonitor) Activity() {
	sm.mu.Lock()
	sm.lastActivity = time.Now()
	sm.mu.Unlock()
}

// Stop halts the monitor.
func (sm *StreamMonitor) Stop() {
	if sm.cancel != nil {
		sm.cancel()
	}
	<-sm.done
}

// OperationMonitor tracks an operation with automatic timeout and panic recovery.
type OperationMonitor struct {
	name      string
	timeout   time.Duration
	watchdog  *Watchdog
	component Component
}

// NewOperationMonitor creates a monitor for a single operation.
func NewOperationMonitor(name string, timeout time.Duration, w *Watchdog, component Component) *OperationMonitor {
	return &OperationMonitor{
		name:      name,
		timeout:   timeout,
		watchdog:  w,
		component: component,
	}
}

// Run executes the operation with timeout and panic recovery.
func (om *OperationMonitor) Run(ctx context.Context, fn func(context.Context) error) error {
	opCtx, cancel := context.WithTimeout(ctx, om.timeout)
	defer cancel()

	errChan := make(chan error, 1)
	panicChan := make(chan interface{}, 1)

	// Send heartbeat before starting
	if om.watchdog != nil && om.component != "" {
		om.watchdog.Heartbeat(om.component)
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				panicChan <- r
			}
		}()

		// Send heartbeat periodically during operation
		if om.watchdog != nil && om.component != "" {
			ticker := time.NewTicker(15 * time.Second)
			defer ticker.Stop()

			go func() {
				for {
					select {
					case <-opCtx.Done():
						return
					case <-ticker.C:
						om.watchdog.Heartbeat(om.component)
					}
				}
			}()
		}

		errChan <- fn(opCtx)
	}()

	select {
	case err := <-errChan:
		return err
	case p := <-panicChan:
		log.Printf("[operation-monitor] PANIC na operação '%s' (%s): %v", om.name, om.component, p)
		return context.DeadlineExceeded
	case <-opCtx.Done():
		log.Printf("[operation-monitor] TIMEOUT na operação '%s' apos %v", om.name, om.timeout)
		return opCtx.Err()
	}
}

// PeriodicHeartbeat sends heartbeats at regular intervals for long-running goroutines.
type PeriodicHeartbeat struct {
	watchdog  *Watchdog
	component Component
	interval  time.Duration
	cancel    context.CancelFunc
	done      chan struct{}
}

// NewPeriodicHeartbeat creates a heartbeat sender for a component.
func NewPeriodicHeartbeat(w *Watchdog, component Component, interval time.Duration) *PeriodicHeartbeat {
	return &PeriodicHeartbeat{
		watchdog:  w,
		component: component,
		interval:  interval,
		done:      make(chan struct{}),
	}
}

// Start begins sending periodic heartbeats.
func (ph *PeriodicHeartbeat) Start(ctx context.Context) {
	beatCtx, cancel := context.WithCancel(ctx)
	ph.cancel = cancel

	// Send immediate heartbeat
	ph.watchdog.Heartbeat(ph.component)

	SafeGo("periodic-heartbeat-"+string(ph.component), func() {
		ticker := time.NewTicker(ph.interval)
		defer ticker.Stop()
		defer close(ph.done)

		for {
			select {
			case <-beatCtx.Done():
				return
			case <-ticker.C:
				ph.watchdog.Heartbeat(ph.component)
			}
		}
	})
}

// Stop halts heartbeat sending.
func (ph *PeriodicHeartbeat) Stop() {
	if ph.cancel != nil {
		ph.cancel()
	}
	<-ph.done
}

// Beat sends a single immediate heartbeat.
func (ph *PeriodicHeartbeat) Beat() {
	ph.watchdog.Heartbeat(ph.component)
}
