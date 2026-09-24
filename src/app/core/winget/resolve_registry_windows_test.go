//go:build windows

package winget

import (
	"testing"
)

// TestFromAppPathsRegistry_ResolvesMachineWidePath garante que a camada de
// registro encontra o caminho REAL do winget (WindowsApps) mesmo quando o alias
// do perfil do usuário não está disponível — o cenário do serviço LocalSystem.
func TestFromAppPathsRegistry_ResolvesMachineWidePath(t *testing.T) {
	p := fromAppPathsRegistry()
	if p == "" {
		t.Skip("winget nao registrado em App Paths neste host")
	}
	if !usable(p) {
		t.Fatalf("caminho do registro nao existe: %s", p)
	}
	t.Logf("winget via App Paths: %s", p)
}
