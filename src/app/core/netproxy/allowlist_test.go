package netproxy

import (
	"context"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

type fakeResolver struct {
	calls atomic.Int32
	fn    func(call int32, host string) ([]net.IPAddr, error)
}

func (f *fakeResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	c := f.calls.Add(1)
	return f.fn(c, host)
}

func TestAllowlist_EmptyDeniesAll(t *testing.T) {
	a := NewAllowlist()
	if !a.IsEmpty() {
		t.Fatal("nova allowlist deveria estar vazia")
	}
	for _, target := range []struct {
		host string
		port int
	}{
		{"192.168.1.10", 80},
		{"10.0.0.1", 443},
		{"127.0.0.1", 3389},
		{"example.com", 80},
	} {
		if a.IsAllowed(target.host, target.port) {
			t.Fatalf("allowlist vazia deveria bloquear %s:%d", target.host, target.port)
		}
	}
}

func TestAllowlist_CIDRAndBareIPAndPorts(t *testing.T) {
	a := NewAllowlist()
	if err := a.AddEntry("192.168.1.0/24"); err != nil {
		t.Fatalf("AddEntry CIDR: %v", err)
	}
	if err := a.AddEntry("10.20.30.40"); err != nil {
		t.Fatalf("AddEntry IP: %v", err)
	}
	a.AddPort(8080)
	a.AddPort(443)

	if !a.IsAllowed("192.168.1.55", 8080) {
		t.Fatal("esperava 192.168.1.55:8080 permitido")
	}
	if !a.IsAllowed("10.20.30.40", 443) {
		t.Fatal("esperava 10.20.30.40:443 permitido")
	}
	if a.IsAllowed("192.168.1.55", 22) {
		t.Fatal("porta 22 deveria ser bloqueada")
	}
	if a.IsAllowed("192.168.2.55", 8080) {
		t.Fatal("IP fora do CIDR deveria ser bloqueado")
	}
	if a.IsAllowed("10.20.30.41", 443) {
		t.Fatal("IP fora do /32 deveria ser bloqueado")
	}
	if !a.ContainsIP(net.ParseIP("192.168.1.99")) {
		t.Fatal("ContainsIP deveria aceitar 192.168.1.99")
	}
	if a.ContainsIP(net.ParseIP("172.16.0.1")) {
		t.Fatal("ContainsIP deveria bloquear 172.16.0.1")
	}
}

func TestAllowlist_SSRFDefaultBlocksEvenWhenDisabledOrWildcard(t *testing.T) {
	ssrfTargets := []string{
		"127.0.0.1",
		"127.0.0.5",
		"::1",
		"169.254.169.254",
		"169.254.170.2",
		"fe80::1",
		"fd00:ec2::254",
		"0.0.0.0",
		"0.0.0.1",
		"::",
		"224.0.0.1",
		"ff02::1",
		"255.255.255.255",
		"::ffff:127.0.0.1",
		"::ffff:169.254.169.254",
		"localhost",
		"sub.localhost",
	}

	wildcard := NewAllowlist()
	_ = wildcard.AddCIDR("0.0.0.0/0")
	_ = wildcard.AddCIDR("::/0")
	wildcard.AddPort(80)

	disabled := NewAllowlist()
	disabled.SetEnabled(false)

	for _, host := range ssrfTargets {
		if wildcard.IsAllowed(host, 80) {
			t.Fatalf("wildcard allowlist deveria bloquear SSRF %q", host)
		}
		if disabled.IsAllowed(host, 80) {
			t.Fatalf("disabled allowlist deveria bloquear SSRF %q", host)
		}
		if ip := net.ParseIP(host); ip != nil {
			if wildcard.ContainsIP(ip) || disabled.ContainsIP(ip) {
				t.Fatalf("ContainsIP deveria bloquear SSRF %q", host)
			}
		}
	}
}

func TestAllowlist_AllowLoopbackPermitsLoopbackStillBlocksMetadataAndUnspecified(t *testing.T) {
	a := NewAllowlist()
	a.SetAllowLoopback(true)
	_ = a.AddCIDR("127.0.0.0/8")
	_ = a.AddCIDR("::1/128")
	a.AddPort(8080)

	if !a.IsAllowed("127.0.0.1", 8080) {
		t.Fatal("esperava 127.0.0.1:8080 permitido quando allowLoopback=true e na allowlist")
	}
	if !a.IsAllowed("::1", 8080) {
		t.Fatal("esperava ::1:8080 permitido quando allowLoopback=true e na allowlist")
	}
	if !a.IsAllowed("::ffff:127.0.0.1", 8080) {
		t.Fatal("esperava ::ffff:127.0.0.1:8080 normalizado e permitido quando allowLoopback=true")
	}

	// Mesmo com allowLoopback=true, sem CIDR correspondente continua bloqueado:
	onlyLan := NewAllowlist()
	onlyLan.SetAllowLoopback(true)
	_ = onlyLan.AddCIDR("192.168.1.0/24")
	if onlyLan.IsAllowed("127.0.0.1", 8080) {
		t.Fatal("loopback nao deve passar sem CIDR correspondente")
	}

	// Metadata e unspecified continuam sempre bloqueados:
	_ = a.AddCIDR("0.0.0.0/0")
	_ = a.AddCIDR("::/0")
	for _, blocked := range []string{"169.254.169.254", "fe80::1", "fd00:ec2::254", "0.0.0.0", "::", "224.0.0.1"} {
		if a.IsAllowed(blocked, 8080) {
			t.Fatalf("SSRF incondicional %q nao pode ser liberado por allowLoopback", blocked)
		}
	}
}

func TestAllowlist_ExplicitRDPEndpointOnlyPermitsExactIPAndPort(t *testing.T) {
	a := NewAllowlist()
	if err := a.AllowRDPEndpoint(3389); err != nil {
		t.Fatalf("AllowRDPEndpoint: %v", err)
	}

	if !a.IsAllowed("127.0.0.1", 3389) {
		t.Fatal("esperava 127.0.0.1:3389 permitido")
	}
	if !a.IsAllowed("::ffff:127.0.0.1", 3389) {
		t.Fatal("esperava ::ffff:127.0.0.1:3389 normalizado e permitido")
	}
	for _, tc := range []struct {
		host string
		port int
	}{
		{"127.0.0.1", 80},
		{"127.0.0.1", 3390},
		{"127.0.0.2", 3389},
		{"::1", 3389},
		{"169.254.169.254", 3389},
		{"192.168.1.10", 3389},
	} {
		if a.IsAllowed(tc.host, tc.port) {
			t.Fatalf("endpoint %s:%d deveria ser bloqueado na allowlist RDP", tc.host, tc.port)
		}
	}
}

func TestAllowlist_MultiIPResolutionMustValidateAllIPs(t *testing.T) {
	a := NewAllowlist()
	_ = a.AddCIDR("192.168.1.0/24")
	a.AddPort(80)

	a.SetResolver(&fakeResolver{
		fn: func(_ int32, _ string) ([]net.IPAddr, error) {
			return []net.IPAddr{
				{IP: net.ParseIP("192.168.1.10")},
				{IP: net.ParseIP("127.0.0.1")},
			}, nil
		},
	})
	if a.IsAllowed("mixed-loopback.lan", 80) {
		t.Fatal("resolucao com [192.168.1.10, 127.0.0.1] deveria ser rejeitada")
	}

	a.SetResolver(&fakeResolver{
		fn: func(_ int32, _ string) ([]net.IPAddr, error) {
			return []net.IPAddr{
				{IP: net.ParseIP("192.168.1.10")},
				{IP: net.ParseIP("169.254.169.254")},
			}, nil
		},
	})
	if a.IsAllowed("mixed-metadata.lan", 80) {
		t.Fatal("resolucao com [192.168.1.10, 169.254.169.254] deveria ser rejeitada")
	}

	a.SetResolver(&fakeResolver{
		fn: func(_ int32, _ string) ([]net.IPAddr, error) {
			return []net.IPAddr{
				{IP: net.ParseIP("192.168.1.10")},
				{IP: net.ParseIP("192.168.1.11")},
			}, nil
		},
	})
	if !a.IsAllowed("valid-multi.lan", 80) {
		t.Fatal("resolucao com dois IPs na allowlist deveria ser permitida")
	}
}

func TestAllowlist_DialAllowedSingleLookupAntiTOCTOU(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	go func() {
		conn, err := ln.Accept()
		if err == nil {
			_ = conn.Close()
		}
	}()

	res := &fakeResolver{
		fn: func(call int32, _ string) ([]net.IPAddr, error) {
			if call == 1 {
				return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
			}
			// Se houvesse segunda consulta, devolveria IP de metadata bloqueado
			return []net.IPAddr{{IP: net.ParseIP("169.254.169.254")}}, nil
		},
	}

	a := NewAllowlist()
	_ = a.AddExplicitEndpoint(netip.MustParseAddr("127.0.0.1"), port)
	a.SetResolver(res)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, err := DialTCPAllowed(ctx, a, "printer.lan", port)
	if err != nil {
		t.Fatalf("DialTCPAllowed falhou: %v", err)
	}
	_ = conn.Close()

	if calls := res.calls.Load(); calls != 1 {
		t.Fatalf("esperava exatamente 1 consulta DNS (anti-TOCTOU), got %d", calls)
	}
}
