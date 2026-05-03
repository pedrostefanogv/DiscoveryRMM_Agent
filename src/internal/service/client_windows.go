//go:build windows

package service

import (
	"context"
	"time"

	"github.com/Microsoft/go-winio"
)

func connectServicePipe(ctx context.Context, pipeName string, timeout time.Duration) (serviceConn, error) {
	if timeout > 0 {
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
	}

	return winio.DialPipeContext(ctx, pipeName)
}
