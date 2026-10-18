//go:build !windows

package app

// ServiceHandshake é o stub não-Windows (não há serviço IPC).
func (*IPCClient) ServiceHandshake() (string, int, bool) { return "", 0, false }
