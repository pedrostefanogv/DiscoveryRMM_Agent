package watchdog

import (
	"context"
	"testing"
	"time"
)

func TestWatchdog_BasicHealthCheck(t *testing.T) {
	cfg := Config{
		HeartbeatInterval:   1 * time.Second,
		UnhealthyThreshold:  3 * time.Second,
		DegradedThreshold:   2 * time.Second,
		CheckInterval:       500 * time.Millisecond,
		EnableAutoRecovery:  false,
		MaxRecoveryAttempts: 3,
	}

	wd := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wd.Start(ctx)
	defer wd.Stop()

	// Send initial heartbeat
	wd.Heartbeat(ComponentAI)

	// Should be healthy immediately
	check := wd.GetComponentHealth(ComponentAI)
	if check.Status != StatusHealthy {
		t.Errorf("Expected healthy, got %s", check.Status)
	}

	// Wait for degraded threshold
	time.Sleep(2200 * time.Millisecond)

	check = wd.GetComponentHealth(ComponentAI)
	if check.Status != StatusDegraded {
		t.Errorf("Expected degraded after 2s, got %s", check.Status)
	}

	// Wait for unhealthy threshold
	time.Sleep(1000 * time.Millisecond)

	check = wd.GetComponentHealth(ComponentAI)
	if check.Status != StatusUnhealthy {
		t.Errorf("Expected unhealthy after 3s, got %s", check.Status)
	}

	// Send heartbeat to recover
	wd.Heartbeat(ComponentAI)
	time.Sleep(100 * time.Millisecond)

	check = wd.GetComponentHealth(ComponentAI)
	if check.Status != StatusHealthy {
		t.Errorf("Expected healthy after heartbeat, got %s", check.Status)
	}
}

func TestWatchdog_RecoveryAction(t *testing.T) {
	cfg := Config{
		HeartbeatInterval:   1 * time.Second,
		UnhealthyThreshold:  2 * time.Second,
		DegradedThreshold:   1500 * time.Millisecond,
		CheckInterval:       500 * time.Millisecond,
		EnableAutoRecovery:  true,
		MaxRecoveryAttempts: 2,
	}

	wd := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	recoveryCalled := false
	wd.RegisterRecovery(ComponentAI, func(c Component) error {
		recoveryCalled = true
		// Simulate successful recovery
		wd.Heartbeat(ComponentAI)
		return nil
	})

	wd.Start(ctx)
	defer wd.Stop()

	// Send initial heartbeat
	wd.Heartbeat(ComponentAI)

	// Wait for unhealthy
	time.Sleep(2500 * time.Millisecond)

	// Recovery should have been called
	if !recoveryCalled {
		t.Error("Expected recovery action to be called")
	}

	// Should be healthy after recovery
	time.Sleep(500 * time.Millisecond)
	check := wd.GetComponentHealth(ComponentAI)
	if check.Status != StatusHealthy {
		t.Errorf("Expected healthy after recovery, got %s", check.Status)
	}
}

func TestWatchdog_MaxRecoveryAttempts(t *testing.T) {
	cfg := Config{
		HeartbeatInterval:   1 * time.Second,
		UnhealthyThreshold:  1500 * time.Millisecond,
		DegradedThreshold:   1000 * time.Millisecond,
		CheckInterval:       300 * time.Millisecond,
		EnableAutoRecovery:  true,
		MaxRecoveryAttempts: 2,
	}

	wd := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	recoveryAttempts := 0
	wd.RegisterRecovery(ComponentAI, func(c Component) error {
		recoveryAttempts++
		// Don't send heartbeat - simulate failed recovery
		return nil
	})

	wd.Start(ctx)
	defer wd.Stop()

	// Send initial heartbeat
	wd.Heartbeat(ComponentAI)

	// Wait for multiple check cycles
	time.Sleep(5 * time.Second)

	// Should have attempted recovery max 2 times
	if recoveryAttempts > cfg.MaxRecoveryAttempts {
		t.Errorf("Expected max %d recovery attempts, got %d", cfg.MaxRecoveryAttempts, recoveryAttempts)
	}
}

