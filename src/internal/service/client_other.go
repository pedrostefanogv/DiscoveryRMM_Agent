//go:build !windows

package service

import (
	"context"
	"fmt"
	"runtime"
	"time"
)

func connectServicePipe(ctx context.Context, pipeName string, timeout time.Duration) (serviceConn, error) {
	_ = ctx
	_ = pipeName
	_ = timeout
	return nil, fmt.Errorf("service pipe unsupported on %s", runtime.GOOS)
}
