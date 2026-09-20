//go:build windows

package native

import "testing"

func TestPrinterStatusString(t *testing.T) {
	cases := map[int]string{
		3: "Ready",
		4: "Printing",
		5: "Warmup",
		6: "Stopped Printing",
		7: "Offline",
		1: "Other",
		2: "Unknown",
		0: "",
		9: "",
	}
	for status, want := range cases {
		if got := printerStatusString(status); got != want {
			t.Errorf("printerStatusString(%d) = %q, want %q", status, got, want)
		}
	}
}

func TestPrinterIsNetwork(t *testing.T) {
	cases := []struct {
		name    string
		attrs   int
		port    string
		wantNet bool
	}{
		{"bit de rede marcado", 0x10, "nul:", true},
		{"porta IP_ sem bit", 0x40, "IP_192.168.1.50", true},
		{"local comum", 0x40 | 0x8, "USB001", false},
		{"porta http", 0, "HTTP://printer.local", true},
		{"porta vazia", 0, "", false},
	}
	for _, tc := range cases {
		if got := printerIsNetwork(tc.attrs, tc.port); got != tc.wantNet {
			t.Errorf("%s: printerIsNetwork(%#x, %q) = %v, want %v", tc.name, tc.attrs, tc.port, got, tc.wantNet)
		}
	}
}
