//go:build windows

package processutil

import (
	"context"
	"os/exec"
	"syscall"
)

// HideWindow prevents console flash when executing child processes from GUI apps.
func HideWindow(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	// CREATE_NO_WINDOW avoids transient console windows for console executables.
	cmd.SysProcAttr.CreationFlags |= 0x08000000
}

// HideCommand creates an exec.Cmd with HideWindow already applied.
// Prefer this over exec.Command for all Windows child processes.
func HideCommand(name string, arg ...string) *exec.Cmd {
	cmd := exec.Command(name, arg...)
	HideWindow(cmd)
	return cmd
}

// HideCommandContext creates an exec.Cmd with context and HideWindow already applied.
// Prefer this over exec.CommandContext for all Windows child processes.
func HideCommandContext(ctx context.Context, name string, arg ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, arg...)
	HideWindow(cmd)
	return cmd
}
