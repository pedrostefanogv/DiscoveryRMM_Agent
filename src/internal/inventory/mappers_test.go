package inventory

import (
	"testing"
)

func TestMapPrograms_NilInput(t *testing.T) {
	result := mapPrograms(nil, "test")
	if result != nil {
		t.Errorf("mapPrograms(nil, ...) should return nil, got %v", result)
	}
}

func TestMapPrograms_EmptyInput(t *testing.T) {
	result := mapPrograms([]map[string]any{}, "test")
	if result != nil {
		t.Errorf("mapPrograms(empty, ...) should return nil, got %v", result)
	}
}

func TestMapPrograms_SkipsEmptyName(t *testing.T) {
	rows := []map[string]any{
		{"name": "", "version": "1.0"},
		{"name": "  ", "version": "2.0"},
	}
	result := mapPrograms(rows, "test")
	if len(result) != 0 {
		t.Errorf("expected 0 items, got %d", len(result))
	}
}

func TestMapPrograms_MapsFields(t *testing.T) {
	rows := []map[string]any{
		{
			"name":               "Visual Studio Code",
			"version":            "1.85.0",
			"publisher":          "Microsoft",
			"identifying_number": "{ABC-123}",
			"uninstall_string":   "C:\\uninstall.exe",
		},
	}
	result := mapPrograms(rows, "osquery/programs")
	if len(result) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result))
	}
	item := result[0]
	if item.Name != "Visual Studio Code" {
		t.Errorf("Name = %q, want %q", item.Name, "Visual Studio Code")
	}
	if item.Version != "1.85.0" {
		t.Errorf("Version = %q, want %q", item.Version, "1.85.0")
	}
	if item.Publisher != "Microsoft" {
		t.Errorf("Publisher = %q, want %q", item.Publisher, "Microsoft")
	}
	if item.Source != "osquery/programs" {
		t.Errorf("Source = %q, want %q", item.Source, "osquery/programs")
	}
	if item.InstallID != "{ABC-123}" {
		t.Errorf("InstallID = %q, want %q", item.InstallID, "{ABC-123}")
	}
}

func TestMapMemoryModules_SkipsZeroSize(t *testing.T) {
	rows := []map[string]any{
		{"size": "0"},
		{"size": "-1"},
	}
	result := mapMemoryModules(rows)
	if len(result) != 0 {
		t.Errorf("expected 0 modules, got %d", len(result))
	}
}

func TestMapMemoryModules_ParsesSmallValues(t *testing.T) {
	// Value below ambiguity threshold should be treated as MB
	rows := []map[string]any{
		{
			"size":                   "8192",
			"device_locator":         "DIMM A",
			"bank_locator":           "BANK 0",
			"manufacturer":           "Samsung",
			"serial_number":          "ABC123",
			"configured_clock_speed": "3200",
			"memory_type":            "DDR4",
		},
	}
	result := mapMemoryModules(rows)
	if len(result) != 1 {
		t.Fatalf("expected 1 module, got %d", len(result))
	}
	m := result[0]
	if m.Manufacturer != "Samsung" {
		t.Errorf("Manufacturer = %q, want Samsung", m.Manufacturer)
	}
	if m.SizeMB != 8192 {
		t.Errorf("SizeMB = %d, want 8192", m.SizeMB)
	}
}

func TestMapNetworkRows_JoinsAddresses(t *testing.T) {
	ifaces := []map[string]any{
		{"interface": "eth0", "mac": "AA:BB:CC:DD:EE:FF", "type": "ethernet"},
	}
	addresses := []map[string]any{
		{"interface": "eth0", "address": "192.168.1.100", "mask": "24"},
		{"interface": "eth0", "address": "fe80::1", "mask": "64"},
	}
	routes := []map[string]any{
		{"interface": "eth0", "gateway": "192.168.1.1", "destination": "0.0.0.0"},
	}
	result := mapNetworkRows(ifaces, addresses, routes)
	if len(result) != 1 {
		t.Fatalf("expected 1 network, got %d", len(result))
	}
	n := result[0]
	if n.IPv4 != "192.168.1.100" {
		t.Errorf("IPv4 = %q, want 192.168.1.100", n.IPv4)
	}
	if n.IPv6 != "fe80::1" {
		t.Errorf("IPv6 = %q, want fe80::1", n.IPv6)
	}
	if n.Gateway != "192.168.1.1" {
		t.Errorf("Gateway = %q, want 192.168.1.1", n.Gateway)
	}
}

