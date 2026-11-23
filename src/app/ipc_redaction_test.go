package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"discovery/app/core/testutil"
	debug "discovery/app/debug"
	"discovery/app/logs"
)

func newTestAppWithDebugService(t *testing.T, initial debug.Config) (*App, *[]debug.Config) {
	t.Helper()
	var emitted []debug.Config
	a := &App{
		ctx: context.Background(),
	}
	a.Logs.Buffer = logs.New()

	cfgPath := filepath.Join(testutil.TempDir(t), "debug_config.json")
	a.DebugSvc = debug.NewService(debug.Options{
		ConfigPaths:         func() []string { return []string{cfgPath} },
		WatcherInterval:     time.Hour,
		HardenFileACL:       func(string) error { return nil },
		ValidateFileACL:     func(string) error { return nil },
		ValidateLoadFileACL: func(string) error { return nil },
		OnConfigChanged: func(cfg debug.Config) {
			emitted = append(emitted, redactDebugConfig(cfg))
		},
	})
	if err := a.DebugSvc.SetConfig(initial); err != nil {
		t.Fatalf("SetConfig initial: %v", err)
	}
	return a, &emitted
}

func TestIPCRedactsAuthTokenAcrossAllPaths(t *testing.T) {
	initial := debug.Config{
		ApiScheme: "https",
		ApiServer: "api.example.local",
		AuthToken: "secret-agent-token-xyz",
		AgentID:   "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7",
	}
	a, emitted := newTestAppWithDebugService(t, initial)

	// 1. debug:get
	resp := a.ipcRPCDebugGet()
	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("ipcRPCDebugGet failed: %+v", resp)
	}
	data := resp["data"].(map[string]any)
	rawCfg, _ := json.Marshal(data["config"])
	var got map[string]any
	if err := json.Unmarshal(rawCfg, &got); err != nil {
		t.Fatalf("unmarshal debug:get: %v", err)
	}
	if _, hasPlain := got["authToken"]; hasPlain && got["authToken"] != "" {
		t.Fatalf("debug:get leaked authToken: %+v", got)
	}
	if _, hasDpapi := got["authTokenDpapi"]; hasDpapi {
		t.Fatalf("debug:get leaked authTokenDpapi: %+v", got)
	}
	if has, _ := got["hasAuthToken"].(bool); !has {
		t.Fatalf("expected hasAuthToken=true in debug:get, got %+v", got)
	}

	// 2. configSetEffectiveData (config:set response)
	eff, err := configSetEffectiveData(a.DebugSvc.GetConfig(), a.RuntimeFlags)
	if err != nil {
		t.Fatalf("configSetEffectiveData: %v", err)
	}
	if _, hasPlain := eff["authToken"]; hasPlain {
		t.Fatalf("configSetEffectiveData leaked authToken: %+v", eff)
	}
	if _, hasDpapi := eff["authTokenDpapi"]; hasDpapi {
		t.Fatalf("configSetEffectiveData leaked authTokenDpapi: %+v", eff)
	}
	if has, _ := eff["hasAuthToken"].(bool); !has {
		t.Fatalf("expected hasAuthToken=true in configSetEffectiveData, got %+v", eff)
	}

	// 3. OnConfigChanged (debug:config_updated)
	if len(*emitted) == 0 {
		t.Fatalf("expected OnConfigChanged to record emitted config")
	}
	last := (*emitted)[len(*emitted)-1]
	if last.AuthToken != "" || last.AuthTokenDpapi != "" || !last.HasAuthToken {
		t.Fatalf("debug:config_updated leaked token or missed hasAuthToken: %+v", last)
	}
}

func TestIPCDebugSetPreservesTokenWhenRedactedEmpty(t *testing.T) {
	initial := debug.Config{
		ApiScheme: "https",
		ApiServer: "api.example.local",
		AuthToken: "preserved-secret-token",
		AgentID:   "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7",
	}
	a, _ := newTestAppWithDebugService(t, initial)

	// Companion UI submits updated ApiServer with empty authToken (because input is redacted)
	resp := a.ipcRPCDebugSet(map[string]any{
		"config": map[string]any{
			"apiScheme": "https",
			"apiServer": "api2.example.local",
			"authToken": "",
			"agentId":   initial.AgentID,
		},
	})
	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("ipcRPCDebugSet failed: %+v", resp)
	}

	live := a.DebugSvc.GetConfig()
	if live.ApiServer != "api2.example.local" {
		t.Fatalf("ApiServer = %q, want api2.example.local", live.ApiServer)
	}
	if live.AuthToken != "preserved-secret-token" {
		t.Fatalf("ipcRPCDebugSet wiped AuthToken: got %q", live.AuthToken)
	}

	// Verify response config is also redacted
	data := resp["data"].(map[string]any)
	rawCfg, _ := json.Marshal(data["config"])
	var got debug.Config
	if err := json.Unmarshal(rawCfg, &got); err != nil {
		t.Fatalf("unmarshal response config: %v", err)
	}
	if got.AuthToken != "" || !got.HasAuthToken {
		t.Fatalf("ipcRPCDebugSet response leaked token or missed hasAuthToken: %+v", got)
	}
}
