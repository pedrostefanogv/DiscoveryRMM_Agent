package mcp

import (
	"runtime"
	"testing"
)

func TestValidateLocalHostOrIP_Privates(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "localhost", "192.168.1.1", "10.0.0.5"} {
		if err := ValidateLocalHostOrIP(host); err != nil {
			t.Errorf("expected %q to be allowed, got error: %v", host, err)
		}
	}
}

func TestValidateLocalHostOrIP_PublicIP(t *testing.T) {
	if err := ValidateLocalHostOrIP("8.8.8.8"); err == nil {
		t.Errorf("expected public IP to be rejected")
	}
}

func TestBuildPingCommand_ContainsHost(t *testing.T) {
	cmd, err := buildPingCommand("192.168.0.1", 1, 1)
	if err != nil {
		t.Fatalf("buildPingCommand failed: %v", err)
	}
	if len(cmd.Args) == 0 {
		t.Fatal("expected command args")
	}
	found := false
	for _, a := range cmd.Args {
		if a == "192.168.0.1" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected ping args to include host, got %v", cmd.Args)
	}
}

func TestBuildFlushDNSCommand_Exists(t *testing.T) {
	cmd, err := buildFlushDNSCommand()
	if err != nil {
		t.Skipf("flush DNS command not available on this platform (%s): %v", runtime.GOOS, err)
	}
	if cmd == nil || len(cmd.Args) == 0 {
		t.Fatalf("expected valid command, got %v", cmd)
	}
}
