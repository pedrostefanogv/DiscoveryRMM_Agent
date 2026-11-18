package debug

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPersistConfigEncryptsAuthTokenAndClearsPlaintext(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("DPAPI encryption test is Windows-specific")
	}
	path := testConfigPath(t)
	svc := serviceForPath(path, time.Hour)

	cfg := validConfig("dpapi.example.local")
	cfg.AuthToken = "super-secret-agent-token"
	if err := svc.PersistConfig(cfg); err != nil {
		t.Fatalf("PersistConfig: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(raw), "super-secret-agent-token") {
		t.Fatalf("plaintext authToken leaked to disk: %s", string(raw))
	}
	var onDisk map[string]any
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, hasPlain := onDisk["authToken"]; hasPlain {
		t.Fatalf("expected authToken key omitted on disk, got %v", onDisk["authToken"])
	}
	dpapiVal, _ := onDisk["authTokenDpapi"].(string)
	if !strings.HasPrefix(dpapiVal, "dpapi:v1:") {
		t.Fatalf("expected dpapi:v1: authTokenDpapi, got %q", dpapiVal)
	}

	svc2 := serviceForPath(path, time.Hour)
	svc2.LoadPersistedConfig()
	defer svc2.StopWatcher()
	if got := svc2.GetConfig().AuthToken; got != "super-secret-agent-token" {
		t.Fatalf("decrypted AuthToken = %q, want %q", got, "super-secret-agent-token")
	}
}

func TestLoadPersistedConfigMigratesLegacyPlaintext(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("DPAPI migration test is Windows-specific")
	}
	path := testConfigPath(t)
	legacy := `{"apiScheme":"https","apiServer":"migrate.example.local","authToken":"tok-legado-123","agentId":"8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7","scheme":"https"}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	svc := serviceForPath(path, time.Hour)
	svc.LoadPersistedConfig()
	defer svc.StopWatcher()

	if got := svc.GetConfig().AuthToken; got != "tok-legado-123" {
		t.Fatalf("loaded AuthToken = %q, want tok-legado-123", got)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile after migration: %v", err)
	}
	if strings.Contains(string(raw), "tok-legado-123") {
		t.Fatalf("legacy plaintext token was not migrated off disk: %s", string(raw))
	}
	if !strings.Contains(string(raw), `"authTokenDpapi": "dpapi:v1:`) {
		t.Fatalf("expected migrated authTokenDpapi on disk, got: %s", string(raw))
	}
}

func TestWatcherRejectsUntrustedACLAndPreservesMemory(t *testing.T) {
	path := testConfigPath(t)
	initial := validConfig("trusted.example.local")
	data, _ := json.Marshal(initial)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile initial: %v", err)
	}

	hookCalled := false
	svc := NewService(Options{
		ConfigPaths:         func() []string { return []string{path} },
		WatcherInterval:     time.Hour,
		HardenFileACL:       func(string) error { return nil },
		ValidateLoadFileACL: func(string) error { return nil },
		ValidateFileACL:     func(string) error { return errors.New("owner nao confiavel (Users:M)") },
	})
	svc.SetExternalConfigApplyHook(func(Config) { hookCalled = true })
	svc.LoadPersistedConfig()
	defer svc.StopWatcher()

	poisoned := validConfig("evil.example.local")
	poisoned.AllowInsecureTLS = true
	poisonData, _ := json.Marshal(poisoned)
	if err := os.WriteFile(path, poisonData, 0o600); err != nil {
		t.Fatalf("WriteFile poisoned: %v", err)
	}

	svc.pollExternalConfigChange()

	if got := svc.GetConfig(); got.ApiServer != "trusted.example.local" || got.AllowInsecureTLS {
		t.Fatalf("untrusted file poisoned in-memory config: %+v", got)
	}
	if hookCalled {
		t.Fatalf("externalHook must not run when ACL validation fails")
	}
}

func TestLoadPersistedConfigQuarantinesUntrustedOwner(t *testing.T) {
	path := testConfigPath(t)
	poisoned := validConfig("evil-boot.example.local")
	poisoned.AllowInsecureTLS = true
	data, _ := json.Marshal(poisoned)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	svc := NewService(Options{
		ConfigPaths:         func() []string { return []string{path} },
		WatcherInterval:     time.Hour,
		HardenFileACL:       func(string) error { return nil },
		ValidateFileACL:     func(string) error { return nil },
		ValidateLoadFileACL: func(string) error { return errors.New("diretorio gravavel por Users") },
	})
	svc.LoadPersistedConfig()
	defer svc.StopWatcher()

	if got := svc.GetConfig(); got.ApiServer != "" || got.AllowInsecureTLS {
		t.Fatalf("expected untrusted config at boot to be rejected, got %+v", got)
	}
	if _, err := os.Stat(path + ".untrusted"); err != nil {
		t.Fatalf("expected untrusted file to be quarantined at %s.untrusted: %v", filepath.Base(path), err)
	}
}
