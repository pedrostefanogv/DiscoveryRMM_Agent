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

	previousCollector := collectWindowsDiskReadWriteUtilizationFunc
	collectWindowsDiskReadWriteUtilizationFunc = func(context.Context) (float64, float64, bool) {
		return 41.2, 18.7, true
	}
	defer func() {
		collectWindowsDiskReadWriteUtilizationFunc = previousCollector
	}()

	metrics := &agentconn.AgentHeartbeatMetrics{DiskReadPercent: -1, DiskWritePercent: -1}
	applyHeartbeatDiskIOFallback(context.Background(), metrics)
	if metrics.DiskReadPercent != 41.2 {
		t.Fatalf("DiskReadPercent = %v, want 41.2", metrics.DiskReadPercent)
	}
	if metrics.DiskWritePercent != 18.7 {
		t.Fatalf("DiskWritePercent = %v, want 18.7", metrics.DiskWritePercent)
	}
	if metrics.DiskPercent != 30.0 {
		t.Fatalf("DiskPercent = %v, want 30.0", metrics.DiskPercent)
	}
}

func TestApplyHeartbeatDiskIOFallback_PreservesExistingValues(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("fallback de disco testado apenas no Windows")
	}

	called := false
	previousCollector := collectWindowsDiskReadWriteUtilizationFunc
	collectWindowsDiskReadWriteUtilizationFunc = func(context.Context) (float64, float64, bool) {
		called = true
		return 91.0, 77.0, true
	}
	defer func() {
		collectWindowsDiskReadWriteUtilizationFunc = previousCollector
	}()

	metrics := &agentconn.AgentHeartbeatMetrics{DiskReadPercent: 11.1, DiskWritePercent: 22.2}
	applyHeartbeatDiskIOFallback(context.Background(), metrics)
	if called {
		t.Fatalf("collector nao deveria ser chamado quando DiskReadPercent/DiskWritePercent ja sao validos")
	}
	if metrics.DiskReadPercent != 11.1 {
		t.Fatalf("DiskReadPercent alterado indevidamente: got %v want 11.1", metrics.DiskReadPercent)
	}
	if metrics.DiskWritePercent != 22.2 {
		t.Fatalf("DiskWritePercent alterado indevidamente: got %v want 22.2", metrics.DiskWritePercent)
	}
	if metrics.DiskPercent != 16.7 {
		t.Fatalf("DiskPercent = %v, want 16.7", metrics.DiskPercent)
	}
}

func TestApplyHeartbeatDiskIOFallback_CollectorFillsOnlyMissingSide(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("fallback de disco testado apenas no Windows")
	}

	previousCollector := collectWindowsDiskReadWriteUtilizationFunc
	collectWindowsDiskReadWriteUtilizationFunc = func(context.Context) (float64, float64, bool) {
		return 90.0, 30.0, true
	}
	defer func() {
		collectWindowsDiskReadWriteUtilizationFunc = previousCollector
	}()

	metrics := &agentconn.AgentHeartbeatMetrics{DiskReadPercent: 40.0, DiskWritePercent: -1}
	applyHeartbeatDiskIOFallback(context.Background(), metrics)
	if metrics.DiskReadPercent != 40.0 {
		t.Fatalf("DiskReadPercent alterado indevidamente: got %v want 40.0", metrics.DiskReadPercent)
	}
	if metrics.DiskWritePercent != 30.0 {
		t.Fatalf("DiskWritePercent = %v, want 30.0", metrics.DiskWritePercent)
	}
	if metrics.DiskPercent != 35.0 {
		t.Fatalf("DiskPercent = %v, want 35.0", metrics.DiskPercent)
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
