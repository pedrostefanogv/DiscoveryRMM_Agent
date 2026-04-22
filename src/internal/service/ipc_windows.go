//go:build windows

package service

import (
	"net"

	"github.com/Microsoft/go-winio"
)

func createIPCListener(pipeName string) (net.Listener, error) {
	return winio.ListenPipe(pipeName, &winio.PipeConfig{
		SecurityDescriptor: pipeSecurityDescriptor,
	})
}
