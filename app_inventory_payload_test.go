package main

import (
	"encoding/json"
	"strings"
	"testing"

	"winget-store/internal/models"
)

func TestBuildAgentHardwareEnvelope_UsesStringStatusAndRawInventoryObject(t *testing.T) {
	report := models.InventoryReport{
		CollectedAt: "2026-03-12T19:31:36Z",
		Source:      "osquery",
		Hardware: models.HardwareInfo{
			Hostname: "PC-123",
		},
		OS: models.OperatingSystem{
			Name:    "Windows 11 Pro",
			Version: "10.0.26220",
		},
	}

	env := buildAgentHardwareEnvelope(report)
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	status, ok := payload["status"].(string)
	if !ok {
		t.Fatalf("status deve ser string, veio %T", payload["status"])
	}
	if status != "Online" {
		t.Fatalf("status = %q, esperado %q", status, "Online")
	}

	if _, ok := payload["inventoryRaw"].(map[string]any); !ok {
		t.Fatalf("inventoryRaw deve ser objeto JSON no payload, veio %T", payload["inventoryRaw"])
	}

	hw, ok := payload["hardware"].(map[string]any)
	if !ok {
		t.Fatalf("hardware deve ser objeto JSON")
	}
	if _, ok := hw["inventoryRaw"].(map[string]any); !ok {
		t.Fatalf("hardware.inventoryRaw deve ser objeto JSON no payload, veio %T", hw["inventoryRaw"])
	}
}

func TestBuildAgentHardwareEnvelope_FiltersInvalidRequiredComponents(t *testing.T) {
	report := models.InventoryReport{
		CollectedAt: "2026-03-12T19:31:36Z",
		Hardware: models.HardwareInfo{
			Hostname: "PC-123",
		},
		OS: models.OperatingSystem{
			Name: "Windows",
		},
		Disks: []models.DiskInfo{
			{Device: "", Label: "sem letra"},
			{Device: "c", Label: "tambem sem letra"},
			{Device: "C:", Label: "valido"},
		},
		Networks: []models.NetworkInfo{
			{FriendlyName: "", Interface: ""},
			{FriendlyName: "Ethernet", Interface: "eth0"},
		},
	}

	env := buildAgentHardwareEnvelope(report)
	if len(env.Components.Disks) != 2 {
		t.Fatalf("esperado 2 discos validos (somente o vazio deve ser filtrado), veio %d", len(env.Components.Disks))
	}
	if env.Components.Disks[1].DriveLetter != "C:" {
		t.Fatalf("driveLetter = %q, esperado %q", env.Components.Disks[1].DriveLetter, "C:")
	}

	if len(env.Components.NetworkAdapters) != 1 {
		t.Fatalf("esperado 1 adaptador valido, veio %d", len(env.Components.NetworkAdapters))
	}
	if env.Components.NetworkAdapters[0].Name != "Ethernet" {
		t.Fatalf("adapter name = %q, esperado %q", env.Components.NetworkAdapters[0].Name, "Ethernet")
	}
}

func TestBuildAgentSoftwareEnvelope_AppliesContractLimits(t *testing.T) {
	veryLong := strings.Repeat("x", 2000)
	report := models.InventoryReport{
		Software: []models.SoftwareItem{
			{
				Name:      veryLong,
				Version:   veryLong,
				Publisher: veryLong,
				InstallID: veryLong,
				Serial:    veryLong,
				Source:    "",
			},
			{Name: ""},
		},
	}

	env := buildAgentSoftwareEnvelope(report)
	if len(env.Software) != 1 {
		t.Fatalf("esperado 1 software valido, veio %d", len(env.Software))
	}
	item := env.Software[0]
	if len(item.Name) > 300 {
		t.Fatalf("name excedeu limite: %d", len(item.Name))
	}
	if len(item.Version) > 120 {
		t.Fatalf("version excedeu limite: %d", len(item.Version))
	}
	if len(item.Publisher) > 300 {
		t.Fatalf("publisher excedeu limite: %d", len(item.Publisher))
	}
	if len(item.InstallID) > 1000 {
		t.Fatalf("installId excedeu limite: %d", len(item.InstallID))
	}
	if len(item.Serial) > 1000 {
		t.Fatalf("serial excedeu limite: %d", len(item.Serial))
	}
	if item.Source != "osquery/programs" {
		t.Fatalf("source = %q, esperado fallback %q", item.Source, "osquery/programs")
	}
}

func TestBuildAgentHardwareEnvelope_IncludesPrintersInComponents(t *testing.T) {
	report := models.InventoryReport{
		CollectedAt: "2026-03-12T19:31:36Z",
		Hardware: models.HardwareInfo{
			Hostname: "PC-123",
		},
		OS: models.OperatingSystem{
			Name: "Windows 11 Pro",
		},
		Printers: []models.PrinterInfo{
			{
				Name:             "HP LaserJet Pro M404",
				DriverName:       "HP Universal Printing PCL 6",
				PortName:         "IP_192.168.1.50",
				PrinterStatus:    "Ready",
				IsDefault:        true,
				IsNetworkPrinter: true,
				Shared:           false,
				Location:         "Financeiro",
			},
		},
	}

	env := buildAgentHardwareEnvelope(report)
	if len(env.Components.Printers) != 1 {
		t.Fatalf("esperado 1 impressora no components.printers, veio %d", len(env.Components.Printers))
	}
	p := env.Components.Printers[0]
	if p.Name != "HP LaserJet Pro M404" {
		t.Fatalf("name = %q", p.Name)
	}
	if p.DriverName != "HP Universal Printing PCL 6" {
		t.Fatalf("driverName = %q", p.DriverName)
	}
	if p.PortName != "IP_192.168.1.50" {
		t.Fatalf("portName = %q", p.PortName)
	}
	if !p.IsDefault || !p.IsNetworkPrinter {
		t.Fatalf("flags de impressora invalidas: isDefault=%v isNetworkPrinter=%v", p.IsDefault, p.IsNetworkPrinter)
	}
	if p.ShareName != nil {
		t.Fatalf("shareName esperado nil para impressora nao compartilhada, veio %v", *p.ShareName)
	}
}
