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

// Regressão do relato (ícone laranja eterno): a UI companion roda sem elevação
// e não consegue ler o config.json — endurecido para SYSTEM/Administrators.
// O estado "provisionado" tem de vir do serviço pelo snapshot IPC; sem isso o
// tray fica preso em provisioning mesmo com o agente online.
func TestTrayIconUsesCompanionOnboardingWhenLocalConfigUnreadable(t *testing.T) {
	prev := hasServerCredentials
	hasServerCredentials = func() bool { return false }
	t.Cleanup(func() { hasServerCredentials = prev })

	a := &App{ipcClient: &IPCClient{}}
	a.storeCompanionOnboarding("normal", true, "", "")

	if got := a.currentTrayIconState(); got == trayIconStateProvisioning {
		t.Fatal("com o serviço reportando configured=true o tray não pode ficar em provisioning (laranja)")
	}
}

// Sem snapshot do serviço (UI recém-aberta) o padrão seguro continua sendo
// "não provisionado": não inventamos provisionamento sem evidência.
func TestTrayIconStaysProvisioningWithoutCompanionSnapshot(t *testing.T) {
	prev := hasServerCredentials
	hasServerCredentials = func() bool { return false }
	t.Cleanup(func() { hasServerCredentials = prev })

	a := &App{ipcClient: &IPCClient{}}
	if got := a.currentTrayIconState(); got != trayIconStateProvisioning {
		t.Fatalf("sem snapshot do serviço, tray state = %d, esperado provisioning", got)
	}
}

// Regressão de ponta a ponta do caminho REAL: o payload do serviço
// (agent:status_snapshot, com as chaves onboarding*) precisa atravessar
// handleCompanionSnapshotEvent e mudar o ícone do tray. Os testes acima montam
// o estado por storeCompanionOnboarding direto; este cobre o mapeamento das
// chaves que o serviço envia (ipc_app_integration.go: onboardingConfigured).
func TestCompanionSnapshotFeedsTrayState(t *testing.T) {
	prev := hasServerCredentials
	hasServerCredentials = func() bool { return false }
	t.Cleanup(func() { hasServerCredentials = prev })

	a := &App{ipcClient: &IPCClient{}}
	a.handleCompanionSnapshotEvent(map[string]any{
		"name":                 "agent:status_snapshot",
		"connected":            true,
		"transportConnected":   true,
		"onboardingMode":       "normal",
		"onboardingConfigured": true,
		"onboardingMessage":    nil,
	})

	if got := a.currentTrayIconState(); got == trayIconStateProvisioning {
		t.Fatal("snapshot real do serviço com onboardingConfigured=true deve tirar o tray de provisioning")
	}

	// E o inverso: snapshot dizendo que o agente NÃO tem credenciais mantém o laranja.
	b := &App{ipcClient: &IPCClient{}}
	b.handleCompanionSnapshotEvent(map[string]any{
		"name":                 "agent:status_snapshot",
		"connected":            false,
		"onboardingMode":       "awaiting-auto-provisioning",
		"onboardingConfigured": false,
	})
	if got := b.currentTrayIconState(); got != trayIconStateProvisioning {
		t.Fatalf("onboardingConfigured=false deve manter provisioning, veio %d", got)
	}
}

// Serviço reportando configured=false precisa manter o laranja (agente
// realmente sem credenciais).
func TestTrayIconHonoursCompanionConfiguredFalse(t *testing.T) {
	prev := hasServerCredentials
	hasServerCredentials = func() bool { return false }
	t.Cleanup(func() { hasServerCredentials = prev })

	a := &App{ipcClient: &IPCClient{}}
	a.storeCompanionOnboarding("awaiting-auto-provisioning", false, "aguardando auto-provisioning", "not-provisioned")

	if got := a.currentTrayIconState(); got != trayIconStateProvisioning {
		t.Fatalf("configured=false do serviço deve manter provisioning, veio %d", got)
	}
}
