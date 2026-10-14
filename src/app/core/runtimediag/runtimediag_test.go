package runtimediag

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTakeFillsReasonableFields(t *testing.T) {
	s := Take()
	if s.SysMiB <= 0 {
		t.Fatalf("SysMiB = %v, want > 0", s.SysMiB)
	}
	if s.Goroutines < 1 {
		t.Fatalf("Goroutines = %d, want >= 1", s.Goroutines)
	}
	if s.HeapSysMiB < s.HeapAllocMiB {
		t.Fatalf("HeapSys (%v) < HeapAlloc (%v)", s.HeapSysMiB, s.HeapAllocMiB)
	}
}

func TestStringContainsCoreMetrics(t *testing.T) {
	line := Take().String()
	for _, want := range []string{"heap=", "goroutines=", "memlimit=", "gogc="} {
		if !strings.Contains(line, want) {
			t.Fatalf("String() = %q, faltando %q", line, want)
		}
	}
}

func TestLogEmitsFirstLineThenStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	lines := make(chan string, 8)
	go Log(ctx, 10*time.Millisecond, func(s string) { lines <- s })

	select {
	case got := <-lines:
		if !strings.HasPrefix(got, "[mem] ") {
			t.Fatalf("primeira linha = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Log não emitiu a primeira linha")
	}
	cancel()
}

func TestLogIgnoresInvalidArgs(t *testing.T) {
	Log(context.Background(), 0, func(string) { t.Fatal("não deveria emitir") })
	Log(context.Background(), time.Second, nil)
}