func TestStreamMonitor_DetectsStall(t *testing.T) {
	stallDetected := false
	monitor := NewStreamMonitor(
		"test-stream",
		500*time.Millisecond,
		func() {
			stallDetected = true
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	monitor.Start(ctx)
	defer monitor.Stop()

	// Don't send any activity
	time.Sleep(600 * time.Millisecond)

	if !stallDetected {
		t.Error("Expected stall to be detected after timeout")
	}
}

func TestStreamMonitor_NoStallWithActivity(t *testing.T) {
	stallDetected := false
	monitor := NewStreamMonitor(
		"test-stream",
		1*time.Second,
		func() {
			stallDetected = true
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	monitor.Start(ctx)
	defer monitor.Stop()

	// Send activity every 200ms
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	done := time.After(1500 * time.Millisecond)
	for {
		select {
		case <-ticker.C:
			monitor.Activity()
		case <-done:
			if stallDetected {
				t.Error("Stream should not have stalled with regular activity")
			}
			return
		}
	}
}

func TestPeriodicHeartbeat(t *testing.T) {
	cfg := DefaultConfig()
	wd := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wd.Start(ctx)
	defer wd.Stop()

	beat := NewPeriodicHeartbeat(wd, ComponentTray, 200*time.Millisecond)
	beat.Start(ctx)
	defer beat.Stop()

	time.Sleep(100 * time.Millisecond)
	check := wd.GetComponentHealth(ComponentTray)
	if check.Status != StatusHealthy {
		t.Errorf("Expected healthy immediately, got %s", check.Status)
	}

	// Wait for multiple heartbeats
	time.Sleep(500 * time.Millisecond)

	check = wd.GetComponentHealth(ComponentTray)
	if check.Status != StatusHealthy {
		t.Errorf("Expected healthy with periodic heartbeats, got %s", check.Status)
	}
}

func TestWatchdog_ComponentSpecificThresholds(t *testing.T) {
	cfg := Config{
		HeartbeatInterval:   1 * time.Second,
		UnhealthyThreshold:  3 * time.Second,
		DegradedThreshold:   2 * time.Second,
		CheckInterval:       200 * time.Millisecond,
		EnableAutoRecovery:  false,
		MaxRecoveryAttempts: 3,
		ComponentThresholds: map[Component]Thresholds{
			ComponentInventory: {
				DegradedThreshold:  4 * time.Second,
				UnhealthyThreshold: 6 * time.Second,
			},
		},
	}

	wd := New(cfg)
	wd.Heartbeat(ComponentInventory)
	wd.Heartbeat(ComponentAI)

	time.Sleep(3500 * time.Millisecond)

	inventory := wd.GetComponentHealth(ComponentInventory)
	if inventory.Status != StatusHealthy {
		t.Errorf("Expected inventory healthy with custom threshold, got %s", inventory.Status)
	}

	ai := wd.GetComponentHealth(ComponentAI)
	if ai.Status != StatusUnhealthy {
		t.Errorf("Expected AI unhealthy with default threshold, got %s", ai.Status)
	}
}

func TestWatchdog_UnhealthyNotifyCooldown(t *testing.T) {
	cfg := Config{
		HeartbeatInterval:       1 * time.Second,
		UnhealthyThreshold:      500 * time.Millisecond,
		DegradedThreshold:       300 * time.Millisecond,
		CheckInterval:           100 * time.Millisecond,
		EnableAutoRecovery:      false,
		MaxRecoveryAttempts:     3,
		UnhealthyNotifyCooldown: 600 * time.Millisecond,
		ComponentThresholds:     nil,
	}

	wd := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	notifyCount := 0
	wd.OnUnhealthy(func(check HealthCheck) {
		notifyCount++
	})

	wd.Start(ctx)
	defer wd.Stop()

	wd.Heartbeat(ComponentInventory)

	// Wait long enough to trigger unhealthy multiple times, but within cooldown windows.
	time.Sleep(1700 * time.Millisecond)

	if notifyCount < 2 || notifyCount > 3 {
		t.Errorf("Expected 2-3 notifications with cooldown applied, got %d", notifyCount)
	}
}

func TestSafeGo_RecoversPanic(t *testing.T) {
	// This test just ensures SafeGo doesn't crash the test
	done := make(chan bool)

	SafeGo("panic-test", func() {
		defer func() {
			done <- true
		}()
		panic("intentional panic for testing")
	})

	select {
	case <-done:
		// Success - panic was recovered
	case <-time.After(1 * time.Second):
		t.Error("SafeGo didn't recover from panic within timeout")
	}
}

func TestOperationMonitor_WithTimeout(t *testing.T) {
	cfg := DefaultConfig()
	wd := New(cfg)
	ctx := context.Background()

	wd.Start(ctx)
	defer wd.Stop()

	opMon := NewOperationMonitor("test-op", 500*time.Millisecond, wd, ComponentInventory)

	err := opMon.Run(ctx, func(ctx context.Context) error {
		time.Sleep(1 * time.Second) // Simulate long operation
		return nil
	})

	if err != context.DeadlineExceeded {
		t.Errorf("Expected DeadlineExceeded, got %v", err)
	}
}

func TestOperationMonitor_Success(t *testing.T) {
	cfg := DefaultConfig()
	wd := New(cfg)
	ctx := context.Background()

	wd.Start(ctx)
	defer wd.Stop()

	opMon := NewOperationMonitor("test-op", 1*time.Second, wd, ComponentInventory)

	err := opMon.Run(ctx, func(ctx context.Context) error {
		time.Sleep(100 * time.Millisecond)
		return nil
	})

	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
}
