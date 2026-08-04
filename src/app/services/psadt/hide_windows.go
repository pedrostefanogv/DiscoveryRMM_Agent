//go:build windows

package psadt

import (
	"os/exec"

	"discovery/app/core/processutil"
)

func init() {
	hideWindow = func(cmd *exec.Cmd) {
		processutil.HideWindow(cmd)
	}
}
