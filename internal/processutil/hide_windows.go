//go:build windows

package processutil

import (
	"os/exec"
	"syscall"
)

// HideWindow prevents console flash when executing child processes from GUI apps.
func HideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
