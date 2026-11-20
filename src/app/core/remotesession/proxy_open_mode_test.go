//go:build windows

package remotesession

import (
	"testing"
)

// Testes do modo "liberado geral" do proxy (enabled=false no payload de start).
//
// Contexto do defeito: o servidor enviava `enabled` no bloco "proxy" do start,
// mas ParseProxyPolicy NAO lia essa chave e BuildProxyAllowlist nunca chamava
// SetEnabled. Resultado: escolher "Liberar acesso geral" na tela nao mudava
// nada — a allowlist chegava vazia e o gate fail-closed bloqueava TODO destino,
// com "allowlist vazia: acesso bloqueado para <ip>:<porta>".
//
// A propriedade de seguranca que estes testes protegem: "liberado geral" NAO
// pode abrir as faixas SSRF incondicionais (link-local, multicast, 169.254.x.x,
// metadados de nuvem). Se alguem "simplificar" isso removendo a checagem
// incondicional, o teste abaixo falha.
func TestProxyPolicy_EnabledFlag(t *testing.T) {
	// Modo allowlist: enabled=true, gate de CIDR ativo.
	allow := ParseProxyPolicy(map[string]any{
		"proxy": map[string]any{
			"enabled":          true,
			"defaultAllowlist": []any{"192.168.10.0/24"},
		},
	}, KindProxy)
	if !allow.Enabled {
		t.Fatal("enabled=true deveria manter o gate de allowlist ativo")
	}

	// Modo "liberado geral": enabled=false.
	open := ParseProxyPolicy(map[string]any{
		"proxy": map[string]any{
			"enabled":          false,
			"defaultAllowlist": []any{},
			"allowedPorts":     []any{},
		},
	}, KindProxy)
	if open.Enabled {
		t.Fatal("enabled=false deveria desligar o gate de allowlist")
	}
}

// Payload ausente/incompleto NAO pode liberar o proxy: o default tem de ser
// fail-closed (gate ativo).
func TestProxyPolicy_EnabledDefaultsToClosed(t *testing.T) {
	if got := ParseProxyPolicy(nil, KindProxy); !got.Enabled {
		t.Fatal("payload nil deveria manter o gate ATIVO (fail-closed)")
	}

	// Bloco proxy sem a chave "enabled" (payload antigo): tambem fail-closed.
	legacy := ParseProxyPolicy(map[string]any{
		"proxy": map[string]any{"defaultAllowlist": []any{"10.0.0.0/8"}},
	}, KindProxy)
	if !legacy.Enabled {
		t.Fatal("payload sem 'enabled' deveria manter o gate ATIVO (fail-closed)")
	}
}

// O modo "liberado geral" precisa realmente DISCAR para destinos arbitrarios,
// que era o que nao funcionava.
func TestProxyAllowlist_OpenModeAllowsArbitraryDestination(t *testing.T) {
	policy := ParseProxyPolicy(map[string]any{
		"proxy": map[string]any{"enabled": false},
	}, KindProxy)

	al, err := BuildProxyAllowlist(policy, KindProxy, 0)
	if err != nil {
		t.Fatalf("BuildProxyAllowlist: %v", err)
	}

	// Destino publico e o caso relatado do bug (LAN do cliente).
	for _, target := range []struct {
		host string
		port int
	}{
		{"203.0.113.10", 8006},
		{"192.168.10.250", 8006}, // o destino que falhava na tela
		{"10.0.0.5", 22},
	} {
		if !al.IsAllowed(target.host, target.port) {
			t.Fatalf("modo liberado geral deveria permitir %s:%d", target.host, target.port)
		}
	}
}

// MESMO com o gate desligado, os alvos SSRF incondicionais continuam barrados.
func TestProxyAllowlist_OpenModeStillBlocksUnconditionalSSRF(t *testing.T) {
	policy := ParseProxyPolicy(map[string]any{
		// allowLoopback=true e o caso mais permissivo possivel: nem assim as
		// faixas incondicionais podem passar.
		"proxy": map[string]any{"enabled": false, "allowLoopback": true},
	}, KindProxy)

	al, err := BuildProxyAllowlist(policy, KindProxy, 0)
	if err != nil {
		t.Fatalf("BuildProxyAllowlist: %v", err)
	}

	blocked := []struct {
		host string
		port int
	}{
		{"169.254.169.254", 80}, // metadados de nuvem (AWS/GCP/Azure)
		{"fd00:ec2::254", 80},   // IMDS v2 IPv6
		{"255.255.255.255", 80}, // broadcast
		{"0.0.0.0", 80},         // unspecified
		{"224.0.0.1", 80},       // multicast
	}

	for _, target := range blocked {
		if al.IsAllowed(target.host, target.port) {
			t.Fatalf("liberado geral NAO pode alcancar %s:%d (SSRF incondicional)", target.host, target.port)
		}
	}
}

// Sem a flag, o modo allowlist continua restringindo como antes.
func TestProxyAllowlist_AllowlistModeStillRestricts(t *testing.T) {
	policy := ParseProxyPolicy(map[string]any{
		"proxy": map[string]any{
			"enabled":          true,
			"defaultAllowlist": []any{"192.168.10.0/24"},
			"allowedPorts":     []any{float64(8006)},
		},
	}, KindProxy)

	al, err := BuildProxyAllowlist(policy, KindProxy, 0)
	if err != nil {
		t.Fatalf("BuildProxyAllowlist: %v", err)
	}

	if !al.IsAllowed("192.168.10.250", 8006) {
		t.Fatal("destino aprovado deveria passar")
	}
	if al.IsAllowed("192.168.10.250", 22) {
		t.Fatal("porta fora da lista deveria ser bloqueada")
	}
	if al.IsAllowed("10.0.0.5", 8006) {
		t.Fatal("IP fora da allowlist deveria ser bloqueado")
	}
}
