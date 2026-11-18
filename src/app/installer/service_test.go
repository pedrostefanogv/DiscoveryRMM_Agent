package installer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallerPersistUsesDPAPIAndHardenACL(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	var hardened []string

	svc := New(Deps{
		HardenFileACL: func(p string) error {
			hardened = append(hardened, p)
			return nil
		},
		ConfigPaths:   func() []string { return []string{cfgPath} },
		OverridePaths: func() []string { return nil },
		SharedPath:    func() string { return cfgPath },
	})

	outPath, err := svc.Persist("", Config{
		ServerAPI: "api.example.local",
		APIKey:    "deploy-secret-1",
		AuthToken: "auth-secret-2",
		AgentID:   "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7",
	})
	if err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if outPath != cfgPath {
		t.Fatalf("outPath = %q, want %q", outPath, cfgPath)
	}
	if len(hardened) != 1 {
		t.Fatalf("expected HardenFileACL called once on temp file, got %v", hardened)
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if runtime.GOOS == "windows" {
		if strings.Contains(string(raw), "deploy-secret-1") || strings.Contains(string(raw), "auth-secret-2") {
			t.Fatalf("plaintext tokens leaked in config.json: %s", string(raw))
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if v, _ := doc["authTokenDpapi"].(string); !strings.HasPrefix(v, "dpapi:v1:") {
			t.Fatalf("expected authTokenDpapi, got %v", doc["authTokenDpapi"])
		}
		if v, _ := doc["deployTokenDpapi"].(string); !strings.HasPrefix(v, "dpapi:v1:") {
			t.Fatalf("expected deployTokenDpapi, got %v", doc["deployTokenDpapi"])
		}
	}

	loaded, loadedPath, err := svc.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loadedPath != cfgPath {
		t.Fatalf("loadedPath = %q, want %q", loadedPath, cfgPath)
	}
	if loaded.AuthToken != "auth-secret-2" || loaded.APIKey != "deploy-secret-1" {
		t.Fatalf("decrypted secrets mismatch: authToken=%q apiKey=%q", loaded.AuthToken, loaded.APIKey)
	}
}

func TestInstallerLoadMigratesLegacyPlaintextAndEnsureDefaultHardensACL(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	var hardened []string

	svc := New(Deps{
		HardenFileACL: func(p string) error {
			hardened = append(hardened, p)
			return nil
		},
		ConfigPaths:   func() []string { return []string{cfgPath} },
		OverridePaths: func() []string { return nil },
		SharedPath:    func() string { return cfgPath },
	})

	// EnsureDefault when no config exists
	_, defPath, err := svc.EnsureDefault()
	if err != nil {
		t.Fatalf("EnsureDefault: %v", err)
	}
	if defPath != cfgPath || len(hardened) != 1 {
		t.Fatalf("EnsureDefault did not harden ACL: path=%q hardened=%v", defPath, hardened)
	}

	if runtime.GOOS != "windows" {
		return
	}

	// Overwrite with legacy plaintext config.json (as created by NSIS)
	legacy := `{"serverapi":"api.example.local","deployToken":"legacy-deploy-tok","authToken":"legacy-auth-tok","agentId":"8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7"}`
	if err := os.WriteFile(cfgPath, []byte(legacy), 0o600); err != nil {
		t.Fatalf("WriteFile legacy: %v", err)
	}

	loaded, _, err := svc.Load()
	if err != nil {
		t.Fatalf("Load legacy: %v", err)
	}
	if loaded.APIKey != "legacy-deploy-tok" || loaded.AuthToken != "legacy-auth-tok" {
		t.Fatalf("Load legacy secrets mismatch: %+v", loaded)
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("ReadFile migrated: %v", err)
	}
	if strings.Contains(string(raw), "legacy-deploy-tok") || strings.Contains(string(raw), "legacy-auth-tok") {
		t.Fatalf("legacy plaintext tokens remained on disk after Load(): %s", string(raw))
	}
	if !strings.Contains(string(raw), `"deployTokenDpapi": "dpapi:v1:`) || !strings.Contains(string(raw), `"authTokenDpapi": "dpapi:v1:`) {
		t.Fatalf("expected migrated DPAPI fields in config.json: %s", string(raw))
	}
}
