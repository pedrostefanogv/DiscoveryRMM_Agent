package main

import (
	"encoding/json"
	"testing"
)

func TestInstallerConfigUnmarshalDiscoveryEnabledBool(t *testing.T) {
	var cfg InstallerConfig
	err := json.Unmarshal([]byte(`{"serverUrl":"api.example.com","apiKey":"key","discoveryEnabled":true}`), &cfg)
	if err != nil {
		t.Fatalf("unmarshal bool: %v", err)
	}
	if cfg.DiscoveryEnabled == nil || !*cfg.DiscoveryEnabled {
		t.Fatalf("discoveryEnabled deveria ser true")
	}
}

func TestInstallerConfigUnmarshalDiscoveryEnabledNumericCompatibility(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "one means true", raw: `{"discoveryEnabled":1}`, want: true},
		{name: "zero means false", raw: `{"discoveryEnabled":0}`, want: false},
		{name: "string one means true", raw: `{"discoveryEnabled":"1"}`, want: true},
		{name: "string false means false", raw: `{"discoveryEnabled":"false"}`, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var cfg InstallerConfig
			if err := json.Unmarshal([]byte(tc.raw), &cfg); err != nil {
				t.Fatalf("unmarshal compat: %v", err)
			}
			if cfg.DiscoveryEnabled == nil {
				t.Fatal("discoveryEnabled nao deveria ser nil")
			}
			if *cfg.DiscoveryEnabled != tc.want {
				t.Fatalf("discoveryEnabled = %v, want %v", *cfg.DiscoveryEnabled, tc.want)
			}
		})
	}
}

func TestInstallerConfigUnmarshalDiscoveryEnabledInvalid(t *testing.T) {
	var cfg InstallerConfig
	err := json.Unmarshal([]byte(`{"discoveryEnabled":2}`), &cfg)
	if err == nil {
		t.Fatal("esperava erro para discoveryEnabled invalido")
	}
}
