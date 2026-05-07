package inventory

import (
	"context"
	"runtime"
	"testing"
	"time"

	"discovery/internal/agentconn"
)

func TestApplyHeartbeatCPUFallback_UsesCollectorWhenMissing(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("fallback de CPU testado apenas no Windows")
	}

	previousCollector := collectWindowsCPUPercentFunc
	collectWindowsCPUPercentFunc = func(context.Context) (float64, bool) {
		return 37.5, true
	}
	defer func() {
		collectWindowsCPUPercentFunc = previousCollector
	}()

	metrics := &agentconn.AgentHeartbeatMetrics{CpuPercent: -1}
	applyHeartbeatCPUFallback(context.Background(), metrics)
	if metrics.CpuPercent != 37.5 {
		t.Fatalf("CpuPercent = %v, want 37.5", metrics.CpuPercent)
	}
}

func TestApplyHeartbeatCPUFallback_PreservesExistingCPU(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("fallback de CPU testado apenas no Windows")
	}

	called := false
	previousCollector := collectWindowsCPUPercentFunc
	collectWindowsCPUPercentFunc = func(context.Context) (float64, bool) {
		called = true
		return 88.8, true
	}
	defer func() {
		collectWindowsCPUPercentFunc = previousCollector
	}()

	metrics := &agentconn.AgentHeartbeatMetrics{CpuPercent: 12.3}
	applyHeartbeatCPUFallback(context.Background(), metrics)
	if called {
		t.Fatalf("collector nao deveria ser chamado quando CpuPercent ja e valido")
	}
	if metrics.CpuPercent != 12.3 {
		t.Fatalf("CpuPercent alterado indevidamente: got %v want 12.3", metrics.CpuPercent)
	}
}

func TestApplyHeartbeatDiskIOFallback_UsesCollectorWhenMissing(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("fallback de disco testado apenas no Windows")
	}

	previousCollector := collectWindowsDiskIOMetricsFunc
	collectWindowsDiskIOMetricsFunc = func(context.Context) (float64, float64, float64, float64, bool) {
		return 24.0, 24.0, 0.0, 7.345, true
	}
	defer func() {
		collectWindowsDiskIOMetricsFunc = previousCollector
	}()

	metrics := &agentconn.AgentHeartbeatMetrics{DiskPercent: -1, DiskReadPercent: -1, DiskWritePercent: -1, DiskResponseMs: -1}
	applyHeartbeatDiskIOFallback(context.Background(), metrics)
	if metrics.DiskReadPercent != 24.0 {
		t.Fatalf("DiskReadPercent = %v, want 24.0", metrics.DiskReadPercent)
	}
	if metrics.DiskWritePercent != 0.0 {
		t.Fatalf("DiskWritePercent = %v, want 0.0", metrics.DiskWritePercent)
	}
	if metrics.DiskPercent != 24.0 {
		t.Fatalf("DiskPercent = %v, want 24.0", metrics.DiskPercent)
	}
	if metrics.DiskResponseMs != 7.345 {
		t.Fatalf("DiskResponseMs = %v, want 7.345", metrics.DiskResponseMs)
	}
}

func TestApplyHeartbeatDiskIOFallback_PreservesExistingValues(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("fallback de disco testado apenas no Windows")
	}

	called := false
	previousCollector := collectWindowsDiskIOMetricsFunc
	collectWindowsDiskIOMetricsFunc = func(context.Context) (float64, float64, float64, float64, bool) {
		called = true
		return 91.0, 77.0, 66.0, 9.999, true
	}
	defer func() {
		collectWindowsDiskIOMetricsFunc = previousCollector
	}()

	metrics := &agentconn.AgentHeartbeatMetrics{DiskPercent: 13.3, DiskReadPercent: 11.1, DiskWritePercent: 22.2, DiskResponseMs: 4.2}
	applyHeartbeatDiskIOFallback(context.Background(), metrics)
	if called {
		t.Fatalf("collector nao deveria ser chamado quando DiskPercent/DiskReadPercent/DiskWritePercent/DiskResponseMs ja sao validos")
	}
	if metrics.DiskPercent != 13.3 {
		t.Fatalf("DiskPercent alterado indevidamente: got %v want 13.3", metrics.DiskPercent)
	}
	if metrics.DiskReadPercent != 11.1 {
		t.Fatalf("DiskReadPercent alterado indevidamente: got %v want 11.1", metrics.DiskReadPercent)
	}
	if metrics.DiskWritePercent != 22.2 {
		t.Fatalf("DiskWritePercent alterado indevidamente: got %v want 22.2", metrics.DiskWritePercent)
	}
	if metrics.DiskResponseMs != 4.2 {
		t.Fatalf("DiskResponseMs alterado indevidamente: got %v want 4.2", metrics.DiskResponseMs)
	}
}

