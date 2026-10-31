package agentconn

import (
	"net"
	"testing"
)

func TestIsCloudflareIP(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		// Observado nos logs do agente: dial 172.67.182.88:4222 e 104.21.18.147:4222
		// sempre com i/o timeout (Cloudflare não encaminha a porta 4222).
		{"172.67.182.88", true},
		{"104.21.18.147", true},
		{"104.16.0.1", true},
		{"188.114.96.1", true},
		// Fora do Cloudflare — deve tentar o dial normal.
		{"8.8.8.8", false},
		{"192.168.1.10", false},
		{"10.0.0.5", false},
		{"2606:4700:4700::1111", true},
		{"2606:4700::a29f:1", true},
		{"2001:db8::1", false},
	}
	for _, c := range cases {
		got := isCloudflareIP(net.ParseIP(c.ip))
		if got != c.want {
			t.Errorf("isCloudflareIP(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}

func TestIsCloudflareIPNil(t *testing.T) {
	if isCloudflareIP(nil) {
		t.Error("isCloudflareIP(nil) = true, want false")
	}
}
