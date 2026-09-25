//go:build windows

package app

import (
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
)

func TestResolveServiceControl(t *testing.T) {
	cases := []struct {
		name  string
		cmd   svc.Cmd
		grace int
		want  serviceControlAction
	}{
		{"stop encerra imediatamente", svc.Stop, 30, actionTeardownNow},
		{"stop sem grace", svc.Stop, 0, actionTeardownNow},
		{"preshutdown com grace segura", svc.PreShutdown, 30, actionHoldThenTeardown},
		{"preshutdown sem grace encerra", svc.PreShutdown, 0, actionTeardownNow},
		{"shutdown com grace segura", svc.Shutdown, 10, actionHoldThenTeardown},
		{"shutdown sem grace encerra", svc.Shutdown, 0, actionTeardownNow},
		{"interrogate responde status", svc.Interrogate, 30, actionInterrogate},
		{"desconhecido responde status", svc.Cmd(200), 30, actionInterrogate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveServiceControl(tc.cmd, tc.grace); got != tc.want {
				t.Fatalf("resolveServiceControl(%v,%d)=%d, want %d", tc.cmd, tc.grace, got, tc.want)
			}
		})
	}
}

func TestGraceDurationFor(t *testing.T) {
	if got := graceDurationFor(svc.PreShutdown, 30); got != 30*time.Second {
		t.Fatalf("preshutdown: got %s, want 30s", got)
	}
	// Fallback legacy (Shutdown) e limitado ao cap do Windows.
	if got := graceDurationFor(svc.Shutdown, 30); got != legacyShutdownGraceCap {
		t.Fatalf("shutdown cap: got %s, want %s", got, legacyShutdownGraceCap)
	}
	if got := graceDurationFor(svc.Shutdown, 2); got != 2*time.Second {
		t.Fatalf("shutdown curto: got %s, want 2s", got)
	}
}

func TestShutdownGraceSeconds(t *testing.T) {
	cases := []struct {
		env  string
		want int
	}{
		{"", defaultShutdownGraceSeconds},
		{"0", 0},
		{"60", 60},
		{"999", maxShutdownGraceSeconds},
		{"-5", 0},
		{"abc", defaultShutdownGraceSeconds},
	}
	for _, tc := range cases {
		t.Run("env="+tc.env, func(t *testing.T) {
			t.Setenv("DISCOVERY_SHUTDOWN_GRACE_SECONDS", tc.env)
			if got := shutdownGraceSeconds(); got != tc.want {
				t.Fatalf("shutdownGraceSeconds(%q)=%d, want %d", tc.env, got, tc.want)
			}
		})
	}
}
