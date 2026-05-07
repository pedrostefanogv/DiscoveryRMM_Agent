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
