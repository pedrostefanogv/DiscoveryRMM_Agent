//go:build windows

package service

import (
	"context"
	"net"
	"time"
)

func connectServicePipe(ctx context.Context, pipeName string, timeout time.Duration) (serviceConn, error) {
	var d net.Dialer
	d.Timeout = timeout
	return d.DialContext(ctx, "pipe", pipeName)
}
