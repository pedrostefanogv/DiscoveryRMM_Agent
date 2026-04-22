//go:build !windows

package service

import (
	"fmt"
	"net"
	"runtime"
)

func createIPCListener(pipeName string) (net.Listener, error) {
	_ = pipeName
	return nil, fmt.Errorf("named pipe IPC unsupported on %s", runtime.GOOS)
}
