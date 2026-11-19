//go:build windows

package sysctrl

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func TestStateName(t *testing.T) {
	cases := []struct {
		state svc.State
		want  string
	}{
		{svc.Stopped, "stopped"},
		{svc.StartPending, "start_pending"},
		{svc.StopPending, "stop_pending"},
		{svc.Running, "running"},
		{svc.ContinuePending, "continue_pending"},
		{svc.PausePending, "pause_pending"},
		{svc.Paused, "paused"},
		{svc.State(999), "unknown"},
	}
	for _, tc := range cases {
		if got := stateName(tc.state); got != tc.want {
			t.Errorf("stateName(%d) = %q, want %q", tc.state, got, tc.want)
		}
	}
}

func TestStartTypeName(t *testing.T) {
	cases := []struct {
		startType uint32
		want      string
	}{
		{mgr.StartAutomatic, "auto"},
		{mgr.StartManual, "demand"},
		{mgr.StartDisabled, "disabled"},
		{999, "unknown"},
	}
	for _, tc := range cases {
		if got := startTypeName(tc.startType); got != tc.want {
			t.Errorf("startTypeName(%d) = %q, want %q", tc.startType, got, tc.want)
		}
	}
}

func TestIsStoppedError(t *testing.T) {
	if isStoppedError(nil) {
		t.Fatal("nil should not match isStoppedError")
	}
	if isStoppedError(errors.New("generic")) {
		t.Fatal("generic error should not match isStoppedError")
	}
	if !isStoppedError(syscall.Errno(1062)) {
		t.Fatal("ERROR_SERVICE_NOT_ACTIVE (1062) should match")
	}
	if !isStoppedError(fmt.Errorf("stop failed: %w", syscall.Errno(1062))) {
		t.Fatal("wrapped ERROR_SERVICE_NOT_ACTIVE (1062) should match")
	}
	if isStoppedError(syscall.Errno(5)) {
		t.Fatal("ERROR_ACCESS_DENIED (5) should not match")
	}
}

func TestListServicesReturnsValidServices(t *testing.T) {
	svcs, err := ListServices()
	if err != nil {
		t.Fatalf("ListServices failed: %v", err)
	}
	if len(svcs) == 0 {
		t.Fatal("ListServices returned 0 services")
	}
	for _, s := range svcs {
		if strings.TrimSpace(s.Name) == "" {
			t.Fatalf("found service with empty Name: %+v", s)
		}
		if s.State == "" || s.State == "unknown" {
			t.Errorf("service %q has unexpected State %q", s.Name, s.State)
		}
	}
}
