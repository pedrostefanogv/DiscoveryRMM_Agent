//go:build !windows

package app

import (
	"net"
	"time"
)

// IPCMessage é o envelope do contrato JSON-lines (stub não-Windows).
type IPCMessage struct {
	Type      string         `json:"type"`
	Payload   map[string]any `json:"payload,omitempty"`
	Timestamp int64          `json:"ts,omitempty"`
}

// StartIPCServer é um stub não-Windows.
func StartIPCServer(onMessage func(net.Conn, IPCMessage)) *IPCServer { return nil }

// IPCServer é um stub não-Windows.
type IPCServer struct{}

func (*IPCServer) Broadcast(IPCMessage)           {}
func (*IPCServer) RespondTo(net.Conn, IPCMessage) {}
func (*IPCServer) ClientCount() int               { return 0 }
func (*IPCServer) Close()                         {}

// IPCClient é um stub não-Windows.
type IPCClient struct{}

func NewIPCClient(onMessage func(IPCMessage), onStateChange func(bool)) *IPCClient {
	return &IPCClient{}
}
func (*IPCClient) RunConnectLoop()       {}
func (*IPCClient) Send(IPCMessage) error { return nil }
func (*IPCClient) Close()                {}

// IsServicePresent é um stub não-Windows.
func IsServicePresent(time.Duration) bool { return false }
