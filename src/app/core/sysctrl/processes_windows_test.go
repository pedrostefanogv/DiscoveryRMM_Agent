//go:build windows

package sysctrl

import (
	"os"
	"strings"
	"testing"
)

func TestKillProcessRefusesSelfPID(t *testing.T) {
	self := uint32(os.Getpid())
	err := KillProcess(self)
	if err == nil {
		t.Fatal("KillProcess(self) succeeded, expected refusal")
	}
	if !strings.Contains(err.Error(), "recusa") {
		t.Fatalf("expected self-PID refusal message, got %v", err)
	}
}

func TestProcessImagePath(t *testing.T) {
	if _, err := ProcessImagePath(0); err == nil {
		t.Fatal("ProcessImagePath(0) expected error")
	}

	self := uint32(os.Getpid())
	path, err := ProcessImagePath(self)
	if err != nil {
		t.Fatalf("ProcessImagePath(%d) failed: %v", self, err)
	}
	if !strings.HasSuffix(strings.ToLower(path), ".exe") {
		t.Fatalf("ProcessImagePath(%d) = %q, expected .exe suffix", self, path)
	}
}

func TestListProcessesIncludesCurrentProcess(t *testing.T) {
	procs, err := ListProcesses()
	if err != nil {
		t.Fatalf("ListProcesses failed: %v", err)
	}
	if len(procs) == 0 {
		t.Fatal("ListProcesses returned 0 processes")
	}

	self := uint32(os.Getpid())
	found := false
	for _, p := range procs {
		if p.PID == self {
			found = true
			if strings.TrimSpace(p.Name) == "" {
				t.Errorf("current process %d has empty Name", self)
			}
			break
		}
	}
	if !found {
		t.Fatalf("current process PID %d not found in ListProcesses (%d entries)", self, len(procs))
	}
}