func TestApplyHeartbeatDiskIOFallback_CollectorFillsOnlyMissingSide(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("fallback de disco testado apenas no Windows")
	}

	previousCollector := collectWindowsDiskIOMetricsFunc
	collectWindowsDiskIOMetricsFunc = func(context.Context) (float64, float64, float64, float64, bool) {
		return 24.0, 90.0, 30.0, 12.111, true
	}
	defer func() {
		collectWindowsDiskIOMetricsFunc = previousCollector
	}()

	metrics := &agentconn.AgentHeartbeatMetrics{DiskPercent: -1, DiskReadPercent: 40.0, DiskWritePercent: -1, DiskResponseMs: -1}
	applyHeartbeatDiskIOFallback(context.Background(), metrics)
	if metrics.DiskPercent != 24.0 {
		t.Fatalf("DiskPercent = %v, want 24.0", metrics.DiskPercent)
	}
	if metrics.DiskReadPercent != 40.0 {
		t.Fatalf("DiskReadPercent alterado indevidamente: got %v want 40.0", metrics.DiskReadPercent)
	}
	if metrics.DiskWritePercent != 30.0 {
		t.Fatalf("DiskWritePercent = %v, want 30.0", metrics.DiskWritePercent)
	}
	if metrics.DiskResponseMs != 12.111 {
		t.Fatalf("DiskResponseMs = %v, want 12.111", metrics.DiskResponseMs)
	}
}

func TestApplyHeartbeatDiskIOFallback_NoDerivationWhenCollectorFails(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("fallback de disco testado apenas no Windows")
	}

	previousCollector := collectWindowsDiskIOMetricsFunc
	collectWindowsDiskIOMetricsFunc = func(context.Context) (float64, float64, float64, float64, bool) {
		return -1, -1, -1, -1, false
	}
	defer func() {
		collectWindowsDiskIOMetricsFunc = previousCollector
	}()

	metrics := &agentconn.AgentHeartbeatMetrics{
		DiskPercent:      -1,
		DiskReadPercent:  24.0,
		DiskWritePercent: 0.0,
		DiskResponseMs:   -1,
	}
	applyHeartbeatDiskIOFallback(context.Background(), metrics)
	if metrics.DiskPercent != -1 {
		t.Fatalf("DiskPercent deveria permanecer omitido sem percent_disk_time: got %v want -1", metrics.DiskPercent)
	}
	if metrics.DiskReadPercent != 24.0 {
		t.Fatalf("DiskReadPercent alterado indevidamente: got %v want 24.0", metrics.DiskReadPercent)
	}
	if metrics.DiskWritePercent != 0.0 {
		t.Fatalf("DiskWritePercent alterado indevidamente: got %v want 0.0", metrics.DiskWritePercent)
	}
	if metrics.DiskResponseMs != -1 {
		t.Fatalf("DiskResponseMs alterado indevidamente: got %v want -1", metrics.DiskResponseMs)
	}
}

func TestCollectWindowsCPUPercent_Local(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("coleta local de CPU suportada apenas no Windows")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cpuPercent, ok := CollectWindowsCPUPercent(ctx)
	if !ok {
		t.Fatalf("coleta local de CPU falhou via PowerShell/CIM")
	}
	if cpuPercent < 0 || cpuPercent > 100 {
		t.Fatalf("CpuPercent fora da faixa: %v", cpuPercent)
	}
}
