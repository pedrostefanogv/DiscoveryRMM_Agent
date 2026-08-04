//go:build !windows

package ctxutil

import "context"

// WithProcessUserToken is a no-op outside Windows; returns ctx unchanged.
func WithProcessUserToken(ctx context.Context, _ uintptr) context.Context {
	return ctx
}

// ProcessUserToken always returns (0, false) outside Windows.
func ProcessUserToken(_ context.Context) (uintptr, bool) {
	return 0, false
}
