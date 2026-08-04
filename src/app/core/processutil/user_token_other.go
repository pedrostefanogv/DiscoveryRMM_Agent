//go:build !windows

package processutil

import (
	"context"
	"os/exec"
)

// ApplyUserContext is a no-op outside Windows.
func ApplyUserContext(_ context.Context, _ *exec.Cmd) {}
