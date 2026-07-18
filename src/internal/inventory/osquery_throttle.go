package inventory

import (
	"context"
	"runtime"
	"sync"
	"time"
)

const (
	// defaultMaxCPUPercent is the CPU usage threshold above which queries
	// are throttled during startup (when StartupThrottleEnabled is nil/true).
	defaultMaxCPUPercent = 50.0

	// throttleDelayBetweenHeavyQueries is the base delay inserted between
	// heavy osquery queries when CPU throttling is active.
	throttleDelayBetweenHeavyQueries = 200 * time.Millisecond

	// throttleDelayBetweenLightQueries is the delay for light queries.
	throttleDelayBetweenLightQueries = 50 * time.Millisecond

	// throttleAutoDetectCoreLimit is the max number of CPU cores for which
	// auto-detection enables throttling (≤6 cores = throttled by default).
	throttleAutoDetectCoreLimit = 6

	// throttleWindowAfterStartup is the duration after process start where
	// throttling remains active regardless of CPU load.
	throttleWindowAfterStartup = 120 * time.Second
)

// ThrottleConfig holds the runtime throttle state.
type ThrottleConfig struct {
	mu      sync.RWMutex
	enabled *bool   // nil = auto-detect, true = forced on, false = forced off
	maxCPU  float64 // CPU percent threshold (0-100)
	startup time.Time
}

var globalThrottle = ThrottleConfig{
	maxCPU:  defaultMaxCPUPercent,
	startup: time.Now(),
}

// heavyOsqueryQueries lists query names that are known to be CPU-intensive.
var heavyOsqueryQueries = map[string]bool{
	"programs":             true,
	"npm_packages":         true,
	"python_packages":      true,
	"process_open_sockets": true, // alias: open_sockets
	"open_sockets":         true,
	"listening_ports":      true,
	"cpu_info":             true,
	"cpuid":                true,
	"memory_devices":       true,
	"startup_items":        true,
	"interface_details":    true,
	"disk_info":            true,
	"logical_drives":       true,
}

// SetThrottleConfig updates the throttle policy from agent configuration.
// Call with nil values to keep current settings.
func SetThrottleConfig(enabled *bool, maxCPUPercent *int) {
	globalThrottle.mu.Lock()
	defer globalThrottle.mu.Unlock()

	if enabled != nil {
		globalThrottle.enabled = enabled
	}
	if maxCPUPercent != nil && *maxCPUPercent > 0 && *maxCPUPercent <= 100 {
		globalThrottle.maxCPU = float64(*maxCPUPercent)
	}
}

// shouldThrottleQueries returns true when query execution should be paced.
func shouldThrottleQueries() bool {
	globalThrottle.mu.RLock()
	enabled := globalThrottle.enabled
	maxCPU := globalThrottle.maxCPU
	startupTime := globalThrottle.startup
	globalThrottle.mu.RUnlock()

	// Explicitly disabled — no throttle.
	if enabled != nil && !*enabled {
		return false
	}

	// During the startup window, always throttle on modest hardware.
	if time.Since(startupTime) < throttleWindowAfterStartup {
		// Auto-detect: throttle if enabled==nil or enabled==true AND cores <= 4.
		if enabled == nil {
			return runtime.NumCPU() <= throttleAutoDetectCoreLimit
		}
		return *enabled
	}

	// After startup window, only throttle if explicitly enabled AND configured
	// maxCPU is set to a value below 100.
	if enabled != nil && *enabled && maxCPU < 100 {
		return true
	}

	return false
}

// throttleDelayForQuery returns the delay to insert before executing a query.
// Heavy queries get longer delays; light queries get minimal delays.
func throttleDelayForQuery(queryName string) time.Duration {
	if !shouldThrottleQueries() {
		return 0
	}
	if heavyOsqueryQueries[queryName] {
		return throttleDelayBetweenHeavyQueries
	}
	return throttleDelayBetweenLightQueries
}

// sleepWithContext sleeps for d or until ctx is cancelled, whichever comes first.
func sleepWithContext(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
