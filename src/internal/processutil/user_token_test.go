//go:build windows

package processutil

import (
	"context"
	"os/exec"
	"syscall"
	"testing"

	"discovery/internal/ctxutil"
)

func TestApplyUserContext_NoToken_DoesNotSetToken(t *testing.T) {
	cmd := &exec.Cmd{}
	ApplyUserContext(context.Background(), cmd)
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.Token != 0 {
		t.Fatal("expected Token to remain zero when no user token in ctx")
	}
}

func TestApplyUserContext_NoToken_PreservesExistingAttr(t *testing.T) {
	cmd := &exec.Cmd{
		SysProcAttr: &syscall.SysProcAttr{HideWindow: true},
	}
	ApplyUserContext(context.Background(), cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("SysProcAttr should not be nil")
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Fatal("HideWindow flag should be preserved")
	}
	if cmd.SysProcAttr.Token != 0 {
		t.Fatal("Token should remain zero")
	}
}

func TestApplyUserContext_WithToken_SetsTokenPreservesHideWindow(t *testing.T) {
	const fakeToken syscall.Token = 777
	ctx := ctxutil.WithProcessUserToken(context.Background(), fakeToken)

	cmd := &exec.Cmd{
		SysProcAttr: &syscall.SysProcAttr{HideWindow: true},
	}
	ApplyUserContext(ctx, cmd)

	if cmd.SysProcAttr == nil {
		t.Fatal("SysProcAttr should not be nil")
	}
	if cmd.SysProcAttr.Token != fakeToken {
		t.Fatalf("Token: got %d, want %d", cmd.SysProcAttr.Token, fakeToken)
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Fatal("HideWindow flag should be preserved after ApplyUserContext")
	}
}

func TestApplyUserContext_WithToken_InitializesSysProcAttr(t *testing.T) {
	const fakeToken syscall.Token = 888
	ctx := ctxutil.WithProcessUserToken(context.Background(), fakeToken)

	cmd := &exec.Cmd{} // SysProcAttr is nil
	ApplyUserContext(ctx, cmd)

	if cmd.SysProcAttr == nil {
		t.Fatal("ApplyUserContext should initialise SysProcAttr when nil")
	}
	if cmd.SysProcAttr.Token != fakeToken {
		t.Fatalf("Token: got %d, want %d", cmd.SysProcAttr.Token, fakeToken)
	}
}
