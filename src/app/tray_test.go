package app

import (
	"bytes"
	"testing"
)

func TestResolveTrayIconState(t *testing.T) {
	tests := []struct {
		name       string
		configured bool
		connected  bool
		want       int32
	}{
		{name: "not provisioned uses provisioning icon", configured: false, connected: false, want: trayIconStateProvisioning},
		{name: "offline provisioned uses offline icon", configured: true, connected: false, want: trayIconStateOffline},
		{name: "connected provisioned uses normal icon", configured: true, connected: true, want: trayIconStateNormal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveTrayIconState(tt.configured, tt.connected); got != tt.want {
				t.Fatalf("resolveTrayIconState(%t, %t) = %d, want %d", tt.configured, tt.connected, got, tt.want)
			}
		})
	}
}

func TestTrayIconForStateFallsBackToNormal(t *testing.T) {
	a := &App{
		trayIcon:         []byte("normal"),
		trayProvisioning: []byte("provisioning"),
	}

	if got := a.trayIconForState(trayIconStateProvisioning); !bytes.Equal(got, []byte("provisioning")) {
		t.Fatalf("trayIconForState(provisioning) = %q, want provisioning", string(got))
	}

	if got := a.trayIconForState(trayIconStateOffline); !bytes.Equal(got, []byte("normal")) {
		t.Fatalf("trayIconForState(offline) fallback = %q, want normal", string(got))
	}
}
