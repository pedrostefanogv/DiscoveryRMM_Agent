//go:build windows

package remotesession

import (
	"context"
	"slices"
	"testing"
	"time"
)

func uint32Ptr(v uint32) *uint32 { return &v }

func TestEvaluateRDPStatus_AllHealthy(t *testing.T) {
	st := evaluateRDPStatus("running", "auto", uint32Ptr(0), 3389, true, time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC))
	if !st.Ready || !st.RDPEnabled || !st.Listening || st.Port != 3389 || len(st.Issues) != 0 {
		t.Fatalf("esperava status healthy, got %+v", st)
	}
}

func TestEvaluateRDPStatus_TermServiceStoppedAndDisabled(t *testing.T) {
	st := evaluateRDPStatus("stopped", "disabled", uint32Ptr(0), 3389, false, time.Now())
	if st.Ready {
		t.Fatal("esperava Ready=false com TermService parado")
	}
	if !slices.Contains(st.Issues, IssueTermServiceNotRunning) || !slices.Contains(st.Issues, IssueTermServiceDisabled) {
		t.Fatalf("issues esperadas ausentes: %+v", st.Issues)
	}
	if len(st.Hints) == 0 {
		t.Fatal("esperava hints acionaveis")
	}
}

func TestEvaluateRDPStatus_RegistryDenied(t *testing.T) {
	st := evaluateRDPStatus("running", "auto", uint32Ptr(1), 3389, true, time.Now())
	if st.Ready || st.RDPEnabled {
		t.Fatalf("esperava Ready=false e RDPEnabled=false, got %+v", st)
	}
	if !slices.Contains(st.Issues, IssueRDPDisabledRegistry) {
		t.Fatalf("esperava issue %s, got %+v", IssueRDPDisabledRegistry, st.Issues)
	}
}

func TestEvaluateRDPStatus_CustomPortNotListening(t *testing.T) {
	st := evaluateRDPStatus("running", "demand", uint32Ptr(0), 3390, false, time.Now())
	if st.Ready || st.Port != 3390 || !slices.Contains(st.Issues, IssueRDPPortNotListening) {
		t.Fatalf("esperava Port=3390 e %s, got %+v", IssueRDPPortNotListening, st)
	}
}

func TestEvaluateRDPStatus_InvalidPortDefaultsTo3389(t *testing.T) {
	for _, p := range []int{0, -1, 70000} {
		st := evaluateRDPStatus("running", "auto", uint32Ptr(0), p, true, time.Now())
		if st.Port != 3389 {
			t.Fatalf("porta %d deveria normalizar para 3389, got %d", p, st.Port)
		}
	}
}

func TestCheckRDPPreflightWithDeps_MockedWindowsProbes(t *testing.T) {
	st := checkRDPPreflightWithDeps(context.Background(), rdpProbeDeps{
		queryTermService: func() (string, string) { return "running", "manual" },
		readRDPRegistry:  func() (*uint32, int) { return uint32Ptr(0), 3391 },
		probeTCP:         func(_ context.Context, port int) bool { return port == 3391 },
		now:              func() time.Time { return time.Date(2026, 11, 18, 1, 2, 3, 0, time.UTC) },
	})
	if !st.Ready || st.Port != 3391 || !st.Listening {
		t.Fatalf("preflight mockado inesperado: %+v", st)
	}
}

func TestCheckRDPPreflight_LiveReadOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	st := CheckRDPPreflight(ctx)
	if st.Port < 1 || st.Port > 65535 || st.CheckedAtUtc == "" || st.TermServiceState == "" {
		t.Fatalf("preflight live invalido: %+v", st)
	}
}