func TestMapListeningPorts_DeduplicatesAndFiltersInvalid(t *testing.T) {
	rows := []map[string]any{
		{
			"process_name": "nginx.exe",
			"pid":          "1234",
			"process_path": `C:\\nginx\\nginx.exe`,
			"protocol":     "tcp",
			"address":      "0.0.0.0",
			"port":         "443",
		},
		{
			"process_name": "nginx.exe",
			"pid":          "1234",
			"process_path": `C:\\nginx\\nginx.exe`,
			"protocol":     "tcp",
			"address":      "0.0.0.0",
			"port":         "443",
		},
		{
			"process_name": "bad",
			"pid":          "9999",
			"protocol":     "tcp",
			"address":      "127.0.0.1",
			"port":         "0",
		},
	}

	got := mapListeningPorts(rows)
	if len(got) != 1 {
		t.Fatalf("expected 1 listening port, got %d", len(got))
	}
	if got[0].Port != 443 {
		t.Fatalf("port = %d, want 443", got[0].Port)
	}
	if got[0].ProcessID != 1234 {
		t.Fatalf("pid = %d, want 1234", got[0].ProcessID)
	}
}

func TestMapOpenSockets_DeduplicatesAndKeepsValidPorts(t *testing.T) {
	rows := []map[string]any{
		{
			"process_name":   "chrome.exe",
			"pid":            "2222",
			"process_path":   `C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe`,
			"local_address":  "192.168.1.20",
			"local_port":     "56000",
			"remote_address": "142.250.218.14",
			"remote_port":    "443",
			"protocol":       "tcp",
			"family":         "2",
		},
		{
			"process_name":   "chrome.exe",
			"pid":            "2222",
			"process_path":   `C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe`,
			"local_address":  "192.168.1.20",
			"local_port":     "56000",
			"remote_address": "142.250.218.14",
			"remote_port":    "443",
			"protocol":       "tcp",
			"family":         "2",
		},
		{
			"process_name":   "invalid.exe",
			"pid":            "3333",
			"local_address":  "0.0.0.0",
			"local_port":     "0",
			"remote_address": "",
			"remote_port":    "0",
			"protocol":       "tcp",
			"family":         "2",
		},
	}

	got := mapOpenSockets(rows)
	if len(got) != 1 {
		t.Fatalf("expected 1 open socket, got %d", len(got))
	}
	if got[0].RemotePort != 443 {
		t.Fatalf("remotePort = %d, want 443", got[0].RemotePort)
	}
	if got[0].LocalPort != 56000 {
		t.Fatalf("localPort = %d, want 56000", got[0].LocalPort)
	}
}

func TestHelpers_ParseInt(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"42", 42},
		{"", 0},
		{"abc", 0},
		{" 10 ", 10},
	}
	for _, tt := range tests {
		got := parseInt(tt.input)
		if got != tt.want {
			t.Errorf("parseInt(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestHelpers_ParseBoolLoose(t *testing.T) {
	truthy := []string{"1", "true", "True", "yes", "up", "connected"}
	for _, v := range truthy {
		if !parseBoolLoose(v) {
			t.Errorf("parseBoolLoose(%q) should be true", v)
		}
	}
	falsy := []string{"0", "false", "", "no", "down", "disconnected"}
	for _, v := range falsy {
		if parseBoolLoose(v) {
			t.Errorf("parseBoolLoose(%q) should be false", v)
		}
	}
}

func TestHelpers_FirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", " ", "hello"); got != "hello" {
		t.Errorf("firstNonEmpty(\"\", \" \", \"hello\") = %q, want \"hello\"", got)
	}
	if got := firstNonEmpty("first", "second"); got != "first" {
		t.Errorf("firstNonEmpty(\"first\", \"second\") = %q, want \"first\"", got)
	}
	if got := firstNonEmpty("", ""); got != "" {
		t.Errorf("firstNonEmpty(\"\", \"\") = %q, want \"\"", got)
	}
}

func TestHelpers_Round2(t *testing.T) {
	tests := []struct {
		input float64
		want  float64
	}{
		{1.234, 1.23},
		{1.235, 1.24},
		{0.0, 0.0},
		{-1.456, -1.46},
	}
	for _, tt := range tests {
		got := round2(tt.input)
		if got != tt.want {
			t.Errorf("round2(%f) = %f, want %f", tt.input, got, tt.want)
		}
	}
}

func TestSanitizeHardwareFields_NilReport(t *testing.T) {
	// Should not panic
	sanitizeHardwareFields(nil)
}

func TestSanitizeHardwareValue(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Dell Inc.", "Dell Inc."},
		{"Default string", ""},
		{"To Be Filled By O.E.M.", ""},
		{"N/A", ""},
		{"Unknown", ""},
		{"", ""},
		{"  \x00  ", ""},
	}
	for _, tt := range tests {
		got := sanitizeHardwareValue(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeHardwareValue(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFindOsqueryBinary_NegativeCacheCanBeInvalidated(t *testing.T) {
	InvalidateOsqueryBinaryCache()

	_, _ = FindOsqueryBinary()

	InvalidateOsqueryBinaryCache()
	path, err := FindOsqueryBinary()
	if err == nil && path == "" {
		t.Fatalf("expected non-empty path when err is nil")
	}
}
