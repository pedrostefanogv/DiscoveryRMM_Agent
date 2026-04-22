//go:build windows

package ctxutil

import (
	"context"
	"syscall"
)

type runAsTokenKey struct{}

// WithProcessUserToken stores a Windows primary token in ctx so that
// processutil.ApplyUserContext can later set it on cmd.SysProcAttr.Token,
// causing Go's exec package to call CreateProcessAsUser instead of CreateProcess.
//
// The token must be a PRIMARY token (TokenPrimary), typically obtained via
// DuplicateTokenEx from an impersonation token captured at the IPC server.
//
// The caller retains ownership of the token: it must remain valid for the
// lifetime of any process spawned with it, and must be closed afterwards.
func WithProcessUserToken(ctx context.Context, tok syscall.Token) context.Context {
	if tok == 0 {
		return ctx
	}
	return context.WithValue(ctx, runAsTokenKey{}, tok)
}

// ProcessUserToken retrieves the Windows primary token stored by
// WithProcessUserToken. Returns (0, false) if none is present.
func ProcessUserToken(ctx context.Context) (syscall.Token, bool) {
	v := ctx.Value(runAsTokenKey{})
	if v == nil {
		return 0, false
	}
	tok, ok := v.(syscall.Token)
	return tok, ok && tok != 0
}
