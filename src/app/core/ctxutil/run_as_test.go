//go:build windows

package ctxutil

import (
	"context"
	"syscall"
	"testing"
)

func TestWithProcessUserToken_StoresToken(t *testing.T) {
	const fakeToken syscall.Token = 12345
	ctx := WithProcessUserToken(context.Background(), fakeToken)
	got, ok := ProcessUserToken(ctx)
	if !ok {
		t.Fatal("ProcessUserToken: expected ok=true")
	}
	if got != fakeToken {
		t.Fatalf("ProcessUserToken: got %d, want %d", got, fakeToken)
	}
}

func TestWithProcessUserToken_ZeroToken_ReturnsUnchanged(t *testing.T) {
	const zero syscall.Token = 0
	parent := context.Background()
	ctx := WithProcessUserToken(parent, zero)
	if ctx != parent {
		t.Fatal("expected ctx unchanged when token is zero")
	}
}

func TestProcessUserToken_NoToken_ReturnsFalse(t *testing.T) {
	tok, ok := ProcessUserToken(context.Background())
	if ok {
		t.Fatalf("expected ok=false for empty context, got token=%d", tok)
	}
	if tok != 0 {
		t.Fatalf("expected token=0 for empty context, got %d", tok)
	}
}

func TestProcessUserToken_InheritedByChildContext(t *testing.T) {
	const fakeToken syscall.Token = 99999
	parent := WithProcessUserToken(context.Background(), fakeToken)
	child, cancel := context.WithCancel(parent)
	defer cancel()
	got, ok := ProcessUserToken(child)
	if !ok {
		t.Fatal("expected child context to inherit token")
	}
	if got != fakeToken {
		t.Fatalf("inherited token: got %d, want %d", got, fakeToken)
	}
}
