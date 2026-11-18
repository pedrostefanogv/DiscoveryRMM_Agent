package netproxy

import "testing"

func TestParseAndValidateURL_ValidHTTPAndHTTPS(t *testing.T) {
	tests := []struct {
		raw      string
		schemes  []string
		wantHost string
		wantPort int
	}{
		{"http://192.168.1.10/status", []string{"http", "https"}, "192.168.1.10", 80},
		{"HTTPS://router.lan:8443/api?x=1", []string{"http", "https"}, "router.lan", 8443},
		{"https://[fd00::1]/", []string{"http", "https"}, "fd00::1", 443},
		{"http://[fd00::1]:8080/health", []string{"http", "https"}, "fd00::1", 8080},
		{"wss://nas.lan/ws", []string{"ws", "wss"}, "nas.lan", 443},
		{"ws://192.168.1.20:9000/stream", []string{"ws", "wss"}, "192.168.1.20", 9000},
	}

	for _, tc := range tests {
		u, host, port, err := parseAndValidateURL(tc.raw, tc.schemes...)
		if err != nil {
			t.Fatalf("parseAndValidateURL(%q) erro inesperado: %v", tc.raw, err)
		}
		if host != tc.wantHost || port != tc.wantPort || u == nil {
			t.Fatalf("parseAndValidateURL(%q) = (%q, %d), want (%q, %d)", tc.raw, host, port, tc.wantHost, tc.wantPort)
		}
	}
}

func TestParseAndValidateURL_RejectsDangerousAndMalformed(t *testing.T) {
	badURLs := []string{
		"",
		"   ",
		"file:///etc/passwd",
		"ftp://192.168.1.1/file",
		"gopher://192.168.1.1:70/",
		"http://192.168.1.1@127.0.0.1/",
		"http://user:pass@192.168.1.1/",
		"http://192.168.1.1:8080\\@127.0.0.1/",
		"http://[::1",
		"http://192.168.1.1:",
		"http://192.168.1.1:0/",
		"http://192.168.1.1:65536/",
		"http://192.168.1.1:abc/",
		"http://[fe80::1%25eth0]/",
		"http://[fe80::1%eth0]/",
		"http://192.168.1.1/\r\nInjected: 1",
		"http://192.168.1.1/\x00",
		"http:///path",
		"http:192.168.1.1",
	}

	for _, raw := range badURLs {
		if u, host, port, err := parseAndValidateURL(raw, "http", "https"); err == nil {
			t.Fatalf("parseAndValidateURL(%q) deveria rejeitar, retornou u=%v host=%q port=%d", raw, u, host, port)
		}
	}
}

func TestValidateHTTPMethod(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"", "GET"},
		{" get ", "GET"},
		{"post", "POST"},
		{"put", "PUT"},
		{"patch", "PATCH"},
		{"delete", "DELETE"},
		{"head", "HEAD"},
		{"options", "OPTIONS"},
	} {
		got, err := validateHTTPMethod(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("validateHTTPMethod(%q) = (%q, %v), want (%q, nil)", tc.in, got, err, tc.want)
		}
	}

	for _, bad := range []string{"CONNECT", "TRACE", "GET /", "FOO"} {
		if _, err := validateHTTPMethod(bad); err == nil {
			t.Fatalf("validateHTTPMethod(%q) deveria rejeitar", bad)
		}
	}
}
