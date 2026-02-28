package export

import (
	"strings"
	"testing"

	"winget-store/internal/models"
)

func TestBuildMarkdown_Basic(t *testing.T) {
	report := models.InventoryReport{
		CollectedAt: "2026-01-01T00:00:00Z",
		Source:      "test",
		Hardware: models.HardwareInfo{
			Hostname:     "testhost",
			Manufacturer: "Dell",
			Model:        "Latitude",
			CPU:          "Intel i7",
			Cores:        4,
			LogicalCores: 8,
			MemoryGB:     16.0,
		},
		OS: models.OperatingSystem{
			Name:         "Windows 11",
			Version:      "10.0",
			Build:        "22631",
			Architecture: "x86_64",
		},
		Software: []models.SoftwareItem{
			{Name: "Git", Version: "2.40", Publisher: "Git SCM", Source: "test"},
		},
	}

	md := BuildMarkdown(report, false)

	if !strings.Contains(md, "# Inventario Discovery") {
		t.Error("missing main title")
	}
	if !strings.Contains(md, "testhost") {
		t.Error("hostname not found")
	}
	if !strings.Contains(md, "Dell") {
		t.Error("manufacturer not found")
	}
	if !strings.Contains(md, "Git") {
		t.Error("software name not found")
	}
}

func TestBuildMarkdown_Redacted(t *testing.T) {
	report := models.InventoryReport{
		CollectedAt: "2026-01-01T00:00:00Z",
		Source:      "test",
		Hardware: models.HardwareInfo{
			Hostname:          "secret-host",
			MotherboardSerial: "SN12345",
			BIOSSerial:        "BIOS999",
		},
		OS: models.OperatingSystem{Name: "Windows"},
	}

	md := BuildMarkdown(report, true)

	if strings.Contains(md, "secret-host") {
		t.Error("hostname should be redacted")
	}
	if strings.Contains(md, "SN12345") {
		t.Error("motherboard serial should be redacted")
	}
	if strings.Contains(md, "BIOS999") {
		t.Error("BIOS serial should be redacted")
	}
	if !strings.Contains(md, Redacted) {
		t.Error("redacted placeholder not found")
	}
}

func TestBuildMarkdown_PipesEscaped(t *testing.T) {
	report := models.InventoryReport{
		CollectedAt: "2026-01-01T00:00:00Z",
		Source:      "test",
		Hardware:    models.HardwareInfo{Hostname: "host|name"},
		OS:          models.OperatingSystem{Name: "Win|11"},
	}

	md := BuildMarkdown(report, false)

	if strings.Contains(md, "host|name") {
		t.Error("pipe in hostname should be escaped")
	}
	if !strings.Contains(md, `host\|name`) {
		t.Error("pipe should be escaped as \\|")
	}
}

func TestRedactHardware(t *testing.T) {
	hw := models.HardwareInfo{
		Hostname:          "mypc",
		Manufacturer:      "Dell",
		MotherboardSerial: "SN123",
		BIOSSerial:        "BIOS456",
	}

	redacted := RedactHardware(hw)

	if redacted.Hostname != Redacted {
		t.Errorf("Hostname = %q, want %q", redacted.Hostname, Redacted)
	}
	if redacted.MotherboardSerial != Redacted {
		t.Errorf("MotherboardSerial = %q, want %q", redacted.MotherboardSerial, Redacted)
	}
	if redacted.BIOSSerial != Redacted {
		t.Errorf("BIOSSerial = %q, want %q", redacted.BIOSSerial, Redacted)
	}
	// Non-sensitive fields should be preserved
	if redacted.Manufacturer != "Dell" {
		t.Errorf("Manufacturer = %q, want Dell (should not be redacted)", redacted.Manufacturer)
	}
}

func TestMd_Sanitizer(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", "-"},
		{"  ", "-"},
		{"hello", "hello"},
		{"pipe|char", `pipe\|char`},
		{"multi\nline", "multi line"},
	}
	for _, tt := range tests {
		got := md(tt.input)
		if got != tt.want {
			t.Errorf("md(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
