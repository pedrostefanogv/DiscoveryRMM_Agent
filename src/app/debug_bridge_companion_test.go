//go:build windows

package app

// Regressão P7: em modo companion a UI NÃO grava debug_config.json
// localmente. Se o RPC ao serviço falhar, o erro sobe para a UI e nenhum
// arquivo é criado/alterado (o serviço é o dono único da config).

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	debug "discovery/app/debug"
)

func TestSetDebugConfigCompanionRPCFailureDoesNotWriteLocalConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug_config.json")
	svc := debug.NewService(debug.Options{
		ConfigPaths:     func() []string { return []string{path} },
		WatcherInterval: time.Hour,
		HardenFileACL:   func(string) error { return nil },
	})

	a := &App{ctx: context.Background()}
	a.DebugSvc = svc
	// Cliente IPC sem conexão: Request falha rápido ("não conectado").
	a.ipcClient = NewIPCClient(nil, nil)

	err := a.SetDebugConfig(DebugConfig{
		ApiScheme: "https",
		ApiServer: "api.example.local",
		AuthToken: "token-123",
		AgentID:   configSetTestAgentID,
	})
	if err == nil {
		t.Fatal("falha de RPC deveria retornar erro explícito para a UI")
	}

	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("nenhum arquivo local deveria ser gravado em falha de RPC (statErr=%v)", statErr)
	}
	if cfg := svc.GetConfig(); cfg.ApiServer != "" || cfg.AuthToken != "" {
		t.Fatalf("config local não deveria ser aplicada em falha de RPC: %+v", cfg)
	}
}
