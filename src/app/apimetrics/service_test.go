package apimetrics

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestServiceEmptyState(t *testing.T) {
	svc := New()
	if snap := svc.GetSnapshot(); len(snap) != 0 {
		t.Fatalf("expected empty snapshot, got %d entries", len(snap))
	}
	if rate := svc.GetErrorRate("/missing"); rate != 0 {
		t.Fatalf("expected 0 error rate for missing endpoint, got %v", rate)
	}
	calls, errs, avg := svc.GetOverallStats()
	if calls != 0 || errs != 0 || avg != 0 {
		t.Fatalf("expected (0, 0, 0), got (%d, %d, %v)", calls, errs, avg)
	}
}

func TestRecordCallAndRecordErrorLifecycle(t *testing.T) {
	svc := New()

	svc.RecordCall("/hb", 10*time.Millisecond)
	svc.RecordError("/hb", 30*time.Millisecond, "timeout")
	svc.RecordCall("/hb", 20*time.Millisecond)

	snap := svc.GetSnapshot()
	m, ok := snap["/hb"]
	if !ok {
		t.Fatal("expected /hb metric in snapshot")
	}
	if m.Endpoint != "/hb" {
		t.Errorf("Endpoint = %q, want /hb", m.Endpoint)
	}
	if m.TotalCalls != 3 || m.SuccessCount != 2 || m.ErrorCount != 1 {
		t.Errorf("counts = (total=%d, ok=%d, err=%d), want (3, 2, 1)", m.TotalCalls, m.SuccessCount, m.ErrorCount)
	}
	if m.LastLatency != 20*time.Millisecond {
		t.Errorf("LastLatency = %v, want 20ms", m.LastLatency)
	}
	if m.TotalLatency != 60*time.Millisecond {
		t.Errorf("TotalLatency = %v, want 60ms", m.TotalLatency)
	}
	if m.LastError != "" {
		t.Errorf("LastError = %q, expected cleared after subsequent RecordCall", m.LastError)
	}
	if m.LastCall.IsZero() {
		t.Error("LastCall should be non-zero")
	}

	// Verify RecordError sets LastError.
	svc.RecordError("/hb", 40*time.Millisecond, "502 bad gateway")
	snap2 := svc.GetSnapshot()
	if snap2["/hb"].LastError != "502 bad gateway" {
		t.Errorf("LastError = %q, want %q", snap2["/hb"].LastError, "502 bad gateway")
	}

	// Mutating returned snapshot copy must not mutate service state.
	copied := snap2["/hb"]
	copied.TotalCalls = 9999
	snap2["/hb"] = copied
	if svc.GetSnapshot()["/hb"].TotalCalls != 4 {
		t.Fatalf("snapshot mutation leaked into internal state")
	}

	// Error rate: 2 errors out of 4 calls = 0.5.
	if rate := svc.GetErrorRate("/hb"); rate != 0.5 {
		t.Errorf("GetErrorRate = %v, want 0.5", rate)
	}
}

func TestOverallStatsAndReset(t *testing.T) {
	svc := New()
	svc.RecordCall("/a", 10*time.Millisecond)
	svc.RecordCall("/a", 20*time.Millisecond)
	svc.RecordError("/b", 30*time.Millisecond, "boom")
	svc.RecordError("/b", 60*time.Millisecond, "boom2")

	totalCalls, totalErrors, avgLatency := svc.GetOverallStats()
	if totalCalls != 4 || totalErrors != 2 {
		t.Fatalf("overall = (%d, %d), want (4, 2)", totalCalls, totalErrors)
	}
	// (10 + 20 + 30 + 60) / 4 = 30ms
	if avgLatency != 30*time.Millisecond {
		t.Fatalf("avgLatency = %v, want 30ms", avgLatency)
	}

	svc.Reset()
	if snap := svc.GetSnapshot(); len(snap) != 0 {
		t.Fatalf("expected empty snapshot after Reset, got %d", len(snap))
	}
	calls, errs, avg := svc.GetOverallStats()
	if calls != 0 || errs != 0 || avg != 0 {
		t.Fatalf("expected (0, 0, 0) after Reset, got (%d, %d, %v)", calls, errs, avg)
	}
}

func TestConcurrentAccess(t *testing.T) {
	svc := New()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ep := fmt.Sprintf("/ep/%d", id%3)
			for i := 0; i < 50; i++ {
				if i%4 == 0 {
					svc.RecordError(ep, time.Millisecond, "err")
				} else {
					svc.RecordCall(ep, 2*time.Millisecond)
				}
				_ = svc.GetErrorRate(ep)
				_ = svc.GetSnapshot()
				_, _, _ = svc.GetOverallStats()
			}
		}(g)
	}
	wg.Wait()

	totalCalls, totalErrors, _ := svc.GetOverallStats()
	if totalCalls != 400 {
		t.Fatalf("totalCalls = %d, want 400", totalCalls)
	}
	// i%4==0 happens 13 times per 50 iterations (0,4,...,48) * 8 goroutines = 104
	if totalErrors != 104 {
		t.Fatalf("totalErrors = %d, want 104", totalErrors)
	}
}
