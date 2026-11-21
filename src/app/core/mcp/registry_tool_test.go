package mcp

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"
)

func mustArgsJSON(t *testing.T, v map[string]any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal(args): %v", err)
	}
	return b
}

func TestRegistryRead_BlocksProtectedPaths(t *testing.T) {
	reg := NewRegistry()
	registerRegistryReadTool(reg)

	for _, protected := range []string{`SAM`, `SECURITY`, `SAM\Domains`} {
		_, err := reg.Call(context.Background(), "registry_read", mustArgsJSON(t, map[string]any{
			"action": "list",
			"hive":   "HKLM",
			"path":   protected,
		}))
		if err == nil {
			t.Fatalf("expected HKLM\\%s to be blocked by winregistry guard", protected)
		}
	}
}

func TestRegistryRead_ReadsPublicKeyOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows only")
	}
	reg := NewRegistry()
	registerRegistryReadTool(reg)

	res, err := reg.Call(context.Background(), "registry_read", mustArgsJSON(t, map[string]any{
		"action":    "get_value",
		"hive":      "HKLM",
		"path":      `SOFTWARE\Microsoft\Windows NT\CurrentVersion`,
		"valueName": "ProductName",
	}))
	if err != nil {
		t.Fatalf("registry_read get_value failed: %v", err)
	}
	raw, _ := json.Marshal(res)
	if len(raw) == 0 {
		t.Fatal("expected non-empty registry value")
	}
}

func TestDiskCleanupTemp_RequiresConsentAndConfirm(t *testing.T) {
	if c := ToolConsentFor("disk", map[string]any{"action": "cleanup_temp"}); c == nil {
		t.Fatal("expected ToolConsentFor(disk, cleanup_temp) to require consent")
	}
	if c := ToolConsentFor("disk", map[string]any{"action": "temp"}); c != nil {
		t.Fatal("expected ToolConsentFor(disk, temp) NOT to require consent")
	}

	reg := NewRegistry()
	registerDiskTool(reg)
	if _, err := reg.Call(context.Background(), "disk", mustArgsJSON(t, map[string]any{"action": "cleanup_temp"})); err == nil {
		t.Fatal("expected disk(cleanup_temp) without confirm=true to be rejected")
	}
}

func TestWindowsUpdate_NativeStatusAndPendingReboot(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows only")
	}
	reg := NewRegistry()
	registerWindowsUpdateTool(reg)

	if _, err := reg.Call(context.Background(), "windows_update", mustArgsJSON(t, map[string]any{"action": "status"})); err != nil {
		t.Fatalf("windows_update(status) failed: %v", err)
	}
	if _, err := reg.Call(context.Background(), "windows_update", mustArgsJSON(t, map[string]any{"action": "pending_reboot"})); err != nil {
		t.Fatalf("windows_update(pending_reboot) failed: %v", err)
	}
}

func TestAdGpoCerts_ValidatesAction(t *testing.T) {
	reg := NewRegistry()
	registerAdGpoCertsTool(reg)
	if reg.Find("ad_gpo_certs") == nil {
		t.Fatal("expected ad_gpo_certs to be registered")
	}
	if _, err := reg.Call(context.Background(), "ad_gpo_certs", mustArgsJSON(t, map[string]any{"action": "invalid"})); err == nil {
		t.Fatal("expected invalid action to return error")
	}
}

func TestIsUserSessionTool(t *testing.T) {
	if !IsUserSessionTool("open_folder", nil) {
		t.Error("expected open_folder to be user-session tool")
	}
	if !IsUserSessionTool("screen", map[string]any{"action": "capture"}) {
		t.Error("expected screen(capture) to be user-session tool")
	}
	if IsUserSessionTool("screen", map[string]any{"action": "permission_status"}) {
		t.Error("expected screen(permission_status) NOT to require user session")
	}
	if !IsUserSessionTool("power_action", map[string]any{"action": "lock"}) {
		t.Error("expected power_action(lock) to be user-session tool")
	}
	if IsUserSessionTool("power_action", map[string]any{"action": "restart"}) {
		t.Error("expected power_action(restart) NOT to be user-session tool")
	}
	if !IsUserSessionTool("shares", map[string]any{"action": "mapped_drives"}) {
		t.Error("expected shares(mapped_drives) to be user-session tool")
	}
}

func TestConsolidatedTools_ValidationAndConfirm(t *testing.T) {
	if c := ToolConsentFor("software", map[string]any{"action": "install", "id": "Google.Chrome"}); c == nil {
		t.Fatal("expected software(install) to require consent")
	}
	if c := ToolConsentFor("software", map[string]any{"action": "search", "query": "chrome"}); c != nil {
		t.Fatal("expected software(search) NOT to require consent")
	}
	reg := newTestRegistry()
	if _, err := reg.Call(context.Background(), "software", mustArgsJSON(t, map[string]any{"action": "install", "id": "Google.Chrome"})); err == nil {
		t.Fatal("expected software(install) without confirm=true to be rejected")
	}
	if _, err := reg.Call(context.Background(), "event_log", mustArgsJSON(t, map[string]any{"action": "invalid"})); err == nil {
		t.Fatal("expected invalid action in event_log to fail")
	}
	if _, err := reg.Call(context.Background(), "local_memory", mustArgsJSON(t, map[string]any{"action": "invalid"})); err == nil {
		t.Fatal("expected invalid action in local_memory to fail")
	}
	if _, err := reg.Call(context.Background(), "screen", mustArgsJSON(t, map[string]any{"action": "invalid"})); err == nil {
		t.Fatal("expected invalid action in screen to fail")
	}
}
