package app

import "testing"

func boolPtrDependencyTest(v bool) *bool { return &v }

func TestResolveP2PFileTransferEnabled(t *testing.T) {
	cases := []struct {
		name          string
		requested     bool
		discovery     *bool
		cloud         *bool
		cloudFallback bool
		wantEnabled   bool
		wantMissing   bool
	}{
		{
			name:        "desativado no servidor permanece desativado",
			requested:   false,
			discovery:   boolPtrDependencyTest(true),
			wantEnabled: false,
		},
		{
			name:        "habilitado com descoberta de rede",
			requested:   true,
			discovery:   boolPtrDependencyTest(true),
			cloud:       boolPtrDependencyTest(false),
			wantEnabled: true,
		},
		{
			name:        "habilitado apenas com cloud bootstrap",
			requested:   true,
			discovery:   boolPtrDependencyTest(false),
			cloud:       boolPtrDependencyTest(true),
			wantEnabled: true,
		},
		{
			name:        "sem descoberta e sem cloud o P2P nao ativa",
			requested:   true,
			discovery:   boolPtrDependencyTest(false),
			cloud:       boolPtrDependencyTest(false),
			wantEnabled: false,
			wantMissing: true,
		},
		{
			name:        "descoberta ausente (nil) e tratada como permitida",
			requested:   true,
			discovery:   nil,
			cloud:       boolPtrDependencyTest(false),
			wantEnabled: true,
		},
		{
			name:          "cloud ausente usa o fallback local habilitado",
			requested:     true,
			discovery:     boolPtrDependencyTest(false),
			cloudFallback: true,
			wantEnabled:   true,
		},
		{
			name:          "cloud ausente usa o fallback local desabilitado",
			requested:     true,
			discovery:     boolPtrDependencyTest(false),
			cloudFallback: false,
			wantEnabled:   false,
			wantMissing:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enabled, missing := resolveP2PFileTransferEnabled(
				tc.requested, tc.discovery, tc.cloud, tc.cloudFallback)
			if enabled != tc.wantEnabled {
				t.Fatalf("enabled = %v, esperado %v", enabled, tc.wantEnabled)
			}
			if missing != tc.wantMissing {
				t.Fatalf("dependencyMissing = %v, esperado %v", missing, tc.wantMissing)
			}
		})
	}
}
