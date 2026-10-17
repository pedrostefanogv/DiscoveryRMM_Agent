package app

// Testes de regressão do RPC "config:set" (serviço é dono único da config):
// patch semântico, validação server-side e reaplicação imediata.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	debug "discovery/app/debug"
)

const configSetTestAgentID = "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7"

func newConfigSetTestApp(t *testing.T) (*App, *debug.Service, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "debug_config.json")
	svc := debug.NewService(debug.Options{
		ConfigPaths:     func() []string { return []string{path} },
		WatcherInterval: time.Hour,
		// Não aplica DACL real em t.TempDir: o processo de teste não-SYSTEM
		// perderia DELETE no arquivo e o cleanup do TempDir falharia.
		HardenFileACL: func(string) error { return nil },
	})
	if err := svc.SetConfig(DebugConfig{
		ApiScheme:  "https",
		ApiServer:  "api.original.local",
		AuthToken:  "token-original",
		AgentID:    configSetTestAgentID,
		NatsServer: "nats://nats.original.local:4222",
		Scheme:     "nats",
		Server:     "nats://nats.original.local:4222",
	}); err != nil {
		t.Fatalf("seed SetConfig: %v", err)
	}
	a := &App{}
	a.DebugSvc = svc
	return a, svc, path
}

func TestIPCRPCConfigSetPartialPatchPreservesFields(t *testing.T) {
	a, svc, path := newConfigSetTestApp(t)

	resp := a.ipcRPCConfigSet(map[string]any{
		"apiServer":      "api.patch.local",
		"debugMode":      true,
		"startMinimized": true,
	})
	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("config:set falhou: %v", resp["error"])
	}

	cfg := svc.GetConfig()
	if cfg.ApiServer != "api.patch.local" {
		t.Fatalf("apiServer não aplicado: %q", cfg.ApiServer)
	}
	// Patch parcial NÃO pode apagar campos não enviados.
	if cfg.AuthToken != "token-original" || cfg.AgentID != configSetTestAgentID {
		t.Fatalf("campos não enviados foram apagados: %+v", cfg)
	}
	if cfg.NatsServer != "nats://nats.original.local:4222" {
		t.Fatalf("natsServer não enviado foi alterado: %q", cfg.NatsServer)
	}
	if !a.RuntimeFlags.DebugMode || !a.RuntimeFlags.StartMinimized {
		t.Fatalf("runtime flags não reaplicados: %+v", a.RuntimeFlags)
	}

	// Persistência efetiva do arquivo também preserva o restante.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ler config persistida: %v", err)
	}
	var persisted DebugConfig
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("config persistida inválida: %v", err)
	}
	if persisted.ApiServer != "api.patch.local" || persisted.AuthToken != "token-original" {
		t.Fatalf("arquivo persistido não reflete o patch semântico: %+v", persisted)
	}

	// Resposta devolve a config EFETIVA.
	dataMap, _ := resp["data"].(map[string]any)
	if dataMap["apiServer"] != "api.patch.local" || dataMap["authToken"] != "token-original" {
		t.Fatalf("data efetiva inesperada: %+v", dataMap)
	}
	if dataMap["debugMode"] != true || dataMap["startMinimized"] != true {
		t.Fatalf("data efetiva sem runtime flags: %+v", dataMap)
	}
}

func TestIPCRPCConfigSetAcceptsNestedConfigPayload(t *testing.T) {
	a, svc, _ := newConfigSetTestApp(t)

	resp := a.ipcRPCConfigSet(map[string]any{
		"config": map[string]any{"authToken": "token-novo"},
	})
	if ok, _ := resp["ok"].(bool); !ok {
		t.Fatalf("config:set aninhado falhou: %v", resp["error"])
	}
	if got := svc.GetConfig(); got.AuthToken != "token-novo" || got.ApiServer != "api.original.local" {
		t.Fatalf("patch aninhado inesperado: %+v", got)
	}
}

func TestIPCRPCConfigSetValidationRejectsWithoutWriting(t *testing.T) {
	cases := []struct {
		name  string
		patch map[string]any
	}{
		{name: "apiServer scheme inválido", patch: map[string]any{"apiServer": "ftp://api.local"}},
		{name: "natsWsServer scheme inválido", patch: map[string]any{"natsWsServer": "http://nats.local:8222"}},
		{name: "natsServer com espaços", patch: map[string]any{"natsServer": "nats://a b:4222"}},
		{name: "agentId não-GUID", patch: map[string]any{"agentId": "nao-e-guid"}},
		{name: "authToken vazio", patch: map[string]any{"authToken": "   "}},
		{name: "tipo debugMode errado", patch: map[string]any{"debugMode": "sim"}},
		{name: "tipo apiServer errado", patch: map[string]any{"apiServer": 123}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, svc, path := newConfigSetTestApp(t)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ler arquivo antes: %v", err)
			}
			cfgBefore := svc.GetConfig()

			resp := a.ipcRPCConfigSet(tc.patch)
			if ok, _ := resp["ok"].(bool); ok {
				t.Fatalf("esperava rejeição, veio ok=true: %+v", resp)
			}
			if msg, _ := resp["error"].(string); msg == "" {
				t.Fatal("resposta de erro deve explicar o motivo")
			}

			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ler arquivo depois: %v", err)
			}
			if string(before) != string(after) {
				t.Fatal("config:set inválido NÃO pode gravar o arquivo")
			}
			if got := svc.GetConfig(); !reflect.DeepEqual(got, cfgBefore) {
				t.Fatalf("config em memória mudou após rejeição: %+v", got)
			}
		})
	}
}
