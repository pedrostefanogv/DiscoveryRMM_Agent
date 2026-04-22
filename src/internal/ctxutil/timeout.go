// Package ctxutil provides shared context helpers used across packages.
package ctxutil

import (
	"context"
	"time"
)

// WithTimeout returns a child context with the given timeout applied.
// If the parent is nil, context.Background is used.
// If timeout <= 0, the parent context is returned unchanged with a no-op
// cancel function (callers should be aware that cancellation will only
// happen if the parent itself is cancelled).
// If the parent already has an earlier deadline, the stricter deadline wins.
func WithTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if timeout <= 0 {
		return parent, func() {}
	}
	// context.WithTimeout already picks the earlier of its own deadline
	// and any existing parent deadline, so this is safe.
	return context.WithTimeout(parent, timeout)
}
