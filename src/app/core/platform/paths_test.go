package platform

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"discovery/app/core/envutil"
)

func TestUIPathsIsolatedFromProgramData(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific LOCALAPPDATA vs ProgramData isolation")
	}
	t.Cleanup(envutil.Reset)
	envutil.Reset()
	t.Setenv("ProgramData", `C:\ProgramData`)
	t.Setenv("LOCALAPPDATA", `C:\Users\alice\AppData\Local`)
	t.Setenv("DISCOVERY_LOG_FILE", "")
	envutil.Init()

	dataDir := DataDir()
	uiDir := UIDataDir()
	expectedRoot := filepath.Join(`C:\Users\alice\AppData\Local`, "Discovery")
	if uiDir != expectedRoot {
		t.Fatalf("UIDataDir() = %q, want %q", uiDir, expectedRoot)
	}
	if strings.HasPrefix(strings.ToLower(uiDir), strings.ToLower(dataDir)) {
		t.Fatalf("UIDataDir() = %q must not live under DataDir() = %q", uiDir, dataDir)
	}

	checks := map[string]string{
		"UIWebView2Root": UIWebView2Root(),
		"UILogDir":       UILogDir(),
		"UILogDBPath":    UILogDBPath(),
		"UIChatDBPath":   UIChatDBPath(),
		"UILogFilePath":  UILogFilePath(),
	}
	for name, val := range checks {
		if !strings.HasPrefix(strings.ToLower(val), strings.ToLower(expectedRoot)) {
			t.Fatalf("%s() = %q must live under %q", name, val, expectedRoot)
		}
		if strings.HasPrefix(strings.ToLower(val), strings.ToLower(dataDir)) {
			t.Fatalf("%s() = %q must not live under %q", name, val, dataDir)
		}
	}
}
