package safego

import (
	"bytes"
	"log"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGoNormalExecutionDoesNotLog(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)

	ran := false
	logged := false
	Go(func() {
		defer wg.Done()
		ran = true
	}, func(string) {
		logged = true
	})

	wg.Wait()
	if !ran {
		t.Fatal("expected goroutine function to execute")
	}
	if logged {
		t.Fatal("did not expect logf to be called when no panic occurs")
	}
}

func TestGoRecoversPanicWithLogFunc(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	logged := make(chan struct{})
	var once sync.Once

	Go(func() {
		panic("unexpected nil widget")
	}, func(line string) {
		mu.Lock()
		lines = append(lines, line)
		mu.Unlock()
		if strings.Contains(line, "TestGoRecoversPanicWithLogFunc") {
			once.Do(func() { close(logged) })
		}
	})

	select {
	case <-logged:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for panic stack trace log")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(lines) < 3 {
		t.Fatalf("expected header + stack lines, got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "[PANIC] goroutine panicou: unexpected nil widget") {
		t.Fatalf("unexpected panic header line: %q", lines[0])
	}
	if lines[1] != "[PANIC] stack trace:" {
		t.Fatalf("unexpected stack label line: %q", lines[1])
	}
	joined := strings.Join(lines[2:], "\n")
	if !strings.Contains(joined, "TestGoRecoversPanicWithLogFunc") {
		t.Fatalf("expected stack trace to mention test function, got:\n%s", joined)
	}
}

type syncLogCapture struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	written chan struct{}
	once    sync.Once
}

func (c *syncLogCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	n, err := c.buf.Write(p)
	c.mu.Unlock()
	c.once.Do(func() { close(c.written) })
	return n, err
}

func (c *syncLogCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

func TestGoRecoversPanicFallbackStdLog(t *testing.T) {
	capture := &syncLogCapture{written: make(chan struct{})}
	origOut := log.Writer()
	log.SetOutput(capture)
	defer log.SetOutput(origOut)

	Go(func() {
		panic("fallback-boom")
	}, nil)

	<-capture.written

	got := capture.String()
	if !strings.Contains(got, "[PANIC] goroutine panicou: fallback-boom") {
		t.Fatalf("expected stdlib log fallback to contain panic message, got %q", got)
	}
}

func TestSplitLines(t *testing.T) {
	cases := []struct {
		raw  string
		want []string
	}{
		{"", []string{}},
		{"single", []string{"single"}},
		{"a\nb\n", []string{"a", "b"}},
		{"a\nb\nc", []string{"a", "b", "c"}},
		{"a\n\nb", []string{"a", "", "b"}},
	}
	for _, tc := range cases {
		got := splitLines(tc.raw)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("splitLines(%q) = %#v, want %#v", tc.raw, got, tc.want)
		}
	}
}
