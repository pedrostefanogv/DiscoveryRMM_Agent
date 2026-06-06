//go:build !windows

package processutil

import (
	"context"
	"os/exec"
)

func HideWindow(cmd *exec.Cmd) {}

func HideCommand(name string, arg ...string) *exec.Cmd {
	return exec.Command(name, arg...)
}

func HideCommandContext(ctx context.Context, name string, arg ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, arg...)
}
