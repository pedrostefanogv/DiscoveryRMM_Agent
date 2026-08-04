package ctxutil

import (
	"context"
	"testing"
	"time"
)

func TestWithTimeout_NilParent(t *testing.T) {
	ctx, cancel := WithTimeout(context.TODO(), 1*time.Second)
	defer cancel()
	if ctx == nil {
		t.Fatal("ctx should not be nil")
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected deadline to be set")
	}
	if deadline.Before(time.Now()) {
		t.Error("deadline should be in the future")
	}
}

func TestWithTimeout_ZeroTimeout(t *testing.T) {
	parent := context.Background()
	ctx, cancel := WithTimeout(parent, 0)
	defer cancel()
	if ctx != parent {
		t.Error("expected parent context returned as-is")
	}
}

func TestWithTimeout_NegativeTimeout(t *testing.T) {
	parent := context.Background()
	ctx, cancel := WithTimeout(parent, -5*time.Second)
	defer cancel()
	if ctx != parent {
		t.Error("expected parent context returned as-is for negative timeout")
	}
}

func TestWithTimeout_PositiveTimeout(t *testing.T) {
	parent := context.Background()
	ctx, cancel := WithTimeout(parent, 100*time.Millisecond)
	defer cancel()

	select {
	case <-ctx.Done():
		t.Fatal("context should not be done yet")
	default:
	}

	<-time.After(150 * time.Millisecond)

	select {
	case <-ctx.Done():
		// expected
	default:
		t.Fatal("context should be done after timeout")
	}
}

func TestWithTimeout_RespectsParentDeadline(t *testing.T) {
	// Parent with 50ms deadline, child requests 5s — should still expire at 50ms
	parent, parentCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer parentCancel()

	ctx, cancel := WithTimeout(parent, 5*time.Second)
	defer cancel()

	select {
	case <-ctx.Done():
		t.Fatal("context should not be done immediately")
	default:
	}

	<-time.After(100 * time.Millisecond)

	select {
	case <-ctx.Done():
		// expected — parent's tighter deadline wins
	default:
		t.Fatal("context should be done when parent deadline expires")
	}
}
