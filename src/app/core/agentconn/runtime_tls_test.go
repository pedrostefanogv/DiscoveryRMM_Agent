package agentconn

import "testing"

func TestEvaluateTLSPinPolicy_NATSTransport(t *testing.T) {
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
			err := evaluateTLSPinPolicy("nats-wss", tc.observed, tc.expected, tc.enforce)
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
	if cfg.NatsWsServer != "wss://tngplacas.com.br:443/nats/" {
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
	if cfg.NatsWsServer != "wss://192.168.1.10:443/nats/" {
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

func TestAutoDeriveNATSEndpoints_InternalHostDerivesNATS_EvenWithWSSExternal(t *testing.T) {
	// Host interno presente: NATS nativo deve ser derivado mesmo com
	// NatsUseWssExternal=true (que governa apenas o WSS externo).
	cfg := &Config{NatsServerHost: "nats.example.com", NatsServerHostInternal: "nats.internal.local", NatsUseWssExternal: true}
	derivedNATS, derivedWSS := autoDeriveNATSEndpoints(cfg)

	if !derivedNATS {
		t.Fatal("deveria derivar nats:// a partir do host interno")
	}
	if !derivedWSS {
		t.Fatal("deveria derivar wss:// a partir do host externo")
	}
	if cfg.NatsServer != "nats://nats.internal.local:4222" {
		t.Fatalf("NatsServer = %q", cfg.NatsServer)
	}
	if cfg.NatsWsServer != "wss://nats.example.com:443/nats/" {
		t.Fatalf("NatsWsServer = %q", cfg.NatsWsServer)
	}
}

func TestAutoDeriveNATSEndpoints_InternalHostOnly(t *testing.T) {
	// Sem host externo, o interno é usado tanto para nativo quanto para WSS.
	cfg := &Config{NatsServerHostInternal: "nats.internal.local"}
	derivedNATS, derivedWSS := autoDeriveNATSEndpoints(cfg)

	if !derivedNATS {
		t.Fatal("deveria derivar nats:// a partir do host interno")
	}
	if !derivedWSS {
		t.Fatal("deveria derivar wss:// a partir do host interno")
	}
	if cfg.NatsServer != "nats://nats.internal.local:4222" {
		t.Fatalf("NatsServer = %q", cfg.NatsServer)
	}
	if cfg.NatsWsServer != "wss://nats.internal.local:443/nats/" {
		t.Fatalf("NatsWsServer = %q", cfg.NatsWsServer)
	}
}

func TestIsPrivate172Range(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"172.20.10.5", true},
		{"172.32.0.1", false}, // público
		{"172.40.1.1", false}, // público
		{"172.15.0.1", false}, // público
		{"192.168.1.1", false},
		{"10.0.0.1", false},
		{"nats.internal.local", false},
	}
	for _, c := range cases {
		if got := isPrivate172Range(c.host); got != c.want {
			t.Errorf("isPrivate172Range(%q) = %v, esperado %v", c.host, got, c.want)
		}
	}
}

func TestIsLocalOrPrivateHost_172Range(t *testing.T) {
	if !isLocalOrPrivateHost("172.16.0.1") {
		t.Fatal("172.16.0.1 deveria ser privado")
	}
	if isLocalOrPrivateHost("172.32.0.1") {
		t.Fatal("172.32.0.1 nao deveria ser privado")
	}
}

func TestNATSWebSocketProxyPath(t *testing.T) {
	tests := []struct {
		name   string
		url    string
		expect string
	}{
		{name: "wss with nats path", url: "wss://tngplacas.com.br:443/nats/", expect: "/nats/"},
		{name: "ws with path", url: "ws://localhost:8080/socket", expect: "/socket"},
		{name: "wss root", url: "wss://tngplacas.com.br:443/", expect: ""},
		{name: "wss no path", url: "wss://tngplacas.com.br:443", expect: ""},
		{name: "nats tcp", url: "nats://tngplacas.com.br:4222", expect: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := natsWebSocketProxyPath(tc.url); got != tc.expect {
				t.Fatalf("natsWebSocketProxyPath(%q) = %q, esperado %q", tc.url, got, tc.expect)
			}
		})
	}
}
