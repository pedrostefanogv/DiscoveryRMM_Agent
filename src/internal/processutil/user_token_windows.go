//go:build windows

package processutil

import (
	"context"
	"os/exec"
	"syscall"

	"discovery/internal/ctxutil"
)

// ApplyUserContext sets cmd.SysProcAttr.Token when a Windows primary token is
// stored in ctx (placed by ctxutil.WithProcessUserToken). This causes Go's exec
// package to call CreateProcessAsUser instead of CreateProcess, so the child
// process runs under the identity of the requesting user rather than as SYSTEM.
//
// Also calls BuildUserEnvironment to populate cmd.Env with the user's actual
// environment variables (%LOCALAPPDATA%, %APPDATA%, %USERPROFILE%, etc.),
// since child processes spawned via CreateProcessAsUser otherwise inherit
// the service's SYSTEM-scoped environment. If BuildUserEnvironment fails,
// cmd.Env is left unset (process inherits service env — non-fatal fallback).
//
// Must be called AFTER HideWindow(cmd) to preserve the HideWindow flag.
//
// If no token is present in ctx, the function is a no-op (process runs as SYSTEM).
func ApplyUserContext(ctx context.Context, cmd *exec.Cmd) {
	tok, ok := ctxutil.ProcessUserToken(ctx)
	if !ok || tok == 0 {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Token = tok

	if env, err := BuildUserEnvironment(tok); err == nil && len(env) > 0 {
		cmd.Env = env
	}
}
