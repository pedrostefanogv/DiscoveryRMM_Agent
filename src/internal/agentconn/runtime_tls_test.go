package agentconn

import "testing"

func TestEvaluateTLSPinPolicy_SignalR(t *testing.T) {
	tests := []struct {
		name      string
		observed  string
		expected  string
		enforce   bool
		wantError bool
	}{
		{name: "compat mode allows empty", observed: "", expected: "", enforce: false, wantError: false},
		{name: "enforce blocks missing expected", observed: "AA", expected: "", enforce: true, wantError: true},
		{name: "enforce blocks missing observed", observed: "", expected: "AA", enforce: true, wantError: true},
		{name: "enforce allows match", observed: "aa:bb", expected: "AABB", enforce: true, wantError: false},
		{name: "enforce blocks mismatch", observed: "AA11", expected: "BB22", enforce: true, wantError: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := evaluateTLSPinPolicy("signalr", tc.observed, tc.expected, tc.enforce)
			if tc.wantError && err == nil {
				t.Fatal("esperava erro")
			}
			if !tc.wantError && err != nil {
				t.Fatalf("nao esperava erro, got %v", err)
			}
		})
	}
}

func TestEvaluateTLSPinPolicy_NATSWSS(t *testing.T) {
	if err := evaluateTLSPinPolicy("nats-wss", "11 22", "1122", true); err != nil {
		t.Fatalf("esperava validacao ok para nats-wss: %v", err)
	}

	if err := evaluateTLSPinPolicy("nats-wss", "1122", "3344", true); err == nil {
		t.Fatal("esperava erro para hash divergente em nats-wss")
	}
}

func TestRewriteNATSHost(t *testing.T) {
	got, err := rewriteNATSHost("wss://nats.old.local:8443", "nats.new.local")
	if err != nil {
		t.Fatalf("rewriteNATSHost retornou erro: %v", err)
	}
	if got != "wss://nats.new.local:8443" {
		t.Fatalf("rewriteNATSHost = %q", got)
	}
}

func TestBuildExternalNATSWSSURL(t *testing.T) {
	got, err := buildExternalNATSWSSURL("broker.external.local:443")
	if err != nil {
		t.Fatalf("buildExternalNATSWSSURL retornou erro: %v", err)
	}
	if got != "wss://broker.external.local:443/nats/" {
		t.Fatalf("buildExternalNATSWSSURL = %q", got)
	}
}

func TestAutoDeriveNATSEndpoints_RemoteHostPrefersWSS(t *testing.T) {
	cfg := &Config{NatsServerHost: "tngplacas.com.br"}
	derivedNATS, derivedWSS := autoDeriveNATSEndpoints(cfg)

	if derivedNATS {
		t.Fatal("nao deveria derivar nats:// para host remoto")
	}
	if !derivedWSS {
		t.Fatal("deveria derivar endpoint wss para host remoto")
	}
	if cfg.NatsServer != "" {
		t.Fatalf("NatsServer = %q, esperado vazio", cfg.NatsServer)
	}
	if cfg.NatsWsServer != "wss://tngplacas.com.br/nats/" {
		t.Fatalf("NatsWsServer = %q", cfg.NatsWsServer)
	}
}

func TestAutoDeriveNATSEndpoints_LocalHostDerivesNATSAndWSS(t *testing.T) {
	cfg := &Config{NatsServerHost: "192.168.1.10"}
	derivedNATS, derivedWSS := autoDeriveNATSEndpoints(cfg)

	if !derivedNATS {
		t.Fatal("deveria derivar nats:// para host local")
	}
	if !derivedWSS {
		t.Fatal("deveria derivar wss:// para host local")
	}
	if cfg.NatsServer != "nats://192.168.1.10:4222" {
		t.Fatalf("NatsServer = %q", cfg.NatsServer)
	}
	if cfg.NatsWsServer != "wss://192.168.1.10/nats/" {
		t.Fatalf("NatsWsServer = %q", cfg.NatsWsServer)
	}
}

func TestAutoDeriveNATSEndpoints_WSSExternalSkipsNATS(t *testing.T) {
	cfg := &Config{NatsServerHost: "nats.example.com", NatsUseWssExternal: true}
	derivedNATS, derivedWSS := autoDeriveNATSEndpoints(cfg)

	if derivedNATS {
		t.Fatal("nao deveria derivar nats:// quando NatsUseWssExternal=true")
	}
	if !derivedWSS {
		t.Fatal("deveria derivar endpoint wss quando host estiver presente")
	}
	if cfg.NatsServer != "" {
		t.Fatalf("NatsServer = %q, esperado vazio", cfg.NatsServer)
	}
}
